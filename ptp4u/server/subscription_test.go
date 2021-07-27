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
	"testing"
	"time"

	ptp "github.com/facebookincubator/ptp/protocol"

	"github.com/stretchr/testify/require"
)

func TestRunning(t *testing.T) {
	sc := SubscriptionClient{}
	sc.setRunning(true)
	require.True(t, sc.Running())

	sc.setRunning(false)
	require.False(t, sc.Running())
}

func TestSubscriptionStart(t *testing.T) {
	w := &sendWorker{}
	interval := 1 * time.Minute
	expire := time.Now().Add(1 * time.Second)
	sc := NewSubscriptionClient(w.queue, net.ParseIP("127.0.0.1"), ptp.MessageAnnounce, interval, expire)

	go sc.Start()
	time.Sleep(100 * time.Millisecond)
	require.True(t, sc.Running())
}

func TestSubscriptionStop(t *testing.T) {
	w := &sendWorker{
		queue: make(chan *SubscriptionClient, 100),
	}
	interval := 10 * time.Millisecond
	expire := time.Now().Add(1 * time.Second)
	sc := NewSubscriptionClient(w.queue, net.ParseIP("127.0.0.1"), ptp.MessageAnnounce, interval, expire)

	go sc.Start()
	time.Sleep(100 * time.Millisecond)
	require.True(t, sc.Running())
	sc.Stop()
	time.Sleep(100 * time.Millisecond)
	require.False(t, sc.Running())
}

func TestSubscriptionflags(t *testing.T) {
	c := &Config{clockIdentity: ptp.ClockIdentity(1234)}
	w := NewSendWorker(0, c, nil)
	require.Equal(t, ptp.FlagUnicast|ptp.FlagTwoStep, w.syncPacket(1).Header.FlagField)
	require.Equal(t, ptp.FlagUnicast, w.followupPacket(time.Second, 1, time.Now()).Header.FlagField)
	require.Equal(t, ptp.FlagUnicast|ptp.FlagPTPTimescale, w.announcePacket(time.Second, 1).Header.FlagField)
}
