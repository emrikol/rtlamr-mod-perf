//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import "testing"

func benchmarkManchesterA72Input() []float64 {
	lut := NewMagLUT()
	input := make([]float64, filterManchesterA72InputSize)
	for idx := range input {
		i := byte(idx*73 + idx/17*29 + 11)
		q := byte(idx*37 + idx/31*19 + 7)
		input[idx] = lut[i] + lut[q]
	}
	return input
}

func benchmarkManchesterA72Leaf(b *testing.B, leaf manchesterA72Leaf) {
	input := benchmarkManchesterA72Input()
	output := make([]byte, filterManchesterA72OutputSize)
	b.ReportAllocs()
	b.SetBytes(int64(len(input) * 8))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		runManchesterA72Leaf(leaf, input, output)
	}
	b.StopTimer()
	manchesterDecisionSink = output[(b.N+len(output)-1)%len(output)]
}

// BenchmarkManchesterA72GoDirect and BenchmarkManchesterA72ProductionDirect
// retain a same-fixture direct comparison between the literal Go recurrence
// and the single production assembly leaf without dispatch overhead.
func BenchmarkManchesterA72GoDirect(b *testing.B) {
	input := benchmarkManchesterA72Input()
	output := make([]byte, filterManchesterA72OutputSize)
	b.ReportAllocs()
	b.SetBytes(int64(len(input) * 8))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		filterManchesterA72Reference(input, output, filterManchesterA72ChipLength)
	}
	b.StopTimer()
	manchesterDecisionSink = output[(b.N+len(output)-1)%len(output)]
}

func BenchmarkManchesterA72ProductionDirect(b *testing.B) {
	benchmarkManchesterA72Leaf(b, filterManchesterA72TBXSSRABalanced)
}

func BenchmarkManchesterA72ProductionWrapper(b *testing.B) {
	if !filterManchesterA72Available() {
		b.Skip("exact Cortex-A72 Manchester leaf unavailable")
	}
	input := benchmarkManchesterA72Input()
	output := make([]byte, filterManchesterA72OutputSize)
	decoder := Decoder{Cfg: PacketConfig{ChipLength: filterManchesterA72ChipLength}}
	b.ReportAllocs()
	b.SetBytes(int64(len(input) * 8))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		decoder.Filter(input, output)
	}
	b.StopTimer()
	manchesterDecisionSink = output[(b.N+len(output)-1)%len(output)]
}
