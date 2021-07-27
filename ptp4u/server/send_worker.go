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

package server

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	ptp "github.com/facebookincubator/ptp/protocol"
	"github.com/facebookincubator/ptp/ptp4u/stats"
	log "github.com/sirupsen/logrus"
)

// sendWorker monitors the queue of jobs
type sendWorker struct {
	mux sync.Mutex

	id     int
	config *Config
	stats  stats.Stats

	syncClients     map[ptp.PortIdentity]*SubscriptionClient
	announceClients map[ptp.PortIdentity]*SubscriptionClient

	queue chan *SubscriptionClient

	// packets
	syncP     *ptp.SyncDelayReq
	followupP *ptp.FollowUp
	announceP *ptp.Announce
}

func NewSendWorker(i int, c *Config, st stats.Stats) *sendWorker {
	s := &sendWorker{
		id:     i,
		config: c,
		stats:  st,
	}
	s.syncClients = make(map[ptp.PortIdentity]*SubscriptionClient)
	s.announceClients = make(map[ptp.PortIdentity]*SubscriptionClient)
	s.queue = make(chan *SubscriptionClient, c.QueueSize)
	s.initSync()
	s.initAnnounce()
	s.initFollowup()
	return s
}

func (s *sendWorker) initSync() {
	s.syncP = &ptp.SyncDelayReq{
		Header: ptp.Header{
			SdoIDAndMsgType: ptp.NewSdoIDAndMsgType(ptp.MessageSync, 0),
			Version:         ptp.Version,
			MessageLength:   uint16(binary.Size(ptp.SyncDelayReq{})),
			DomainNumber:    0,
			FlagField:       ptp.FlagUnicast | ptp.FlagTwoStep,
			SequenceID:      0,
			SourcePortIdentity: ptp.PortIdentity{
				PortNumber:    1,
				ClockIdentity: s.config.clockIdentity,
			},
			LogMessageInterval: 0x7f,
			ControlField:       0,
		},
	}
}

// syncPacket generates ptp Sync packet
func (s *sendWorker) syncPacket(sequenceID uint16) *ptp.SyncDelayReq {
	s.syncP.SequenceID = sequenceID
	return s.syncP
}

func (s *sendWorker) initFollowup() {
	s.followupP = &ptp.FollowUp{
		Header: ptp.Header{
			SdoIDAndMsgType: ptp.NewSdoIDAndMsgType(ptp.MessageFollowUp, 0),
			Version:         ptp.Version,
			MessageLength:   uint16(binary.Size(ptp.FollowUp{})),
			DomainNumber:    0,
			FlagField:       ptp.FlagUnicast,
			SequenceID:      0,
			SourcePortIdentity: ptp.PortIdentity{
				PortNumber:    1,
				ClockIdentity: s.config.clockIdentity,
			},
			LogMessageInterval: 0,
			ControlField:       2,
		},
		FollowUpBody: ptp.FollowUpBody{
			PreciseOriginTimestamp: ptp.NewTimestamp(time.Now()),
		},
	}
}

// followupPacket generates ptp Follow Up packet
func (s *sendWorker) followupPacket(interval time.Duration, sequenceID uint16, hwts time.Time) *ptp.FollowUp {
	i, err := ptp.NewLogInterval(interval)
	if err != nil {
		log.Errorf("Failed to get interval: %v", err)
	}

	s.followupP.SequenceID = sequenceID
	s.followupP.LogMessageInterval = i
	s.followupP.PreciseOriginTimestamp = ptp.NewTimestamp(hwts)
	return s.followupP
}

func (s *sendWorker) initAnnounce() {
	s.announceP = &ptp.Announce{
		Header: ptp.Header{
			SdoIDAndMsgType: ptp.NewSdoIDAndMsgType(ptp.MessageAnnounce, 0),
			Version:         ptp.Version,
			MessageLength:   uint16(binary.Size(ptp.Announce{})),
			DomainNumber:    0,
			FlagField:       ptp.FlagUnicast | ptp.FlagPTPTimescale,
			SequenceID:      0,
			SourcePortIdentity: ptp.PortIdentity{
				PortNumber:    1,
				ClockIdentity: s.config.clockIdentity,
			},
			LogMessageInterval: 0,
			ControlField:       5,
		},
		AnnounceBody: ptp.AnnounceBody{
			CurrentUTCOffset:     0,
			Reserved:             0,
			GrandmasterPriority1: 128,
			GrandmasterClockQuality: ptp.ClockQuality{
				ClockClass:              6,
				ClockAccuracy:           33, // 0x21 - Time Accurate within 100ns
				OffsetScaledLogVariance: 23008,
			},
			GrandmasterPriority2: 128,
			GrandmasterIdentity:  s.config.clockIdentity,
			StepsRemoved:         0,
			TimeSource:           ptp.TimeSourceGNSS,
		},
	}
}

// announcePacket generates ptp Announce packet
func (s *sendWorker) announcePacket(interval time.Duration, sequenceID uint16) *ptp.Announce {
	i, err := ptp.NewLogInterval(interval)
	if err != nil {
		log.Errorf("Failed to get interval: %v", err)
	}

	s.announceP.SequenceID = sequenceID
	s.announceP.LogMessageInterval = i
	s.announceP.CurrentUTCOffset = int16(s.config.UTCOffset.Seconds())

	return s.announceP
}

// Start a SendWorker which will pull data from the queue and send Sync and Followup packets
func (s *sendWorker) Start() {
	econn, err := net.ListenUDP("udp", &net.UDPAddr{IP: s.config.IP, Port: 0})
	if err != nil {
		log.Fatalf("Binding to event socket error: %s", err)
	}
	defer econn.Close()

	// get connection file descriptor
	eFd, err := ptp.ConnFd(econn)
	if err != nil {
		log.Fatalf("Getting event connection FD: %s", err)
	}

	// Syncs sent from event port
	if s.config.TimestampType == ptp.HWTIMESTAMP {
		if err := ptp.EnableHWTimestampsSocket(econn, s.config.Interface); err != nil {
			log.Fatalf("Failed to enable RX hardware timestamps: %v", err)
		}
	} else if s.config.TimestampType == ptp.SWTIMESTAMP {
		if err := ptp.EnableSWTimestampsSocket(econn); err != nil {
			log.Fatalf("Unable to enable RX software timestamps")
		}
	} else {
		log.Fatalf("Unrecognized timestamp type: %s", s.config.TimestampType)
	}

	gconn, err := net.ListenUDP("udp", &net.UDPAddr{IP: s.config.IP, Port: 0})
	if err != nil {
		log.Fatalf("Binding to general socket error: %s", err)
	}
	defer gconn.Close()

	buf := make([]byte, ptp.PayloadSizeBytes)

	// reusable buffers for ReadTXtimestampBuf
	bbuf := make([]byte, ptp.PayloadSizeBytes)
	oob := make([]byte, ptp.ControlSizeBytes)

	// TMP buffers
	tbuf := make([]byte, ptp.PayloadSizeBytes)
	toob := make([]byte, ptp.ControlSizeBytes)

	// arrays of zeroes to reset buffers
	emptyb := make([]byte, ptp.PayloadSizeBytes)
	emptyo := make([]byte, ptp.ControlSizeBytes)

	// TODO: Enable dscp accordingly

	var (
		n        int
		sync     *ptp.SyncDelayReq
		followup *ptp.FollowUp
		announce *ptp.Announce

		txTS     time.Time
		attempts int
	)

	for c := range s.queue {
		// clean up buffers
		copy(bbuf, emptyb)
		copy(oob, emptyo)
		copy(tbuf, emptyb)
		copy(toob, emptyo)

		switch c.subscriptionType {
		case ptp.MessageSync:
			// send sync

			sync = s.syncPacket(c.sequenceID)
			n, err = ptp.BytesTo(sync, buf)
			if err != nil {
				log.Errorf("Failed to generate the sync packet: %v", err)
				continue
			}
			log.Debugf("Sending sync")
			_, err = econn.WriteToUDP(buf[:n], c.ecliAddr)
			if err != nil {
				log.Errorf("Failed to send the sync packet: %v", err)
				continue
			}
			s.stats.IncTX(ptp.MessageSync)

			txTS, attempts, err = ptp.ReadTXtimestampBuf(eFd, bbuf, oob, tbuf, toob)
			s.stats.SetMaxTXTSAttempts(s.id, int64(attempts))
			if err != nil {
				log.Warningf("Failed to read TX timestamp: %v", err)
				continue
			}
			if s.config.TimestampType != ptp.HWTIMESTAMP {
				txTS = txTS.Add(s.config.UTCOffset)
			}
			log.Debugf("Read TX timestamp: %v", txTS)

			// send followup
			followup = s.followupPacket(c.interval, c.sequenceID, txTS)
			n, err = ptp.BytesTo(followup, buf)
			if err != nil {
				log.Errorf("Failed to generate the followup packet: %v", err)
				continue
			}
			log.Debugf("Sending followup")

			_, err = gconn.WriteToUDP(buf[:n], c.gcliAddr)
			if err != nil {
				log.Errorf("Failed to send the followup packet: %v", err)
				continue
			}
			s.stats.IncTX(ptp.MessageFollowUp)
		case ptp.MessageAnnounce:
			// send announce
			announce = s.announcePacket(c.interval, c.sequenceID)
			n, err = ptp.BytesTo(announce, buf)
			if err != nil {
				log.Errorf("Failed to prepare the unicast announce: %v", err)
				continue
			}
			log.Debugf("Sending announce")

			_, err = gconn.WriteToUDP(buf[:n], c.gcliAddr)
			if err != nil {
				log.Errorf("Failed to send the unicast announce: %v", err)
				continue
			}
			s.stats.IncTX(ptp.MessageAnnounce)
		default:
			log.Errorf("Unknown subscription type: %v", c.subscriptionType)
			continue
		}

		c.sequenceID++
		s.stats.SetMaxWorkerQueue(s.id, int64(len(s.queue)))
	}
}

// client retrieves an existing client
func (s *sendWorker) findSubscription(clientID ptp.PortIdentity, st ptp.MessageType) *SubscriptionClient {
	switch st {
	case ptp.MessageSync:
		sub, ok := s.syncClients[clientID]
		if !ok {
			return nil
		}
		return sub
	case ptp.MessageAnnounce:
		sub, ok := s.announceClients[clientID]
		if !ok {
			return nil
		}
		return sub
	default:
		panic(fmt.Sprintf("unsupported message type %v", st))
	}
}

// registerSubscription will overwrite an existing subscription.
// Make sure you call findSubscription before this
func (s *sendWorker) registerSubscription(clientID ptp.PortIdentity, st ptp.MessageType, sc *SubscriptionClient) {
	switch st {
	case ptp.MessageSync:
		s.syncClients[clientID] = sc
	case ptp.MessageAnnounce:
		s.announceClients[clientID] = sc
	default:
		panic(fmt.Sprintf("unsupported message type %v", st))
	}
}

func (s *sendWorker) inventoryClients() {
	s.mux.Lock()
	defer s.mux.Unlock()
	for k, sc := range s.syncClients {
		if !sc.Running() {
			delete(s.syncClients, k)
			continue
		}
		s.stats.IncSubscription(ptp.MessageSync)
	}
	for k, sc := range s.announceClients {
		if !sc.Running() {
			delete(s.announceClients, k)
			continue
		}
		s.stats.IncSubscription(ptp.MessageAnnounce)
	}
}

func (s *sendWorker) addClient(clientID ptp.PortIdentity, grantType ptp.MessageType, clientIP net.IP, interval time.Duration, expire time.Time) {
	s.mux.Lock()
	defer s.mux.Unlock()
	sc := s.findSubscription(clientID, grantType)
	if sc == nil {
		sc = NewSubscriptionClient(s.queue, clientIP, grantType, interval, expire)
		log.Infof("Starting a new %s subscription for %s", sc.subscriptionType, sc.ecliAddr.IP)
		s.registerSubscription(clientID, grantType, sc)
	} else {
		// Update existing subscription data
		sc.expire = expire
		sc.interval = interval
	}
	if !sc.Running() {
		go sc.Start()
	}
}

func (s *sendWorker) removeClient(clientID ptp.PortIdentity, grantType ptp.MessageType) {
	s.mux.Lock()
	defer s.mux.Unlock()
	sc := s.findSubscription(clientID, grantType)
	if sc != nil {
		sc.Stop()
	}
}
