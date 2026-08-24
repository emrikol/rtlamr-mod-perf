package csv

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/xerrors"
)

func TestRecorderNil(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := Encoder{csv.NewWriter(buf)}

	if err := enc.Encode(nil); err == nil {
		t.Fatalf("%+v\n", err)
	}
}

type Msg struct{}

func (m Msg) Record() []string {
	return []string{}
}

func TestRecorder(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := Encoder{csv.NewWriter(buf)}

	if err := enc.Encode(Msg{}); err != nil {
		t.Fatalf("%+v\n", err)
	}
}

type NonRecorder struct{}

func TestNonRecorder(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := Encoder{csv.NewWriter(buf)}

	err := enc.Encode(NonRecorder{})

	var runtimeErr runtime.Error
	if !xerrors.As(err, &runtimeErr) {
		t.Fatalf("%+v\n", runtimeErr)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestEncodeReturnsFlushError(t *testing.T) {
	want := errors.New("write failed")
	enc := NewEncoder(failingWriter{err: want})

	if err := enc.Encode(Msg{}); !errors.Is(err, want) {
		t.Fatalf("Encode error = %v, want %v", err, want)
	}
}

type panicRecorder struct{}

func (panicRecorder) Record() []string {
	panic("record failed")
}

func TestEncodeRecoversNonErrorPanic(t *testing.T) {
	enc := NewEncoder(io.Discard)
	err := enc.Encode(panicRecorder{})
	if err == nil || !strings.Contains(err.Error(), "record failed") {
		t.Fatalf("Encode error = %v, want recovered panic", err)
	}
}
