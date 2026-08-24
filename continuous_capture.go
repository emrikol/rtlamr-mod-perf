package main

import (
	"fmt"
	"io"
	"time"
)

// blockCapture writes a bounded number of complete decoder input blocks. A
// complete-block boundary makes the resulting corpus directly replayable and
// avoids introducing a partial final block solely because a duration was not
// an exact multiple of the configured sample rate.
type blockCapture struct {
	writer          io.Writer
	blockBytes      int
	blocksRemaining int64
	blocksWritten   int64
}

func newBlockCapture(writer io.Writer, sampleRate, blockBytes int, duration time.Duration) (*blockCapture, error) {
	if writer == nil {
		return nil, fmt.Errorf("continuous capture writer is nil")
	}
	if sampleRate <= 0 || blockBytes <= 0 || duration <= 0 {
		return nil, fmt.Errorf("continuous capture requires positive sample rate, block size, and duration")
	}

	// rtl_tcp emits one unsigned byte for I and one for Q per complex sample.
	// Round upward to a complete input block. For rtlamr's supported chip
	// lengths, whole-second durations are already exact block multiples.
	targetBytes := (int64(sampleRate)*2*int64(duration) + int64(time.Second) - 1) / int64(time.Second)
	blocks := (targetBytes + int64(blockBytes) - 1) / int64(blockBytes)
	return &blockCapture{writer: writer, blockBytes: blockBytes, blocksRemaining: blocks}, nil
}

func (c *blockCapture) WriteBlock(block []byte) error {
	if c.blocksRemaining == 0 {
		return nil
	}
	if len(block) != c.blockBytes {
		return fmt.Errorf("continuous capture block has %d bytes, want %d", len(block), c.blockBytes)
	}
	n, err := c.writer.Write(block)
	if err != nil {
		return err
	}
	if n != len(block) {
		return io.ErrShortWrite
	}
	c.blocksRemaining--
	c.blocksWritten++
	if c.blocksRemaining == 0 {
		if syncer, ok := c.writer.(interface{ Sync() error }); ok {
			if err := syncer.Sync(); err != nil {
				return fmt.Errorf("sync continuous capture: %w", err)
			}
		}
	}
	return nil
}

func (c *blockCapture) Complete() bool {
	return c.blocksRemaining == 0
}

func (c *blockCapture) BytesWritten() int64 {
	return c.blocksWritten * int64(c.blockBytes)
}
