/*
Copyright (c) Facebook, Inc. and its affiliates.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
Package server implements simple UDP server to work with NTP packets.
In addition, it run checker, announce and stats implementations
*/

package server

import (
	"encoding/binary"
	"net"
	"time"

	ptp "github.com/facebookincubator/ptp/protocol"
	"github.com/facebookincubator/ptp/ptp4u/stats"
	log "github.com/sirupsen/logrus"
)

type subAction struct {
	clientIP  net.IP
	interval  time.Duration
	expire    time.Time
	grantType ptp.MessageType
	identity  ptp.PortIdentity
}

// delayWorker monitors the queue of jobs
type delayWorker struct {
	id       int
	subQueue chan *subAction

	config *Config
	stats  stats.Stats

	generalConn *net.UDPConn
	eventConn   *net.UDPConn

	delayRespP *ptp.DelayResp
	signalingP *ptp.Signaling

	syncDelayReq *ptp.SyncDelayReq

	gcliAddr *net.UDPAddr
}

func NewDelayWorker(id int, subQueue chan *subAction, c *Config, st stats.Stats, generalConn, eventConn *net.UDPConn) *delayWorker {
	w := &delayWorker{
		id:          id,
		subQueue:    subQueue,
		config:      c,
		stats:       st,
		generalConn: generalConn,
		eventConn:   eventConn,
	}
	w.signalingP = &ptp.Signaling{}
	w.syncDelayReq = &ptp.SyncDelayReq{}
	w.initDelayRespPacket()
	w.gcliAddr = &net.UDPAddr{IP: nil, Port: ptp.PortGeneral}
	return w
}

func (w *delayWorker) initDelayRespPacket() *ptp.DelayResp {
	w.delayRespP = &ptp.DelayResp{
		Header: ptp.Header{
			SdoIDAndMsgType: ptp.NewSdoIDAndMsgType(ptp.MessageDelayResp, 0),
			Version:         ptp.Version,
			MessageLength:   uint16(binary.Size(ptp.DelayResp{})),
			DomainNumber:    0,
			FlagField:       ptp.FlagUnicast,
			SequenceID:      0,
			SourcePortIdentity: ptp.PortIdentity{
				PortNumber:    1,
				ClockIdentity: w.config.clockIdentity,
			},
			LogMessageInterval: 0x7f,
			ControlField:       3,
			CorrectionField:    0,
		},
		DelayRespBody: ptp.DelayRespBody{},
	}
	return w.delayRespP
}

// delayRespPacket generates ptp Delay Response packet
func (w *delayWorker) delayRespPacket(h *ptp.Header, received time.Time) *ptp.DelayResp {
	w.delayRespP.SequenceID = h.SequenceID
	w.delayRespP.CorrectionField = h.CorrectionField
	w.delayRespP.ReceiveTimestamp = ptp.NewTimestamp(received)
	w.delayRespP.RequestingPortIdentity = h.SourcePortIdentity
	return w.delayRespP
}

func (w *delayWorker) start() {

	go func() {
		wbuf := make([]byte, ptp.PayloadSizeBytes)
		buf := make([]byte, ptp.PayloadSizeBytes)
		var (
			n       int
			remAddr *net.UDPAddr
			err     error
		)
		for {
			n, remAddr, err = w.generalConn.ReadFromUDP(buf)
			if err != nil {
				log.Errorf("Failed to read packet on %s: %v", w.generalConn.LocalAddr(), err)
				continue
			}
			w.handleGeneralMessage(buf[:n], remAddr.IP, wbuf)
		}
	}()

	// buffers
	wbuf := make([]byte, ptp.PayloadSizeBytes)
	oob := make([]byte, ptp.ControlSizeBytes)
	buf := make([]byte, ptp.PayloadSizeBytes)
	emptyo := make([]byte, ptp.ControlSizeBytes)

	var (
		bufn     int
		clientIP net.IP
		rxTS     time.Time
		err      error
	)

	for {
		// reset oob buffer
		copy(oob, emptyo)
		bufn, clientIP, rxTS, err = ptp.ReadPacketWithRXTimestampBuf(w.eventConn, buf, oob)
		if err != nil {
			log.Errorf("Failed to read packet on %s: %v", w.eventConn.LocalAddr(), err)
			continue
		}
		if w.config.TimestampType != ptp.HWTIMESTAMP {
			rxTS = rxTS.Add(w.config.UTCOffset)
		}
		w.handleEventMessage(buf[:bufn], clientIP, rxTS, wbuf)
	}
}

// handleEventMessage is a handler which gets called every time Event Message arrives
func (w *delayWorker) handleEventMessage(request []byte, clientIP net.IP, rxTS time.Time, buf []byte) {
	msgType, err := ptp.ProbeMsgType(request)
	if err != nil {
		log.Errorf("Failed to probe the ptp message type: %v", err)
		return
	}

	switch msgType {
	case ptp.MessageDelayReq:
		if err := ptp.FromBytes(request, w.syncDelayReq); err != nil {
			log.Errorf("Failed to read the ptp SyncDelayReq: %v", err)
			return
		}

		log.Debugf("Got delay request")
		dResp := w.delayRespPacket(&w.syncDelayReq.Header, rxTS)
		n, err := ptp.BytesTo(dResp, buf)
		if err != nil {
			log.Errorf("Failed to prepare the delay response: %v", err)
			return
		}
		w.gcliAddr.IP = clientIP
		if w.generalConn == nil {
			panic("ooops")
		}
		_, err = w.generalConn.WriteToUDP(buf[:n], w.gcliAddr)
		if err != nil {
			log.Errorf("Failed to send the delay response: %v", err)
			return
		}
		log.Debugf("Sent delay response")
		w.stats.IncTX(ptp.MessageDelayResp)

	default:
		log.Errorf("Got unsupported message type %s(%d)", msgType, msgType)
	}

	w.stats.IncRX(msgType)
}

// grantUnicastTransmission generates ptp Signaling packet granting the requested subscription
func (w *delayWorker) grantUnicastTransmission(sg *ptp.Signaling, mt ptp.UnicastMsgTypeAndFlags, interval ptp.LogInterval, duration uint32) *ptp.Signaling {
	m := &ptp.Signaling{
		Header:             sg.Header,
		TargetPortIdentity: sg.SourcePortIdentity,
		TLVs: []ptp.TLV{
			&ptp.GrantUnicastTransmissionTLV{
				TLVHead: ptp.TLVHead{
					TLVType:     ptp.TLVGrantUnicastTransmission,
					LengthField: uint16(binary.Size(ptp.GrantUnicastTransmissionTLV{}) - binary.Size(ptp.TLVHead{})),
				},
				MsgTypeAndReserved:    mt,
				LogInterMessagePeriod: interval,
				DurationField:         duration,
				Reserved:              0,
				Renewal:               1,
			},
		},
	}

	m.Header.FlagField = ptp.FlagUnicast
	m.Header.SourcePortIdentity = ptp.PortIdentity{
		PortNumber:    1,
		ClockIdentity: w.config.clockIdentity,
	}
	m.Header.MessageLength = uint16(binary.Size(ptp.Header{}) + binary.Size(ptp.PortIdentity{}) + binary.Size(ptp.GrantUnicastTransmissionTLV{}))
	return m
}

// sendGrant sends a Unicast Grant message
func (w *delayWorker) sendGrant(t ptp.MessageType, sg *ptp.Signaling, mt ptp.UnicastMsgTypeAndFlags, interval ptp.LogInterval, duration uint32, clientIP net.IP, buf []byte) {
	grant := w.grantUnicastTransmission(sg, mt, interval, duration)
	var err error
	n, err := ptp.BytesTo(grant, buf)
	if err != nil {
		log.Errorf("Failed to prepare the unicast grant: %v", err)
		return
	}
	gcliAddr := &net.UDPAddr{IP: clientIP, Port: ptp.PortGeneral}
	_, err = w.generalConn.WriteToUDP(buf[:n], gcliAddr)
	if err != nil {
		log.Errorf("Failed to send the unicast grant: %v", err)
		return
	}
	log.Debugf("Sent unicast grant")
	w.stats.IncTXSignaling(t)
}

// handleGeneralMessage is a handler which gets called every time General Message arrives
func (w *delayWorker) handleGeneralMessage(request []byte, clientIP net.IP, buf []byte) {
	msgType, err := ptp.ProbeMsgType(request)
	if err != nil {
		log.Errorf("Failed to probe the ptp message type: %v", err)
		return
	}

	switch msgType {
	case ptp.MessageSignaling:
		signaling := &ptp.Signaling{}
		if err := ptp.FromBytes(request, signaling); err != nil {
			log.Error(err)
			return
		}

		for _, tlv := range signaling.TLVs {
			switch v := tlv.(type) {
			case *ptp.RequestUnicastTransmissionTLV:
				grantType := v.MsgTypeAndReserved.MsgType()

				switch grantType {
				case ptp.MessageAnnounce, ptp.MessageSync:
					duration := v.DurationField
					durationt := time.Duration(duration) * time.Second

					interval := v.LogInterMessagePeriod
					intervalt := interval.Duration()
					// Reject queries out of limit
					if intervalt < w.config.MinSubInterval || durationt > w.config.MaxSubDuration {
						log.Warningf("Got too demanding %s request. Duration: %s, Interval: %s. Rejecting. Consider changing -maxsubduration and -minsubinterval", grantType, durationt, intervalt)
						w.sendGrant(grantType, signaling, v.MsgTypeAndReserved, interval, 0, clientIP, buf)
						return
					}

					expire := time.Now().Add(durationt)

					// tell server to start/renew this subscription
					w.subQueue <- &subAction{
						clientIP:  clientIP,
						interval:  intervalt,
						expire:    expire,
						grantType: grantType,
						identity:  signaling.SourcePortIdentity,
					}

					// Send confirmation grant
					w.sendGrant(grantType, signaling, v.MsgTypeAndReserved, interval, duration, clientIP, buf)

				case ptp.MessageDelayResp:
					// Send confirmation grant
					w.sendGrant(grantType, signaling, v.MsgTypeAndReserved, 0, v.DurationField, clientIP, buf)

				default:
					log.Errorf("Got unsupported grant type %s", grantType)
				}
				w.stats.IncRXSignaling(grantType)
			case *ptp.CancelUnicastTransmissionTLV:
				grantType := v.MsgTypeAndFlags.MsgType()
				log.Debugf("Got %s cancel request", grantType)
				// tell server to remove this subscription
				w.subQueue <- &subAction{
					interval:  0,
					grantType: grantType,
					identity:  signaling.SourcePortIdentity,
				}

			default:
				log.Errorf("Got unsupported message type %s(%d)", msgType, msgType)
			}
		}
	}
}
