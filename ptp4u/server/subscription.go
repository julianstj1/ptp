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
	"net"
	"sync"
	"time"

	ptp "github.com/facebookincubator/ptp/protocol"
	log "github.com/sirupsen/logrus"
)

// SubscriptionClient is sending subscriptionType messages periodically
type SubscriptionClient struct {
	sync.Mutex
	subscriptionType ptp.MessageType

	interval   time.Duration
	expire     time.Time
	sequenceID uint16
	running    bool
	queue      chan *SubscriptionClient

	// addresses
	ecliAddr *net.UDPAddr
	gcliAddr *net.UDPAddr
}

// NewSubscriptionClient gets minimal required arguments to create a subscription
func NewSubscriptionClient(queue chan *SubscriptionClient, ip net.IP, st ptp.MessageType, i time.Duration, e time.Time) *SubscriptionClient {
	s := &SubscriptionClient{
		queue:            queue,
		ecliAddr:         &net.UDPAddr{IP: ip, Port: ptp.PortEvent},
		gcliAddr:         &net.UDPAddr{IP: ip, Port: ptp.PortGeneral},
		subscriptionType: st,
		interval:         i,
		expire:           e,
	}
	return s
}

// Start launches the subscription timers and exit on expire
func (sc *SubscriptionClient) Start() {
	sc.setRunning(true)
	defer sc.Stop()
	log.Infof("Starting a new %s subscription for %s", sc.subscriptionType, sc.ecliAddr.IP)

	// Send first message right away
	sc.queue <- sc

	intervalTicker := time.NewTicker(sc.interval)
	oldInterval := sc.interval

	var now time.Time

	defer intervalTicker.Stop()
	for range intervalTicker.C {
		if !sc.Running() {
			return
		}
		log.Debugf("Subscription %s for %s is valid until %s", sc.subscriptionType, sc.ecliAddr.IP, sc.expire)
		now = time.Now()
		if now.After(sc.expire) {
			log.Infof("Subscription %s is over for %s", sc.subscriptionType, sc.ecliAddr.IP)
			// TODO send cancellation
			return
		}
		// check if interval changed, maybe update our ticker
		if oldInterval != sc.interval {
			intervalTicker.Reset(sc.interval)
			oldInterval = sc.interval
		}
		// Add myself to the worker queue
		sc.queue <- sc
	}
}

// setRunning sets running with the lock
func (sc *SubscriptionClient) setRunning(running bool) {
	sc.Lock()
	defer sc.Unlock()
	sc.running = running
}

// Running returns the status of the Subscription
func (sc *SubscriptionClient) Running() bool {
	sc.Lock()
	defer sc.Unlock()
	return sc.running
}

// Stop stops the subscription
func (sc *SubscriptionClient) Stop() {
	sc.setRunning(false)
}
