package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestBlockCaptureExactWholeSecond(t *testing.T) {
	var dst bytes.Buffer
	capture, err := newBlockCapture(&dst, 2359296, 16384, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if capture.blocksRemaining != 288 {
		t.Fatalf("blocksRemaining = %d, want 288", capture.blocksRemaining)
	}

	block := bytes.Repeat([]byte{0xa5}, 16384)
	for i := 0; i < 300; i++ {
		if err := capture.WriteBlock(block); err != nil {
			t.Fatal(err)
		}
	}
	if !capture.Complete() {
		t.Fatal("capture did not complete")
	}
	if got, want := int64(dst.Len()), int64(2359296*2); got != want {
		t.Fatalf("captured bytes = %d, want %d", got, want)
	}
	if got, want := capture.BytesWritten(), int64(dst.Len()); got != want {
		t.Fatalf("BytesWritten = %d, want %d", got, want)
	}
}

func TestBlockCaptureRoundsToCompleteBlock(t *testing.T) {
	var dst syncingBuffer
	capture, err := newBlockCapture(&dst, 10, 16, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if capture.blocksRemaining != 1 {
		t.Fatalf("blocksRemaining = %d, want 1", capture.blocksRemaining)
	}
	if err := capture.WriteBlock(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if dst.Len() != 16 {
		t.Fatalf("captured bytes = %d, want 16", dst.Len())
	}
	if !dst.synced {
		t.Fatal("completed capture was not synced")
	}
}

func TestBlockCaptureRejectsInvalidInputAndShortWrites(t *testing.T) {
	if _, err := newBlockCapture(nil, 1, 1, time.Second); err == nil {
		t.Fatal("nil writer accepted")
	}
	if _, err := newBlockCapture(io.Discard, 0, 1, time.Second); err == nil {
		t.Fatal("zero sample rate accepted")
	}

	capture, err := newBlockCapture(shortWriter{}, 1, 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.WriteBlock([]byte{1}); err == nil {
		t.Fatal("wrong block size accepted")
	}
	if err := capture.WriteBlock([]byte{1, 2}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want io.ErrShortWrite", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

type syncingBuffer struct {
	bytes.Buffer
	synced bool
}

func (b *syncingBuffer) Sync() error {
	b.synced = true
	return nil
}
