package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bemasher/rtlamr/protocol"
	"github.com/bemasher/rtltcp"
)

var receiverBenchmarkMessages int

type discardTestEncoder struct{}

func (discardTestEncoder) Encode(interface{}) error { return nil }

type signalingBuffer struct {
	bytes.Buffer
	wrote chan struct{}
	once  sync.Once
}

func (b *signalingBuffer) Write(p []byte) (int, error) {
	n, err := b.Buffer.Write(p)
	b.once.Do(func() { close(b.wrote) })
	return n, err
}

func TestReceiverCloseInterruptsPartialBlockRead(t *testing.T) {
	receiverConn, sourceConn := receiverTCPPair(t)
	defer sourceConn.Close()

	r := receiverForReadTest(receiverConn, 16)
	r.Run()

	// Leave ReadFull waiting for the rest of the block. Close must interrupt
	// that read and treat it as an intentional shutdown, not a receiver error.
	if _, err := sourceConn.Write([]byte{0x01}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		r.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		_ = sourceConn.Close()
		<-done
		t.Fatal("Receiver.Close did not interrupt a partial block read")
	}

	if r.err != nil {
		t.Fatalf("intentional close returned receiver error: %v", r.err)
	}

	// Main has both an explicit and a deferred Close. Keep that lifecycle
	// idempotent.
	r.Close()
}

func TestReceiverReportsUnexpectedReadFailure(t *testing.T) {
	receiverConn, sourceConn := receiverTCPPair(t)
	r := receiverForReadTest(receiverConn, 16)
	r.Run()

	if err := sourceConn.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-r.ctx.Done():
	case <-time.After(time.Second):
		r.Close()
		t.Fatal("receiver did not stop after its source connection closed")
	}
	r.Close()

	if r.err == nil || !strings.Contains(r.err.Error(), "sample source") {
		t.Fatalf("unexpected read failure = %v, want wrapped sample source error", r.err)
	}
}

func TestReceiverPreservesBlocksAcrossIrregularReads(t *testing.T) {
	receiverConn, sourceConn := receiverTCPPair(t)
	decoder := receiverDecoder(t)
	blockBytes := decoder.Cfg.BlockSize2

	var captured bytes.Buffer
	r := receiverForReadTest(receiverConn, blockBytes)
	r.d = decoder
	r.continuousCapture = &blockCapture{
		writer:          &captured,
		blockBytes:      blockBytes,
		blocksRemaining: 3,
	}

	previousEncoder := encoder
	encoder = discardTestEncoder{}
	defer func() { encoder = previousEncoder }()

	r.Run()
	payload := make([]byte, blockBytes*3+blockBytes/2)
	for idx := range payload {
		payload[idx] = byte(idx*73 + idx/29)
	}
	writeErr := make(chan error, 1)
	go func() {
		chunks := []int{1, 31, 4093, 7, 16381, 257}
		for offset, chunkIndex := 0, 0; offset < len(payload); chunkIndex++ {
			chunk := chunks[chunkIndex%len(chunks)]
			if chunk > len(payload)-offset {
				chunk = len(payload) - offset
			}
			if err := writeTCPBytes(sourceConn, payload[offset:offset+chunk]); err != nil {
				writeErr <- err
				return
			}
			offset += chunk
		}
		writeErr <- sourceConn.Close()
	}()

	select {
	case <-r.ctx.Done():
	case <-time.After(2 * time.Second):
		_ = sourceConn.Close()
		r.Close()
		t.Fatal("receiver did not finish irregular input")
	}
	r.Close()
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}

	want := payload[:blockBytes*3]
	if !bytes.Equal(captured.Bytes(), want) {
		t.Fatalf("captured %d bytes, want exact %d-byte complete-block prefix", captured.Len(), len(want))
	}
}

func TestReceiverDoesNotWaitForReadBufferToFill(t *testing.T) {
	receiverConn, sourceConn := receiverTCPPair(t)
	defer sourceConn.Close()
	decoder := receiverDecoder(t)
	blockBytes := decoder.Cfg.BlockSize2

	captured := &signalingBuffer{wrote: make(chan struct{})}
	r := receiverForReadTest(receiverConn, blockBytes)
	r.d = decoder
	r.continuousCapture = &blockCapture{
		writer:          captured,
		blockBytes:      blockBytes,
		blocksRemaining: 1,
	}

	previousEncoder := encoder
	encoder = discardTestEncoder{}
	defer func() { encoder = previousEncoder }()

	r.Run()
	block := make([]byte, blockBytes)
	if err := writeTCPBytes(sourceConn, block); err != nil {
		t.Fatal(err)
	}

	select {
	case <-captured.wrote:
	case <-time.After(time.Second):
		r.Close()
		t.Fatal("receiver waited for its larger read buffer instead of processing one complete block")
	}
	r.Close()

	if !bytes.Equal(captured.Bytes(), block) {
		t.Fatalf("captured block differs: got %d bytes, want %d", captured.Len(), len(block))
	}
}

func TestReceiverSampleRateUsesElapsedInterval(t *testing.T) {
	const sampleRate = 2359296
	bytesRead := sampleRate * 2 * 10
	if got := receiverSampleRate(bytesRead, 10*time.Second); got != sampleRate {
		t.Fatalf("exact sample rate = %d, want %d", got, sampleRate)
	}
	if got := receiverSampleRate(bytesRead, 0); got != 0 {
		t.Fatalf("zero-duration sample rate = %d, want 0", got)
	}
	if got := receiverSampleRate(0, 10*time.Second); got != 0 {
		t.Fatalf("zero-byte sample rate = %d, want 0", got)
	}
}

func receiverForReadTest(conn *net.TCPConn, blockBytes int) *Receiver {
	ctx, cancel := context.WithCancel(context.Background())
	source, err := newStreamReceiverSource(conn, conn, blockBytes, receiverReadBlocks)
	if err != nil {
		panic(err)
	}
	return &Receiver{
		SDR:    rtltcp.SDR{TCPConn: conn},
		source: source,
		d:      protocol.Decoder{Cfg: protocol.PacketConfig{BlockSize2: blockBytes}},
		ctx:    ctx,
		cancel: cancel,
		wg:     &sync.WaitGroup{},
	}
}

func receiverTCPPair(t testing.TB) (*net.TCPConn, *net.TCPConn) {
	t.Helper()

	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type acceptResult struct {
		conn *net.TCPConn
		err  error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, acceptErr := listener.AcceptTCP()
		accepted <- acceptResult{conn: conn, err: acceptErr}
	}()

	client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	result := <-accepted
	if result.err != nil {
		_ = client.Close()
		t.Fatal(result.err)
	}

	return client, result.conn
}

func BenchmarkReceiverPipeline(b *testing.B) {
	b.Run("split-with-deadline", func(b *testing.B) {
		receiverConn, sourceConn := receiverTCPPair(b)
		defer receiverConn.Close()
		defer sourceConn.Close()

		decoder := receiverDecoder(b)
		blockBytes := decoder.Cfg.BlockSize2
		blocks := make(chan []byte)
		writeErr := make(chan error, 1)
		readErr := make(chan error, 1)
		go receiverBenchmarkWrite(sourceConn, blockBytes, b.N, writeErr)

		b.SetBytes(int64(blockBytes))
		b.ReportAllocs()
		b.ResetTimer()

		go func() {
			defer close(blocks)
			buffers := [2][]byte{make([]byte, blockBytes), make([]byte, blockBytes)}
			for i := 0; i < b.N; i++ {
				block := buffers[i&1]
				if err := receiverConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					readErr <- err
					return
				}
				if _, err := io.ReadFull(receiverConn, block); err != nil {
					readErr <- err
					return
				}
				blocks <- block
			}
			readErr <- nil
		}()

		messages := 0
		for block := range blocks {
			messages += len(decoder.Decode(block))
		}
		receiverBenchmarkMessages = messages

		b.StopTimer()
		if err := <-readErr; err != nil {
			b.Fatal(err)
		}
		if err := <-writeErr; err != nil {
			b.Fatal(err)
		}
	})

	b.Run("single-read-decode", func(b *testing.B) {
		receiverConn, sourceConn := receiverTCPPair(b)
		defer receiverConn.Close()
		defer sourceConn.Close()

		decoder := receiverDecoder(b)
		blockBytes := decoder.Cfg.BlockSize2
		block := make([]byte, blockBytes)
		writeErr := make(chan error, 1)
		go receiverBenchmarkWrite(sourceConn, blockBytes, b.N, writeErr)
		reader := &countingReader{reader: receiverConn}

		b.SetBytes(int64(blockBytes))
		b.ReportAllocs()
		b.ResetTimer()

		messages := 0
		for i := 0; i < b.N; i++ {
			if _, err := io.ReadFull(reader, block); err != nil {
				b.Fatal(err)
			}
			messages += len(decoder.Decode(block))
		}
		receiverBenchmarkMessages = messages
		b.ReportMetric(float64(reader.reads)/float64(b.N), "reads/block")

		b.StopTimer()
		if err := <-writeErr; err != nil {
			b.Fatal(err)
		}
	})

	b.Run("opportunistic-read-decode", func(b *testing.B) {
		receiverConn, sourceConn := receiverTCPPair(b)
		defer receiverConn.Close()
		defer sourceConn.Close()

		decoder := receiverDecoder(b)
		blockBytes := decoder.Cfg.BlockSize2
		input := make([]byte, blockBytes*receiverReadBlocks)
		writeErr := make(chan error, 1)
		go receiverBenchmarkWrite(sourceConn, blockBytes, b.N, writeErr)

		b.SetBytes(int64(blockBytes))
		b.ReportAllocs()
		b.ResetTimer()

		messages := 0
		filled := 0
		processed := 0
		reads := 0
		for processed < b.N {
			n, err := receiverConn.Read(input[filled:])
			reads++
			filled += n
			completeBlocks := filled / blockBytes
			if remaining := b.N - processed; completeBlocks > remaining {
				completeBlocks = remaining
			}
			completeBytes := completeBlocks * blockBytes
			for offset := 0; offset < completeBytes; offset += blockBytes {
				messages += len(decoder.Decode(input[offset : offset+blockBytes]))
			}
			processed += completeBlocks
			filled -= completeBytes
			copy(input[:filled], input[completeBytes:completeBytes+filled])
			if err != nil {
				b.Fatal(err)
			}
		}
		receiverBenchmarkMessages = messages
		b.ReportMetric(float64(reads)/float64(b.N), "reads/block")

		b.StopTimer()
		if err := <-writeErr; err != nil {
			b.Fatal(err)
		}
	})
}

func receiverDecoder(t testing.TB) protocol.Decoder {
	t.Helper()
	decoder := protocol.NewDecoder()
	parser, err := protocol.NewParser("r900", 72)
	if err != nil {
		t.Fatal(err)
	}
	decoder.RegisterProtocol(parser)
	decoder.Allocate()
	return decoder
}

type countingReader struct {
	reader io.Reader
	reads  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

func receiverBenchmarkWrite(conn *net.TCPConn, blockBytes, blocks int, result chan<- error) {
	block := make([]byte, blockBytes)
	for i := 0; i < blocks; i++ {
		if err := writeTCPBytes(conn, block); err != nil {
			result <- err
			return
		}
	}
	result <- nil
}

func writeTCPBytes(conn *net.TCPConn, data []byte) error {
	for offset := 0; offset < len(data); {
		n, err := conn.Write(data[offset:])
		if err != nil {
			return err
		}
		offset += n
	}
	return nil
}
