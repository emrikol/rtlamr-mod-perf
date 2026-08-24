package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestStreamReceiverSourcePreservesIncompleteTail(t *testing.T) {
	input := make([]byte, 16)
	for index := range input {
		input[index] = byte(index)
	}
	reader := &receiverChunkReader{data: input, chunks: []int{3, 6, 7}, terminal: io.EOF}
	source, err := newStreamReceiverSource(reader, nil, 4, 3)
	if err != nil {
		t.Fatal(err)
	}

	first, err := source.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, input[:8]) {
		t.Fatalf("first batch = %v, want %v", first, input[:8])
	}
	if _, err := source.Next(); err == nil {
		t.Fatal("Next without Release succeeded")
	}
	if err := source.Release(); err != nil {
		t.Fatal(err)
	}

	second, err := source.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, input[8:]) {
		t.Fatalf("second batch = %v, want %v", second, input[8:])
	}
	if err := source.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal error = %v, want EOF", err)
	}
}

func TestStreamReceiverSourceDefersErrorUntilBatchReleased(t *testing.T) {
	terminal := io.ErrUnexpectedEOF
	reader := &receiverChunkReader{
		data:             []byte{1, 2, 3, 4},
		chunks:           []int{4},
		terminal:         terminal,
		terminalWithData: true,
	}
	source, err := newStreamReceiverSource(reader, nil, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(batch, reader.data) {
		t.Fatalf("batch = %v, want %v", batch, reader.data)
	}
	if err := source.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Next(); !errors.Is(err, terminal) {
		t.Fatalf("deferred error = %v, want %v", err, terminal)
	}
}

func TestStreamReceiverSourceClosesOnce(t *testing.T) {
	closer := new(receiverCountingCloser)
	source, err := newStreamReceiverSource(bytes.NewReader(nil), closer, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if closer.calls != 1 {
		t.Fatalf("close calls = %d, want 1", closer.calls)
	}
}

func TestStreamReceiverSourceRejectsInvalidGeometry(t *testing.T) {
	for _, test := range []struct {
		name       string
		reader     io.Reader
		blockBytes int
		blocks     int
	}{
		{name: "nil reader", blockBytes: 4, blocks: 1},
		{name: "zero block bytes", reader: bytes.NewReader(nil), blocks: 1},
		{name: "zero blocks", reader: bytes.NewReader(nil), blockBytes: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newStreamReceiverSource(test.reader, nil, test.blockBytes, test.blocks); err == nil {
				t.Fatal("invalid geometry succeeded")
			}
		})
	}
}

type receiverChunkReader struct {
	data             []byte
	chunks           []int
	offset           int
	chunk            int
	terminal         error
	terminalWithData bool
}

func (reader *receiverChunkReader) Read(output []byte) (int, error) {
	if reader.offset == len(reader.data) {
		return 0, reader.terminal
	}
	limit := len(output)
	if reader.chunk < len(reader.chunks) && reader.chunks[reader.chunk] < limit {
		limit = reader.chunks[reader.chunk]
	}
	reader.chunk++
	if remaining := len(reader.data) - reader.offset; remaining < limit {
		limit = remaining
	}
	n := copy(output, reader.data[reader.offset:reader.offset+limit])
	reader.offset += n
	if reader.terminalWithData && reader.offset == len(reader.data) {
		return n, reader.terminal
	}
	return n, nil
}

type receiverCountingCloser struct {
	calls int
}

func (closer *receiverCountingCloser) Close() error {
	closer.calls++
	return nil
}
