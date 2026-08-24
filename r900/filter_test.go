package r900

import (
	"bytes"
	"math"
	"math/rand"
	"testing"

	"github.com/bemasher/rtlamr/protocol"
)

func combinedDecoderConfig() protocol.PacketConfig {
	return protocol.PacketConfig{
		BlockSize:    8192,
		ChipLength:   72,
		BufferLength: 114176,
	}
}

// legacyFilter is the v0.9.5 implementation retained as an oracle for the
// incremental implementation. Its buffers are reused as they are in the
// production parser, keeping the benchmark comparison fair.
type legacyFilter struct {
	csum      []float64
	filtered  [][3]float64
	quantized []byte
}

func newLegacyFilter(cfg protocol.PacketConfig) *legacyFilter {
	return &legacyFilter{
		csum:      make([]float64, cfg.BufferLength+1),
		filtered:  make([][3]float64, cfg.BufferLength),
		quantized: make([]byte, cfg.BufferLength),
	}
}

func (f *legacyFilter) filterAndQuantize(signal []float64, cfg protocol.PacketConfig) []byte {
	var sum float64
	for idx, v := range signal {
		sum += v
		f.csum[idx+1] = sum
	}

	for idx := 0; idx < cfg.BufferLength-cfg.ChipLength*4; idx++ {
		c0 := f.csum[idx]
		c1 := f.csum[idx+cfg.ChipLength] * 2
		c2 := f.csum[idx+cfg.ChipLength*2] * 2
		c3 := f.csum[idx+cfg.ChipLength*3] * 2
		c4 := f.csum[idx+cfg.ChipLength*4]

		f.filtered[idx][0] = c2 - c4 - c0
		f.filtered[idx][1] = c1 - c2 + c3 - c4 - c0
		f.filtered[idx][2] = c1 - c3 + c4 - c0
	}

	for idx, vec := range f.filtered {
		argmax := byte(0)
		max := math.Abs(vec[0])
		if v1 := math.Abs(vec[1]); v1 > max {
			max = v1
			argmax = 1
		}
		if v2 := math.Abs(vec[2]); v2 > max {
			argmax = 2
		}

		f.quantized[idx] = argmax
		if vec[argmax] > 0 {
			f.quantized[idx] += 3
		}
	}

	return f.quantized
}

func newBenchmarkParser(cfg protocol.PacketConfig) *Parser {
	decoder := &protocol.Decoder{Cfg: cfg}
	filterSignalLength := cfg.BlockSize + cfg.ChipLength*4
	return &Parser{
		Decoder:      decoder,
		cfg:          cfg,
		filterSignal: make([]float64, filterSignalLength),
		csum:         make([]float64, filterSignalLength+1),
		quantized:    make([]byte, cfg.BufferLength),
	}
}

func shiftAndAppend(signal, block []float64) {
	copy(signal, signal[len(block):])
	copy(signal[len(signal)-len(block):], block)
}

func logicalQuantized(parser *Parser) []byte {
	result := make([]byte, len(parser.quantized))
	for idx := range result {
		result[idx] = parser.quantizedAt(idx)
	}
	return result
}

func TestIncrementalFilterMatchesLegacy(t *testing.T) {
	cfg := combinedDecoderConfig()
	rng := rand.New(rand.NewSource(0x900))
	parser := newBenchmarkParser(cfg)
	legacy := newLegacyFilter(cfg)
	referenceSignal := make([]float64, cfg.BufferLength)
	block := make([]float64, cfg.BlockSize)

	// Multiple sequential blocks exercise both the initial full calculation
	// and reuse after every signal-window shift.
	for iteration := 0; iteration < 64; iteration++ {
		for idx := range block {
			block[idx] = rng.Float64() * 2
		}

		shiftAndAppend(referenceSignal, block)
		parser.appendSignal(block)
		parser.filterAndQuantize()

		want := legacy.filterAndQuantize(referenceSignal, cfg)
		got := logicalQuantized(parser)
		if !bytes.Equal(got, want) {
			for idx := range want {
				if got[idx] != want[idx] {
					t.Fatalf("iteration %d: decision %d = %d, want %d", iteration, idx, got[idx], want[idx])
				}
			}
		}
	}
}

func TestOnDemandFilterMatchesLegacy(t *testing.T) {
	cfg := combinedDecoderConfig()
	cfg.SymbolLength = cfg.ChipLength * 2
	cfg.PreambleLength = 32 * cfg.SymbolLength
	cfg.PacketLength = cfg.BufferLength - cfg.BlockSize
	rng := rand.New(rand.NewSource(0x901))
	parser := NewParser(cfg.ChipLength).(*Parser)
	decoder := protocol.NewDecoder()
	decoder.RegisterProtocol(parser)
	decoder.Cfg.PacketSymbols = cfg.PacketLength / cfg.SymbolLength
	decoder.Allocate()
	cfg = decoder.Cfg
	legacy := newLegacyFilter(cfg)
	referenceSignal := make([]float64, cfg.BufferLength)
	block := make([]float64, cfg.BlockSize)
	input := make([]byte, cfg.BlockSize*2)
	lut := protocol.NewMagLUT()

	for iteration := 0; iteration < 64; iteration++ {
		for idx := range input {
			input[idx] = byte(rng.Intn(256))
		}
		lut.Execute(input, block)
		shiftAndAppend(referenceSignal, block)
		for range decoder.Decode(input) {
		}
		want := legacy.filterAndQuantize(referenceSignal, cfg)

		for _, qIdx := range []int{0, 1, 7, 8, cfg.BlockSize - 1} {
			payloadIdx := qIdx + cfg.PreambleLength - cfg.SymbolLength
			for offset := 0; offset < PayloadSymbols*4*cfg.ChipLength; offset += cfg.ChipLength * 4 {
				idx := payloadIdx + offset
				if got := parser.quantizeSignalAt(idx); got != want[idx] {
					t.Fatalf("iteration %d: decision %d = %d, want %d", iteration, idx, got, want[idx])
				}
			}
		}
	}
}

func BenchmarkCombinedR900IDMLegacyFilter(b *testing.B) {
	cfg := combinedDecoderConfig()
	rng := rand.New(rand.NewSource(0x900))
	signal := make([]float64, cfg.BufferLength)
	legacy := newLegacyFilter(cfg)
	for idx := range signal {
		signal[idx] = rng.Float64() * 2
	}

	b.ReportAllocs()
	b.SetBytes(int64(cfg.BlockSize * 2))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		legacy.filterAndQuantize(signal, cfg)
	}
}

func BenchmarkCombinedR900IDMIncrementalFilter(b *testing.B) {
	cfg := combinedDecoderConfig()
	rng := rand.New(rand.NewSource(0x900))
	parser := newBenchmarkParser(cfg)
	block := make([]float64, cfg.BlockSize)
	for idx := range block {
		block[idx] = rng.Float64() * 2
	}
	parser.appendSignal(block)
	parser.filterAndQuantize()

	b.ReportAllocs()
	b.SetBytes(int64(cfg.BlockSize * 2))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		parser.appendSignal(block)
		parser.filterAndQuantize()
	}
}
