//go:build linux && cgo && rtlsdr

package main

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestDirectRTLRingReleaseQueuePreservesCollarOrder(t *testing.T) {
	source := &directRTLSource{
		kernelRing:   true,
		kernelRetain: 2,
		kernelHeld:   make([]directRTLRingRelease, 4),
	}
	release := func(sequence uint64) {
		t.Helper()
		source.kernelCurrent = directRTLRingCompletion{Sequence: sequence, Slot: uint32(sequence % 4), Length: 256 << 10}
		source.kernelCurrentValid = true
		if err := source.releaseKernelBatch(); err != nil {
			t.Fatalf("release %d: %v", sequence, err)
		}
	}

	release(0)
	release(1)
	if source.kernelPendingValid || source.kernelHeldCount != 2 {
		t.Fatalf("initial collar state pending=%t held=%d", source.kernelPendingValid, source.kernelHeldCount)
	}
	release(2)
	if !source.kernelPendingValid || source.kernelPending.Sequence != 0 || source.kernelHeldCount != 2 {
		t.Fatalf("first returned descriptor=%+v pending=%t held=%d", source.kernelPending, source.kernelPendingValid, source.kernelHeldCount)
	}
	source.kernelPendingValid = false // successful exchange consumed sequence 0
	release(3)
	if !source.kernelPendingValid || source.kernelPending.Sequence != 1 {
		t.Fatalf("second returned descriptor=%+v pending=%t", source.kernelPending, source.kernelPendingValid)
	}
}

func TestDirectRTLRingZeroCollarDefersCurrentReleaseToExchange(t *testing.T) {
	source := &directRTLSource{kernelRing: true, kernelHeld: make([]directRTLRingRelease, 4)}
	source.kernelCurrent = directRTLRingCompletion{Sequence: 7, Slot: 3, Length: 256 << 10}
	source.kernelCurrentValid = true
	if err := source.releaseKernelBatch(); err != nil {
		t.Fatal(err)
	}
	if !source.kernelPendingValid || source.kernelPending.Sequence != 7 || source.kernelCurrentValid {
		t.Fatalf("release state current=%t pending=%t descriptor=%+v", source.kernelCurrentValid, source.kernelPendingValid, source.kernelPending)
	}
}

func TestDirectRTLKernelIOCTLUsesDirectSyscall(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	argument := directRTLRingExchange{}
	err = directRTLKernelIOCTL(int(reader.Fd()), 0, unsafe.Pointer(&argument))
	if err != syscall.ENOTTY {
		t.Fatalf("direct ioctl error=%v want=%v", err, syscall.ENOTTY)
	}
}

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
