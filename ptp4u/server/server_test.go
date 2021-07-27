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
	"testing"
	"time"

	ptp "github.com/facebookincubator/ptp/protocol"
	"github.com/facebookincubator/ptp/ptp4u/stats"
	"github.com/stretchr/testify/require"
)

func TestDelayRespPacket(t *testing.T) {
	c := &Config{clockIdentity: ptp.ClockIdentity(1234)}
	st := stats.NewJSONStats()
	s := NewDelayWorker(0, nil, c, st, nil, nil)
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
