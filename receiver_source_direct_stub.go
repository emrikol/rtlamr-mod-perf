//go:build !linux || !cgo || !rtlsdr

package main

import "fmt"

func directRTLSourceAvailable() bool { return false }

func newDirectRTLSource(config directRTLConfig) (receiverSource, uint32, uint32, error) {
	return nil, 0, 0, fmt.Errorf("direct RTL-SDR support requires linux, cgo, and the rtlsdr build tag")
}
