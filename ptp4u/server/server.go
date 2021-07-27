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
	"fmt"
	"hash/fnv"
	"net"
	"sync"
	"time"

	ptp "github.com/facebookincubator/ptp/protocol"
	"github.com/facebookincubator/ptp/ptp4u/stats"
	log "github.com/sirupsen/logrus"
)

// Server is PTP unicast server
type Server struct {
	Config *Config
	Stats  stats.Stats
	sw     []*sendWorker
	dw     []*delayWorker

	eventConn   *net.UDPConn
	generalConn *net.UDPConn

	subQueue chan *subAction
}

// Start the workers send bind to event and general UDP ports
func (s *Server) Start() error {
	// Set clock identity
	iface, err := net.InterfaceByName(s.Config.Interface)
	if err != nil {
		return fmt.Errorf("unable to get mac address of the interface: %v", err)
	}
	s.Config.clockIdentity, err = ptp.NewClockIdentity(iface.HardwareAddr)
	if err != nil {
		return fmt.Errorf("unable to get the Clock Identity (EUI-64 address) of the interface: %v", err)
	}

	// Call wg.Add(1) ONLY once
	// If ANY goroutine finishes no matter how many of them we run
	// wg.Done will unblock
	var wg sync.WaitGroup
	wg.Add(1)

	// start both port listeners
	log.Infof("Binding on %s %d", s.Config.IP, ptp.PortGeneral)
	s.generalConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: s.Config.IP, Port: ptp.PortGeneral})
	if err != nil {
		return fmt.Errorf("starting general port listener: %s", err)
	}
	defer s.generalConn.Close()

	log.Infof("Binding on %s %d", s.Config.IP, ptp.PortEvent)
	s.eventConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: s.Config.IP, Port: ptp.PortEvent})
	if err != nil {
		return fmt.Errorf("starting event port listener: %s", err)
	}
	defer s.eventConn.Close()

	if err := s.enableTimestamps(); err != nil {
		return err
	}

	// start X workers
	s.sw = make([]*sendWorker, s.Config.Workers)
	s.dw = make([]*delayWorker, s.Config.Workers)
	s.subQueue = make(chan *subAction, s.Config.QueueSize)
	for i := 0; i < s.Config.Workers; i++ {
		// Each worker to monitor own queue
		s.sw[i] = NewSendWorker(i, s.Config, s.Stats)
		go func(i int) {
			defer wg.Done()
			s.sw[i].Start()
		}(i)
		// Single queue for all delay workers as they share outgoing connection
		s.dw[i] = NewDelayWorker(i, s.subQueue, s.Config, s.Stats, s.generalConn, s.eventConn)
		go func(i int) {
			defer wg.Done()
			s.dw[i].start()
		}(i)
	}

	go func() {
		defer wg.Done()
		for sub := range s.subQueue {
			if sub.interval > 0 {
				worker := s.findWorker(sub.identity, sub.grantType)
				worker.addClient(sub.identity, sub.grantType, sub.clientIP, sub.interval, sub.expire)
			} else {
				worker := s.findWorker(sub.identity, sub.grantType)
				worker.removeClient(sub.identity, sub.grantType)
			}

		}
	}()

	// Run active metric reporting
	go func() {
		defer wg.Done()
		for {
			<-time.After(s.Config.MetricInterval)
			for _, w := range s.sw {
				w.inventoryClients()
			}
			s.Stats.SetUTCOffset(int64(s.Config.UTCOffset.Seconds()))

			s.Stats.Snapshot()
			s.Stats.Reset()
		}
	}()

	// Update UTC offset periodically
	go func() {
		defer wg.Done()
		for {
			<-time.After(1 * time.Minute)
			if s.Config.SHM {
				if err := s.Config.SetUTCOffsetFromSHM(); err != nil {
					log.Errorf("Failed to update UTC offset: %v. Keeping the last known: %s", err, s.Config.UTCOffset)
				}
			}
			log.Debugf("UTC offset is: %v", s.Config.UTCOffset.Seconds())
		}
	}()

	// Wait for ANY gorouine to finish
	wg.Wait()
	return fmt.Errorf("one of server routines finished")
}

func (s *Server) enableTimestamps() error {
	// Enable RX timestamps. Delay requests need to be timestamped by ptp4u on receipt
	if s.Config.TimestampType == ptp.HWTIMESTAMP {
		if err := ptp.EnableHWTimestampsSocket(s.eventConn, s.Config.Interface); err != nil {
			return fmt.Errorf("Cannot enable hardware RX timestamps")
		}
	} else if s.Config.TimestampType == ptp.SWTIMESTAMP {
		if err := ptp.EnableSWTimestampsSocket(s.eventConn); err != nil {
			return fmt.Errorf("Cannot enable software RX timestamps")
		}
	} else {
		return fmt.Errorf("Unrecognized timestamp type: %s", s.Config.TimestampType)
	}
	return nil
}

func hashClientID(clientID ptp.PortIdentity) uint32 {
	h := fnv.New32a()
	_ = binary.Write(h, binary.BigEndian, clientID)
	return h.Sum32()
}

func (s *Server) findWorker(clientID ptp.PortIdentity, st ptp.MessageType) *sendWorker {
	hash := hashClientID(clientID)
	return s.sw[hash%uint32(s.Config.Workers)]
}
