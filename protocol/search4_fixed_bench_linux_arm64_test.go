//go:build search4fixedbench && linux && arm64 && gc && !purego && !race
// +build search4fixedbench,linux,arm64,gc,!purego,!race

package protocol

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"testing"

	"golang.org/x/sys/unix"
)

var (
	search4FixedBenchProtocol = flag.String("search4fixed-protocol", "scm", "fixed preamble: scm or scmplus")
	search4FixedBenchCount    = flag.Int("search4fixed-count", 1024, "candidate-mask bytes: 512 or 1024")
	search4FixedBenchCalls    = flag.Int("search4fixed-calls", 5000, "complete searches in each timed sample")
	search4FixedBenchQuartets = flag.Int("search4fixed-quartets", 30, "alternating ABBA quartets")
	search4FixedBenchPacked   = flag.String("search4fixed-packed-file", "", "optional 1582-byte-record packed corpus")
	search4FixedBenchRecord   = flag.Int("search4fixed-packed-record", 0, "zero-based packed corpus record")
)

var search4FixedBenchSink int

func TestSearch4FixedDispatchABBA(t *testing.T) {
	if !searchAlignedCandidates4FixedAvailable() {
		t.Skip("Cortex-A72 fixed SCM search is unavailable")
	}
	if (*search4FixedBenchCount != 512 && *search4FixedBenchCount != 1024) || *search4FixedBenchCalls <= 0 || *search4FixedBenchQuartets < 2 {
		t.Fatal("invalid benchmark geometry")
	}
	var preamble []byte
	switch *search4FixedBenchProtocol {
	case "scm":
		preamble = searchAlignedCandidatesSCMPreamble[:]
	case "scmplus":
		preamble = searchAlignedCandidatesSCMPlusPreamble[:]
	default:
		t.Fatalf("unknown protocol %q", *search4FixedBenchProtocol)
	}
	count := *search4FixedBenchCount
	packedLen := count + 18*(len(preamble)-1)
	packed := make([]byte, packedLen)
	if *search4FixedBenchPacked == "" {
		state := uint64(0x9e3779b97f4a7c15)
		for idx := range packed {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			packed[idx] = byte(state)
		}
	} else {
		fixture, err := os.ReadFile(*search4FixedBenchPacked)
		if err != nil {
			t.Fatal(err)
		}
		const recordSize = 1582
		offset := *search4FixedBenchRecord * recordSize
		if len(fixture)%recordSize != 0 || offset < 0 || offset+recordSize > len(fixture) || packedLen > recordSize {
			t.Fatal("invalid packed fixture or record")
		}
		copy(packed, fixture[offset:offset+packedLen])
	}
	var masks [4]byte
	for idx := 0; idx < 4; idx++ {
		masks[idx] = (preamble[idx] ^ 1) * 0xff
	}
	scratch := make([]byte, count)
	indices := make([]int, 0, count*8)
	baselineDecoder := Decoder{packed: packed, sIdxA: make([]int, 0, count*8)}
	searchAlignedCandidates4Platform(scratch, packed, 18, masks)
	want := append([]int(nil), baselineDecoder.finishAlignedCandidates(preamble, 18, scratch)...)
	got, ok := searchAlignedCandidates4FixedPlatform(preamble, scratch, packed, 18, indices)
	if !ok || !equalInts(got, want) {
		t.Fatal("production dispatch differs from current path")
	}

	oldGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGCPercent)
	runtime.GC()
	runtime.GOMAXPROCS(1)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	counters := openSearch4FixedCounters(t)
	defer counters.close()

	type arm struct {
		label     string
		candidate bool
	}
	abba := [...]arm{{"A1", false}, {"B1", true}, {"B2", true}, {"A2", false}}
	baab := [...]arm{{"B1", true}, {"A1", false}, {"A2", false}, {"B2", true}}
	var baselineCycles, candidateCycles, baselineInstructions, candidateInstructions, deltas []float64
	for quartet := 0; quartet < *search4FixedBenchQuartets; quartet++ {
		order := abba
		if quartet&1 != 0 {
			order = baab
		}
		var a, b []float64
		for _, sample := range order {
			measurement := counters.measure(t, sample.candidate, preamble, masks, scratch, packed, indices, &baselineDecoder, *search4FixedBenchCalls)
			if sample.label[0] == 'A' {
				a = append(a, measurement.cycles)
				baselineCycles = append(baselineCycles, measurement.cycles)
				baselineInstructions = append(baselineInstructions, measurement.instructions)
			} else {
				b = append(b, measurement.cycles)
				candidateCycles = append(candidateCycles, measurement.cycles)
				candidateInstructions = append(candidateInstructions, measurement.instructions)
			}
		}
		aMean, bMean := (a[0]+a[1])/2, (b[0]+b[1])/2
		deltas = append(deltas, 100*(aMean-bMean)/aMean)
	}
	meanDelta, sdDelta := search4FixedMeanSD(deltas)
	halfWidth := search4FixedStudentT95(len(deltas)-1) * sdDelta / math.Sqrt(float64(len(deltas)))
	fmt.Printf("SEARCH4_FIXED_DISPATCH protocol=%s count=%d record=%d survivors=%d baseline_cycles=%.6f candidate_cycles=%.6f baseline_instructions=%.6f candidate_instructions=%.6f faster_pct=%.6f ci95_low=%.6f ci95_high=%.6f baseline_median_cycles=%.6f candidate_median_cycles=%.6f\n",
		*search4FixedBenchProtocol, count, *search4FixedBenchRecord, len(got), search4FixedMean(baselineCycles), search4FixedMean(candidateCycles), search4FixedMean(baselineInstructions), search4FixedMean(candidateInstructions), meanDelta, meanDelta-halfWidth, meanDelta+halfWidth, search4FixedMedian(baselineCycles), search4FixedMedian(candidateCycles))
}

type search4FixedCounters struct{ cycles, instructions int }
type search4FixedMeasure struct{ cycles, instructions float64 }

func openSearch4FixedCounters(t *testing.T) search4FixedCounters {
	open := func(config uint64) int {
		attr := unix.PerfEventAttr{Type: unix.PERF_TYPE_HARDWARE, Size: unix.PERF_ATTR_SIZE_VER0, Config: config, Bits: unix.PerfBitDisabled | unix.PerfBitExcludeKernel | unix.PerfBitExcludeHv}
		fd, err := unix.PerfEventOpen(&attr, 0, -1, -1, 0)
		if err != nil {
			t.Fatal(err)
		}
		return fd
	}
	return search4FixedCounters{cycles: open(unix.PERF_COUNT_HW_CPU_CYCLES), instructions: open(unix.PERF_COUNT_HW_INSTRUCTIONS)}
}

func (c search4FixedCounters) close() {
	unix.Close(c.cycles)
	unix.Close(c.instructions)
}

func (c search4FixedCounters) measure(t *testing.T, candidate bool, preamble []byte, masks [4]byte, scratch, packed []byte, indices []int, decoder *Decoder, calls int) search4FixedMeasure {
	for _, fd := range [...]int{c.cycles, c.instructions} {
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_RESET, 0); err != nil {
			t.Fatal(err)
		}
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_ENABLE, 0); err != nil {
			t.Fatal(err)
		}
	}
	if candidate {
		for idx := 0; idx < calls; idx++ {
			got, ok := searchAlignedCandidates4FixedPlatform(preamble, scratch, packed, 18, indices)
			if !ok {
				t.Fatal("candidate dispatch rejected")
			}
			search4FixedBenchSink = len(got)
		}
	} else {
		for idx := 0; idx < calls; idx++ {
			searchAlignedCandidates4Platform(scratch, packed, 18, masks)
			decoder.sIdxA = decoder.sIdxA[:0]
			search4FixedBenchSink = len(decoder.finishAlignedCandidates(preamble, 18, scratch))
		}
	}
	for _, fd := range [...]int{c.cycles, c.instructions} {
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_DISABLE, 0); err != nil {
			t.Fatal(err)
		}
	}
	read := func(fd int) uint64 {
		var data [8]byte
		n, err := unix.Read(fd, data[:])
		if err != nil || n != len(data) {
			t.Fatal(err)
		}
		return binary.LittleEndian.Uint64(data[:])
	}
	return search4FixedMeasure{
		cycles:       float64(read(c.cycles)) / float64(calls),
		instructions: float64(read(c.instructions)) / float64(calls),
	}
}

func search4FixedMean(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func search4FixedMeanSD(values []float64) (float64, float64) {
	mean := search4FixedMean(values)
	var sum float64
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	return mean, math.Sqrt(sum / float64(len(values)-1))
}

func search4FixedMedian(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	mid := len(ordered) / 2
	if len(ordered)&1 != 0 {
		return ordered[mid]
	}
	return (ordered[mid-1] + ordered[mid]) / 2
}

func search4FixedStudentT95(df int) float64 {
	table := [...]float64{0, 12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.262, 2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086}
	if df < len(table) {
		return table[df]
	}
	return 1.96
}
