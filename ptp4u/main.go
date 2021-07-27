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

package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"time"

	ptp "github.com/facebookincubator/ptp/protocol"
	"github.com/facebookincubator/ptp/ptp4u/server"
	"github.com/facebookincubator/ptp/ptp4u/stats"
	log "github.com/sirupsen/logrus"
)

func main() {
	c := &server.Config{}

	var ipaddr string
	var pprofaddr string
	var ballastSize int

	flag.StringVar(&ipaddr, "ip", "::", "IP to bind on")
	flag.StringVar(&pprofaddr, "pprofaddr", "", "host:port for the pprof to bind")
	flag.StringVar(&c.Interface, "iface", "eth0", "Set the interface")
	flag.StringVar(&c.LogLevel, "loglevel", "info", "Set a log level. Can be: trace, debug, info, warning, error")
	flag.DurationVar(&c.MinSubInterval, "minsubinterval", 1*time.Second, "Minimum interval of the sync/announce subscription messages")
	flag.DurationVar(&c.MaxSubDuration, "maxsubduration", 10*time.Minute, "Maximum sync/announce subscription duration")
	flag.StringVar(&c.TimestampType, "timestamptype", ptp.HWTIMESTAMP, fmt.Sprintf("Timestamp type. Can be: %s, %s", ptp.HWTIMESTAMP, ptp.SWTIMESTAMP))
	flag.DurationVar(&c.UTCOffset, "utcoffset", 37*time.Second, "Set the number of workers. Ignored if shm is set")
	flag.BoolVar(&c.SHM, "shm", false, "Use Share Memory Segment to determine UTC offset periodically")
	flag.IntVar(&c.Workers, "workers", 10, "Set the number of workers")
	flag.IntVar(&c.MonitoringPort, "monitoringport", 8888, "Port to run monitoring server on")
	flag.IntVar(&c.QueueSize, "queue", 10000, "Size of the queue to send out packets")
	flag.DurationVar(&c.MetricInterval, "metricinterval", 1*time.Minute, "Interval of resetting metrics")
	flag.IntVar(&ballastSize, "ballastSize", 1024, "Size of heap ballast in Mb")

	flag.Parse()

	// see https://github.com/golang/go/issues/23044
	// also https://github.com/golang/go/issues/42430
	// and https://blog.twitch.tv/en/2019/04/10/go-memory-ballast-how-i-learnt-to-stop-worrying-and-love-the-heap-26c2462549a2/
	ballast := make([]byte, 1024*1024*ballastSize)

	switch c.LogLevel {
	case "trace":
		log.SetLevel(log.TraceLevel)
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warning":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	default:
		log.Fatalf("Unrecognized log level: %v", c.LogLevel)
	}

	switch c.TimestampType {
	case ptp.SWTIMESTAMP:
		log.Warning("Software timestamps greatly reduce the precision")
		fallthrough
	case ptp.HWTIMESTAMP:
		log.Debugf("Using %s timestamps", c.TimestampType)
	default:
		log.Fatalf("Unrecognized timestamp type: %s", c.TimestampType)
	}

	c.IP = net.ParseIP(ipaddr)
	found, err := c.IfaceHasIP()
	if err != nil {
		log.Fatal(err)
	}
	if !found {
		log.Fatalf("IP '%s' is not found on interface '%s'", c.IP, c.Interface)
	}

	if pprofaddr != "" {
		log.Warningf("Staring profiler on %s", pprofaddr)
		go func() {
			log.Println(http.ListenAndServe(pprofaddr, nil))
		}()
	}

	if c.SHM {
		if err := c.SetUTCOffsetFromSHM(); err != nil {
			log.Fatalf("Failed to set UTC offset: %v", err)
		}
	}
	log.Infof("UTC offset is: %v", c.UTCOffset)

	// Monitoring
	// Replace with your implementation of Stats
	st := stats.NewJSONStats()
	go st.Start(c.MonitoringPort)

	s := server.Server{
		Config: c,
		Stats:  st,
	}

	if err := s.Start(); err != nil {
		log.Fatalf("Server run failed: %v", err)
	}
	runtime.KeepAlive(ballast)
}
