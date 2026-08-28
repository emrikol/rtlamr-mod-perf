//go:build linux && cgo && rtlsdr

package main

import (
	"os"
	"syscall"
	"testing"
)

func TestDirectRTLCancelStopsKernelRing(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	// A pipe rejects the ring STOP request with ENOTTY. Seeing that exact error
	// proves the production cancellation path attempted the ioctl; returning nil
	// means a kernel-backed Next call would remain blocked forever.
	want := -int(syscall.ENOTTY)
	if got := directRTLCancelFDForTest(reader.Fd(), true); got != want {
		t.Fatalf("kernel-ring cancel result = %d, want %d from STOP ioctl", got, want)
	}
}

func TestDirectRTLCancelDoesNotStopSynchronousReader(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	if got := directRTLCancelFDForTest(reader.Fd(), false); got != 0 {
		t.Fatalf("synchronous-reader cancel result = %d, want 0", got)
	}
}
