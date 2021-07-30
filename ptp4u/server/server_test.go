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
	"github.com/facebookincubator/ptp/ptp4u/stats"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Testing conversion so if Packet structure changes we notice
func TestServerFindLeastBusyWorkerID(t *testing.T) {
	s := Server{
		Config: &Config{Workers: 3},
	}

	s.sw = make([]*sendWorker, s.Config.Workers)
	for i := 0; i < s.Config.Workers; i++ {
		s.sw[i] = &sendWorker{}
	}

	require.Equal(t, 0, s.findLeastBusyWorkerID())

	s.sw[0].load = 1234
	require.Equal(t, 1, s.findLeastBusyWorkerID())

	s.sw[1].load = 8
	require.Equal(t, 2, s.findLeastBusyWorkerID())

	s.sw[2].load = 100500
	require.Equal(t, 1, s.findLeastBusyWorkerID())

	s.sw[1].load = 1000000
	require.Equal(t, 0, s.findLeastBusyWorkerID())
}

func TestServerRegisterSubscription(t *testing.T) {
	var (
		scE *SubscriptionClient
		scS *SubscriptionClient
		scA *SubscriptionClient
		scT *SubscriptionClient
	)

	ci := ptp.ClockIdentity(1234)
	pi := ptp.PortIdentity{
		PortNumber:    1,
		ClockIdentity: ptp.ClockIdentity(1234),
	}

	c := &Config{clockIdentity: ci}
	s := Server{Config: c}
	s.clients.init()

	// Nothing should be there
	scE = s.findSubscription(pi, ptp.MessageSync)
	require.Nil(t, scE)

	scE = s.findSubscription(pi, ptp.MessageAnnounce)
	require.Nil(t, scE)

	// Add Sync. Check we got
	sa := ipToSockaddr(net.ParseIP("127.0.0.1"), 123)
	scS = NewSubscriptionClient(&sendWorker{}, sa, sa, ptp.MessageSync, c, time.Second, time.Now())
	s.registerSubscription(pi, ptp.MessageSync, scS)
	// Check Sync is saved
	scT = s.findSubscription(pi, ptp.MessageSync)
	require.Equal(t, scS, scT)

	// Add announce. Check we have now both
	scA = NewSubscriptionClient(&sendWorker{}, sa, sa, ptp.MessageAnnounce, c, time.Second, time.Now())
	s.registerSubscription(pi, ptp.MessageAnnounce, scA)
	// First check Sync
	scT = s.findSubscription(pi, ptp.MessageSync)
	require.Equal(t, scS, scT)
	// Then check Announce
	scT = s.findSubscription(pi, ptp.MessageAnnounce)
	require.Equal(t, scA, scT)

	// Override announce
	scA = NewSubscriptionClient(&sendWorker{}, sa, sa, ptp.MessageAnnounce, c, time.Second, time.Now())
	s.registerSubscription(pi, ptp.MessageAnnounce, scA)
	// Check new Announce is saved
	scT = s.findSubscription(pi, ptp.MessageAnnounce)
	require.Equal(t, scA, scT)
}

func TestServerInventoryClients(t *testing.T) {
	clipi1 := ptp.PortIdentity{
		PortNumber:    1,
		ClockIdentity: ptp.ClockIdentity(1234),
	}
	clipi2 := ptp.PortIdentity{
		PortNumber:    1,
		ClockIdentity: ptp.ClockIdentity(5678),
	}
	c := &Config{clockIdentity: ptp.ClockIdentity(1234)}

	st := stats.NewJSONStats()
	go st.Start(0)
	time.Sleep(time.Millisecond)

	s := Server{Config: c, Stats: st}
	s.clients.init()

	sa := ipToSockaddr(net.ParseIP("127.0.0.1"), 123)
	scS1 := NewSubscriptionClient(&sendWorker{}, sa, sa, ptp.MessageSync, c, time.Second, time.Now().Add(time.Minute))
	s.registerSubscription(clipi1, ptp.MessageSync, scS1)
	scS1.running = true
	s.inventoryClients()
	require.Equal(t, 1, len(s.clients.keys()))

	scA1 := NewSubscriptionClient(&sendWorker{}, sa, sa, ptp.MessageAnnounce, c, time.Second, time.Now().Add(time.Minute))
	s.registerSubscription(clipi1, ptp.MessageSync, scA1)
	scA1.running = true
	s.inventoryClients()
	require.Equal(t, 1, len(s.clients.keys()))

	scS2 := NewSubscriptionClient(&sendWorker{}, sa, sa, ptp.MessageSync, c, time.Second, time.Now().Add(time.Minute))
	s.registerSubscription(clipi2, ptp.MessageSync, scS2)
	scS2.running = true
	s.inventoryClients()
	require.Equal(t, 2, len(s.clients.keys()))

	// Shutting down
	scS1.running = false
	s.inventoryClients()
	require.Equal(t, 2, len(s.clients.keys()))

	scA1.running = false
	s.inventoryClients()
	require.Equal(t, 1, len(s.clients.keys()))

	scS2.running = false
	s.inventoryClients()
	require.Equal(t, 0, len(s.clients.keys()))
}

func TestDelayRespPacket(t *testing.T) {
	c := &Config{clockIdentity: ptp.ClockIdentity(1234)}
	st := stats.NewJSONStats()
	s := Server{Config: c, Stats: st}
	sp := ptp.PortIdentity{
		PortNumber:    1,
		ClockIdentity: ptp.ClockIdentity(1234),
	}
	h := &ptp.Header{
		SequenceID:         42,
		CorrectionField:    ptp.NewCorrection(100500),
		SourcePortIdentity: sp,
	}

	now := time.Now()

	dResp := s.delayRespPacket(h, now)
	// Unicast flag
	require.Equal(t, uint16(42), dResp.Header.SequenceID)
	require.Equal(t, 100500, int(dResp.Header.CorrectionField.Nanoseconds()))
	require.Equal(t, sp, dResp.Header.SourcePortIdentity)
	require.Equal(t, now.Unix(), dResp.DelayRespBody.ReceiveTimestamp.Time().Unix())
	require.Equal(t, ptp.FlagUnicast, dResp.Header.FlagField)
}

func TestSockaddrToIP(t *testing.T) {
	ip4 := net.ParseIP("127.0.0.1")
	ip6 := net.ParseIP("::1")
	port := 123

	sa4 := ipToSockaddr(ip4, port)
	sa6 := ipToSockaddr(ip6, port)

	require.Equal(t, ip4.String(), sockaddrToIP(sa4).String())
	require.Equal(t, ip6.String(), sockaddrToIP(sa6).String())
}

func TestIpToSockaddr(t *testing.T) {
	ip4 := net.ParseIP("127.0.0.1")
	ip6 := net.ParseIP("::1")
	port := 123

	expectedSA4 := &unix.SockaddrInet4{Port: port}
	copy(expectedSA4.Addr[:], ip4.To4())

	expectedSA6 := &unix.SockaddrInet6{Port: port}
	copy(expectedSA6.Addr[:], ip6.To16())

	sa4 := ipToSockaddr(ip4, port)
	sa6 := ipToSockaddr(ip6, port)

	require.Equal(t, expectedSA4, sa4)
	require.Equal(t, expectedSA6, sa6)
}

func TestLoadOrStore(t *testing.T) {
	ip4 := net.ParseIP("127.0.0.1")
	ip6 := net.ParseIP("::1")
	port := 123
	expected4 := ipToSockaddr(ip4, port)
	expected6 := ipToSockaddr(ip6, port)
	s := syncMapSock{}
	s.init()

	require.Equal(t, 0, len(s.m))
	// Store 2 and load 1
	val4 := s.loadOrStore(ip4, 123)
	val6 := s.loadOrStore(ip6, 123)
	val6Load := s.loadOrStore(ip6, 123)

	require.Equal(t, 2, len(s.m))
	require.Equal(t, expected4, val4)
	require.Equal(t, expected6, val6)
	require.Equal(t, expected6, val6Load)
}
