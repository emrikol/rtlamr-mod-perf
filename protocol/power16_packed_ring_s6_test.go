package protocol

import (
	"bytes"
	"math/rand"
	"testing"
)

func s6PackOracle(dst, src []byte) {
	for byteIdx := range dst {
		var value byte
		for bit := 0; bit < 8; bit++ {
			value = value<<1 | src[byteIdx*8+bit]
		}
		dst[byteIdx] = value
	}
}

func s6InitializePackedState(cfg PacketConfig, logical []byte, start int) *power16State {
	length := cfg.BufferLength >> 3
	lookahead := (cfg.BlockSize + cfg.PreambleLength) >> 3
	state := &power16State{
		packedLength:    length,
		packedStart:     start,
		packedLookahead: lookahead,
		packedBacking:   make([]byte, length+lookahead),
	}
	packed := make([]byte, length)
	s6PackOracle(packed, logical)
	for logicalIdx, value := range packed {
		physical := start + logicalIdx
		if physical >= length {
			physical -= length
		}
		state.packedBacking[physical] = value
	}
	copy(state.packedBacking[length:], state.packedBacking[:lookahead])
	return state
}

func TestS6PackedHistoryMatchesIndependentShiftOracle(t *testing.T) {
	tests := []struct {
		name       string
		cfg        PacketConfig
		starts     []int
		iterations int
	}{
		{
			name: "production-all-reachable-starts",
			cfg: PacketConfig{
				BlockSize:      8192,
				PreambleLength: 4608,
				PacketLength:   105984,
				BufferLength:   114176,
			},
			starts:     s6Multiples(64, 114176>>3),
			iterations: 3,
		},
		{
			name: "small-every-byte-start",
			cfg: PacketConfig{
				BlockSize:      24,
				PreambleLength: 16,
				PacketLength:   112,
				BufferLength:   136,
			},
			starts:     s6Multiples(1, 136>>3),
			iterations: 41,
		},
		{
			name: "one-byte-block",
			cfg: PacketConfig{
				BlockSize:      8,
				PreambleLength: 24,
				PacketLength:   72,
				BufferLength:   80,
			},
			starts:     s6Multiples(1, 80>>3),
			iterations: 31,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, start := range test.starts {
				rng := rand.New(rand.NewSource(0x56a60000 + int64(start)*131 + int64(test.cfg.BufferLength)))
				logical := make([]byte, test.cfg.BufferLength)
				for idx := range logical {
					logical[idx] = byte(rng.Intn(2))
				}
				state := s6InitializePackedState(test.cfg, logical, start)
				block := make([]byte, test.cfg.BlockSize)
				wantWindow := make([]byte, state.packedLookahead)
				wantRing := make([]byte, state.packedLength)

				for iteration := 0; iteration < test.iterations; iteration++ {
					for idx := range block {
						block[idx] = byte(rng.Intn(2))
					}
					copy(logical, logical[test.cfg.BlockSize:])
					copy(logical[test.cfg.PacketLength:], block)

					output, nextStart, tail := state.nextPackedOutput(test.cfg)
					s6PackOracle(output, block)
					state.commitPackedOutput(nextStart, tail, len(output))

					s6PackOracle(wantWindow, logical[:test.cfg.BlockSize+test.cfg.PreambleLength])
					if got := state.packedSearchWindow(); !bytes.Equal(got, wantWindow) {
						t.Fatalf("start=%d iteration=%d: search window mismatch", start, iteration)
					}
					s6PackOracle(wantRing, logical)
					for logicalIdx, want := range wantRing {
						physical := state.packedStart + logicalIdx
						if physical >= state.packedLength {
							physical -= state.packedLength
						}
						if got := state.packedBacking[physical]; got != want {
							t.Fatalf("start=%d iteration=%d logical_byte=%d: got=%#02x want=%#02x", start, iteration, logicalIdx, got, want)
						}
					}
					if !bytes.Equal(state.packedBacking[state.packedLength:], state.packedBacking[:state.packedLookahead]) {
						t.Fatalf("start=%d iteration=%d: lookahead mirror mismatch", start, iteration)
					}
				}
			}
		})
	}
}

func s6Multiples(step, limit int) []int {
	values := make([]int, 0, (limit+step-1)/step)
	for value := 0; value < limit; value += step {
		values = append(values, value)
	}
	return values
}

func TestS6OrdinaryDecoderPackedPathMatchesExistingTail(t *testing.T) {
	baselinePlatform := power16DecoderTestPlatform()
	candidatePlatform := power16DecoderTestPlatform()
	candidatePlatform.implementation = "s6-packed-reference-test"
	candidatePlatform.runPacked = func(decisions, packed []byte, window []uint16, input []byte) bool {
		if !power16ReferenceBlock(decisions, window, input) {
			return false
		}
		s6PackOracle(packed, decisions)
		return true
	}
	baseline := newPower16AutomaticTestDecoder(baselinePlatform)
	candidate := newPower16AutomaticTestDecoder(candidatePlatform)
	baseline.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	candidate.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	baseline.Allocate()
	candidate.Allocate()
	if !candidate.ownsQuantizedRing() {
		t.Fatal("packed decoder did not allocate a direct-write quantized ring")
	}

	rng := rand.New(rand.NewSource(0x56a6dec0de))
	input := make([]byte, power16BlockSize*2)
	for block := 0; block < 223*2; block++ {
		if _, err := rng.Read(input); err != nil {
			t.Fatal(err)
		}
		baseMessages := baseline.Decode(input)
		candidateMessages := candidate.Decode(input)
		if len(candidate.filterScratch) == 0 || &candidate.filterOutput[0] == &candidate.filterScratch[0] {
			t.Fatalf("block=%d: packed decoder copied through decision scratch", block)
		}
		if len(baseMessages) != len(candidateMessages) {
			t.Fatalf("block=%d: messages=%d, want %d", block, len(candidateMessages), len(baseMessages))
		}
		if !bytes.Equal(baseline.filterOutput, candidate.filterOutput) || !bytes.Equal(baseline.packed, candidate.packed) {
			t.Fatalf("block=%d: decision or search input differs", block)
		}
		for idx := range baseline.Quantized {
			if got, want := candidate.quantizedAt(idx), baseline.quantizedAt(idx); got != want {
				t.Fatalf("block=%d logical_decision=%d: got=%d want=%d", block, idx, got, want)
			}
		}
		for _, idx := range []int{0, power16BlockSize - 1, power16BlockSize, baseline.Cfg.BufferLength - power16History} {
			got := candidate.Power16Window(idx, power16History)
			want := baseline.Power16Window(idx, power16History)
			if !s6EqualUint16(got, want) {
				t.Fatalf("block=%d history_idx=%d differs", block, idx)
			}
		}
	}
}

func TestS6PackedPathPreservesReplacedQuantizedBuffer(t *testing.T) {
	baselinePlatform := power16DecoderTestPlatform()
	candidatePlatform := power16DecoderTestPlatform()
	candidatePlatform.implementation = "s6-packed-replaced-quantized-test"
	candidatePlatform.runPacked = func(decisions, packed []byte, window []uint16, input []byte) bool {
		if !power16ReferenceBlock(decisions, window, input) {
			return false
		}
		s6PackOracle(packed, decisions)
		return true
	}
	baseline := newPower16AutomaticTestDecoder(baselinePlatform)
	candidate := newPower16AutomaticTestDecoder(candidatePlatform)
	baseline.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	candidate.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	baseline.Allocate()
	candidate.Allocate()

	// Quantized is public. A same-sized caller replacement must keep the
	// established append path rather than writing to stale decoder storage.
	candidate.Quantized = make([]byte, candidate.Cfg.BufferLength)
	if candidate.ownsQuantizedRing() {
		t.Fatal("caller-replaced Quantized was treated as decoder-owned")
	}

	rng := rand.New(rand.NewSource(0x56a6ca11))
	input := make([]byte, power16BlockSize*2)
	for block := 0; block < 32; block++ {
		if _, err := rng.Read(input); err != nil {
			t.Fatal(err)
		}
		baseline.Decode(input)
		candidate.Decode(input)
		if !bytes.Equal(baseline.filterOutput, candidate.filterOutput) ||
			!bytes.Equal(baseline.Quantized, candidate.Quantized) ||
			baseline.quantizedStart != candidate.quantizedStart ||
			!bytes.Equal(baseline.packed, candidate.packed) {
			t.Fatalf("block=%d: caller-replacement fallback differs", block)
		}
	}
	if len(candidate.filterScratch) == 0 || &candidate.filterOutput[0] != &candidate.filterScratch[0] {
		t.Fatal("caller-replacement fallback did not use the separate decision scratch")
	}
}

func s6EqualUint16(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}
