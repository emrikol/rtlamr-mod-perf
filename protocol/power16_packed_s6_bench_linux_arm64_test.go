//go:build linux && arm64 && gc && !purego && !race

package protocol

import (
	"bytes"
	"testing"
	"unsafe"
)

var s6BoundarySink int

type s6BoundaryRunner struct {
	cfg        PacketConfig
	input      []byte
	window     []uint16
	decisions  []byte
	quantized  Decoder
	packed     []byte
	packedRing *power16State
	idm        []int
	r900       []int
}

func newS6BoundaryRunner(candidate bool) *s6BoundaryRunner {
	cfg := testDecoderConfig()
	runner := &s6BoundaryRunner{
		cfg:       cfg,
		input:     make([]byte, power16BlockSize*2),
		window:    make([]uint16, power16Window),
		decisions: make([]byte, power16BlockSize),
		quantized: Decoder{Cfg: cfg, Quantized: make([]byte, cfg.BufferLength)},
		idm:       make([]int, cfg.BlockSize),
		r900:      make([]int, cfg.BlockSize),
	}
	for idx := range runner.input {
		runner.input[idx] = byte(idx*73 + idx/31*19 + 7)
	}
	copy(runner.window, s6PackedHistory(0x600))
	if candidate {
		runner.packedRing = &power16State{
			packedLength:    cfg.BufferLength >> 3,
			packedLookahead: (cfg.BlockSize + cfg.PreambleLength) >> 3,
		}
		runner.packedRing.packedBacking = make([]byte, runner.packedRing.packedLength+runner.packedRing.packedLookahead)
		runner.packed = runner.packedRing.packedSearchWindow()
	} else {
		runner.packed = make([]byte, (cfg.BlockSize+cfg.PreambleLength)>>3)
	}
	return runner
}

func (r *s6BoundaryRunner) stepBaseline() (int, int) {
	if !power16FusedA72(r.decisions, r.window, r.input) {
		panic("S6 baseline leaf rejected fixed fixture")
	}
	r.quantized.appendQuantized(r.decisions)
	packQuantizedRing(r.packed, r.quantized.Quantized, r.quantized.quantizedStart)
	idmN, r900N := r.search()
	r.advancePowerHistory()
	return idmN, r900N
}

func (r *s6BoundaryRunner) stepCandidate() (int, int) {
	packed, nextStart, tail := r.packedRing.nextPackedOutput(r.cfg)
	if !power16FusedPackedA72(r.decisions, packed, r.window, r.input) {
		panic("S6 candidate leaf rejected fixed fixture")
	}
	r.packedRing.commitPackedOutput(nextStart, tail, len(packed))
	r.packed = r.packedRing.packedSearchWindow()
	r.quantized.appendQuantized(r.decisions)
	idmN, r900N := r.search()
	r.advancePowerHistory()
	return idmN, r900N
}

func (r *s6BoundaryRunner) search() (int, int) {
	idmN, r900N := searchAlignedCandidates32IDMR900A72(
		unsafe.Pointer(&r.packed[0]), unsafe.Pointer(&r.idm[0]), unsafe.Pointer(&r.r900[0]), r.cfg.BlockSize>>3,
	)
	return idmN, r900N
}

func (r *s6BoundaryRunner) advancePowerHistory() {
	copy(r.window[:power16History], r.window[power16BlockSize:power16Window])
}

func TestS6PackedCompleteBoundaryMatchesBaselineAcrossRingWraps(t *testing.T) {
	baseline := newS6BoundaryRunner(false)
	candidate := newS6BoundaryRunner(true)
	// The production ring has 223 byte-aligned block positions. Three complete
	// rotations cover every wrap transition repeatedly.
	for block := 0; block < 223*3; block++ {
		baseIDM, baseR900 := baseline.stepBaseline()
		candidateIDM, candidateR900 := candidate.stepCandidate()
		if baseIDM != candidateIDM || baseR900 != candidateR900 ||
			!equalInts(baseline.idm[:baseIDM], candidate.idm[:candidateIDM]) ||
			!equalInts(baseline.r900[:baseR900], candidate.r900[:candidateR900]) {
			t.Fatalf("block=%d: candidate order/count differs: IDM %d/%d R900 %d/%d", block, candidateIDM, baseIDM, candidateR900, baseR900)
		}
		if !bytes.Equal(baseline.decisions, candidate.decisions) ||
			!bytes.Equal(baseline.packed, candidate.packed) ||
			!power16Equal(baseline.window, candidate.window) {
			t.Fatalf("block=%d: producer/history/search input differs", block)
		}
	}
}

func TestS6PackedBoundaryRunnersAllocateZero(t *testing.T) {
	baseline := newS6BoundaryRunner(false)
	candidate := newS6BoundaryRunner(true)
	if got := testing.AllocsPerRun(32, func() { baseline.stepBaseline() }); got != 0 {
		t.Fatalf("baseline allocations/run=%v", got)
	}
	if got := testing.AllocsPerRun(32, func() { candidate.stepCandidate() }); got != 0 {
		t.Fatalf("candidate allocations/run=%v", got)
	}
}

func BenchmarkS6Power16PackSearchBoundary(b *testing.B) {
	b.Run("baseline-fused-plus-pack", func(b *testing.B) {
		runner := newS6BoundaryRunner(false)
		b.ReportAllocs()
		b.SetBytes(power16BlockSize * 2)
		b.ResetTimer()
		var total int
		for idx := 0; idx < b.N; idx++ {
			idmN, r900N := runner.stepBaseline()
			total += idmN + r900N
		}
		s6BoundarySink = total
	})
	b.Run("candidate-fused-packed-ring", func(b *testing.B) {
		runner := newS6BoundaryRunner(true)
		b.ReportAllocs()
		b.SetBytes(power16BlockSize * 2)
		b.ResetTimer()
		var total int
		for idx := 0; idx < b.N; idx++ {
			idmN, r900N := runner.stepCandidate()
			total += idmN + r900N
		}
		s6BoundarySink = total
	})
}
