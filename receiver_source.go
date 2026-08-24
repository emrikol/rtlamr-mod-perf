package main

import (
	"fmt"
	"io"
	"sync"
)

// receiverSource returns complete decoder-block batches. The returned bytes
// remain valid until Release is called. This ownership contract lets both
// implementations reuse their input buffer without allocating per batch.
type receiverSource interface {
	Next() ([]byte, error)
	Release() error
	Cancel() error
	Close() error
	Name() string
}

type streamReceiverSource struct {
	reader      io.Reader
	closer      io.Closer
	buffer      []byte
	blockBytes  int
	filled      int
	batchBytes  int
	pendingErr  error
	batchActive bool
	closeOnce   sync.Once
	closeErr    error
}

func newStreamReceiverSource(reader io.Reader, closer io.Closer, blockBytes, blocks int) (*streamReceiverSource, error) {
	if reader == nil {
		return nil, fmt.Errorf("nil stream reader")
	}
	if blockBytes <= 0 || blocks <= 0 {
		return nil, fmt.Errorf("invalid stream geometry: block_bytes=%d blocks=%d", blockBytes, blocks)
	}
	return &streamReceiverSource{
		reader:     reader,
		closer:     closer,
		buffer:     make([]byte, blockBytes*blocks),
		blockBytes: blockBytes,
	}, nil
}

func (source *streamReceiverSource) Name() string { return "rtl_tcp" }

func (source *streamReceiverSource) Next() ([]byte, error) {
	if source.batchActive {
		return nil, fmt.Errorf("stream batch was not released")
	}
	if source.pendingErr != nil {
		err := source.pendingErr
		source.pendingErr = nil
		return nil, err
	}

	for {
		n, err := source.reader.Read(source.buffer[source.filled:])
		source.filled += n
		completeBytes := source.filled - source.filled%source.blockBytes
		if completeBytes > 0 {
			source.batchBytes = completeBytes
			source.batchActive = true
			source.pendingErr = err
			return source.buffer[:completeBytes], nil
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
}

func (source *streamReceiverSource) Release() error {
	if !source.batchActive {
		return fmt.Errorf("no active stream batch")
	}
	tailBytes := source.filled - source.batchBytes
	copy(source.buffer[:tailBytes], source.buffer[source.batchBytes:source.filled])
	source.filled = tailBytes
	source.batchBytes = 0
	source.batchActive = false
	return nil
}

func (source *streamReceiverSource) Cancel() error {
	source.closeOnce.Do(func() {
		if source.closer != nil {
			source.closeErr = source.closer.Close()
		}
	})
	return source.closeErr
}

func (source *streamReceiverSource) Close() error { return source.Cancel() }

type directRTLConfig struct {
	Device            string
	CenterFreq        uint32
	SampleRate        uint32
	BlockBytes        uint32
	BatchBytes        uint32
	TunerGainModeSet  bool
	TunerGainMode     bool
	TunerGainSet      bool
	TunerGainTenthsDB int
	GainByIndexSet    bool
	GainByIndex       uint32
	FreqCorrectionSet bool
	FreqCorrectionPPM int
	TestModeSet       bool
	TestMode          bool
	AGCModeSet        bool
	AGCMode           bool
	DirectSamplingSet bool
	DirectSampling    bool
	OffsetTuningSet   bool
	OffsetTuning      bool
	RTLXtalFreqSet    bool
	RTLXtalFreq       uint32
	TunerXtalFreqSet  bool
	TunerXtalFreq     uint32
}
