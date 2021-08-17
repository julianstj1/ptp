module main

go 1.16

replace github.com/facebookincubator/ptp/protocol => ../protocol

replace github.com/facebookincubator/ptp => ../

replace github.com/facebookincubator/ptp/clientgen/clientgenlib => ./clientgenlib

require (
	github.com/facebookincubator/ptp/clientgen/clientgenlib v0.0.0-00010101000000-000000000000
	github.com/facebookincubator/ptp/protocol v0.0.0-00010101000000-000000000000 // indirect
	github.com/google/gopacket v1.1.19 // indirect
	github.com/jamiealquiza/tachymeter v2.0.0+incompatible // indirect
	github.com/kpango/fastime v1.0.17 // indirect
	github.com/sirupsen/logrus v1.8.1
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c
)
