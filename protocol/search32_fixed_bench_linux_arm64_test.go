//go:build search32bench && linux && arm64 && gc && !purego && !race
// +build search32bench,linux,arm64,gc,!purego,!race

package protocol

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

var (
	search32FixedBenchProtocol = flag.String("search32fixed-protocol", "idm", "fixed preamble: idm or r900")
	search32FixedBenchCalls    = flag.Int("search32fixed-calls", 100000, "leaf calls in each timed sample")
	search32FixedBenchQuartets = flag.Int("search32fixed-quartets", 20, "alternating ABBA quartets")
	search32FixedPackedFile    = flag.String("search32fixed-packed-file", "", "optional packed corpus fixture")
	search32FixedPackedRecord  = flag.Int("search32fixed-packed-record", 0, "zero-based packed corpus record")
	search32FixedPackedResidue = flag.Int("search32fixed-packed-residue", 0, "packed input address modulo 64")
	search32FixedDstResidue    = flag.Int("search32fixed-dst-residue", 0, "scratch destination address modulo 64")
)

type search32FixedRunFunc func(dst, packed, masks, indices unsafe.Pointer, symLenByte, count, calls int)

type search32FixedCounters struct {
	cycles       int
	instructions int
}

type search32FixedMeasure struct {
	cycles       float64
	instructions float64
}

func TestSearch32ProductionPackedAllocationResidues(t *testing.T) {
	const (
		allocations = 4096
		packedSize  = (8192 + 32*144 + 7) >> 3
	)
	residues := make([]int, 64)
	keep := make([][]byte, allocations)
	for idx := range keep {
		keep[idx] = make([]byte, packedSize)
		residues[uintptr(unsafe.Pointer(&keep[idx][0]))&63]++
	}
	for residue, count := range residues {
		if count != 0 {
			fmt.Printf("PACKED_ALLOCATION_RESIDUE residue=%d count=%d\n", residue, count)
		}
	}
	runtime.KeepAlive(keep)
}

func TestSearch32FixedLeafIsolatedABBA(t *testing.T) {
	if *search32FixedBenchCalls <= 0 || *search32FixedBenchQuartets < 2 || *search32FixedPackedResidue < 0 || *search32FixedPackedResidue > 63 || *search32FixedDstResidue < 0 || *search32FixedDstResidue > 63 {
		t.Fatal("search32fixed calls/quartets/residues are invalid")
	}
	const (
		count      = 1024
		symLenByte = 18
	)
	bits, candidateLeaf, candidateRun := search32FixedBenchSelection(t, *search32FixedBenchProtocol)
	preamble := make([]byte, len(bits))
	masks := make([]byte, len(bits))
	for idx, bit := range []byte(bits) {
		preamble[idx] = bit - '0'
		masks[idx] = (preamble[idx] ^ 1) * 0xff
	}
	packed := bytesAtCacheLineResidue(count+symLenByte*31, *search32FixedPackedResidue)
	if *search32FixedPackedFile == "" {
		rand.New(rand.NewSource(0x32a72)).Read(packed)
	} else {
		fixture, err := os.ReadFile(*search32FixedPackedFile)
		if err != nil {
			t.Fatal(err)
		}
		if len(fixture)%len(packed) != 0 || *search32FixedPackedRecord < 0 || (*search32FixedPackedRecord+1)*len(packed) > len(fixture) {
			t.Fatalf("invalid packed fixture: bytes=%d record=%d record_bytes=%d", len(fixture), *search32FixedPackedRecord, len(packed))
		}
		copy(packed, fixture[*search32FixedPackedRecord*len(packed):(*search32FixedPackedRecord+1)*len(packed)])
	}
	dst := bytesAtCacheLineResidue(count, *search32FixedDstResidue)
	wantIndices := make([]int, count*8)
	gotIndices := make([]int, count*8)
	wantN := searchAlignedCandidates32A72(unsafe.Pointer(&dst[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&masks[0]), unsafe.Pointer(&wantIndices[0]), symLenByte, count)
	gotN := candidateLeaf(unsafe.Pointer(&dst[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&masks[0]), unsafe.Pointer(&gotIndices[0]), symLenByte, count)
	if gotN != wantN || !equalInts(gotIndices[:gotN], wantIndices[:wantN]) {
		t.Fatal("fixed candidate differs from current A72 leaf")
	}

	oldGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGCPercent)
	runtime.GC()
	runtime.GOMAXPROCS(1)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	counters := openSearch32FixedCounters(t)
	defer counters.close()
	search32FixedRunCurrent(unsafe.Pointer(&dst[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&masks[0]), unsafe.Pointer(&wantIndices[0]), symLenByte, count, 10000)
	candidateRun(unsafe.Pointer(&dst[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&masks[0]), unsafe.Pointer(&gotIndices[0]), symLenByte, count, 10000)

	type arm struct {
		label string
		fn    search32FixedRunFunc
	}
	abba := [...]arm{{"A1", search32FixedRunCurrent}, {"B1", candidateRun}, {"B2", candidateRun}, {"A2", search32FixedRunCurrent}}
	baab := [...]arm{{"B1", candidateRun}, {"A1", search32FixedRunCurrent}, {"A2", search32FixedRunCurrent}, {"B2", candidateRun}}
	deltas := make([]float64, 0, *search32FixedBenchQuartets)
	var baselineCycles, candidateCycles, baselineInstructions, candidateInstructions []float64
	fmt.Printf("SEARCH32_FIXED_ABBA protocol=%s calls_per_sample=%d quartets=%d count=%d stride=%d packed_residue=%d dst_residue=%d packed_file=%q packed_record=%d survivors=%d\n", *search32FixedBenchProtocol, *search32FixedBenchCalls, *search32FixedBenchQuartets, count, symLenByte, uintptr(unsafe.Pointer(&packed[0]))&63, uintptr(unsafe.Pointer(&dst[0]))&63, *search32FixedPackedFile, *search32FixedPackedRecord, gotN)
	for quartet := 0; quartet < *search32FixedBenchQuartets; quartet++ {
		order := abba
		if quartet&1 != 0 {
			order = baab
		}
		var a, b []float64
		for _, sample := range order {
			measurement := counters.measure(t, sample.fn, dst, packed, masks, gotIndices, *search32FixedBenchCalls)
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
		aMean := (a[0] + a[1]) / 2
		bMean := (b[0] + b[1]) / 2
		delta := 100 * (aMean - bMean) / aMean
		deltas = append(deltas, delta)
		fmt.Printf("QUARTET quartet=%03d baseline_cycles=%.6f candidate_cycles=%.6f candidate_faster_pct=%.6f\n", quartet+1, aMean, bMean, delta)
	}
	meanDelta, sdDelta := search32FixedMeanSD(deltas)
	halfWidth := search32FixedStudentT95(len(deltas)-1) * sdDelta / math.Sqrt(float64(len(deltas)))
	fmt.Printf("SUMMARY protocol=%s baseline_mean_cycles=%.6f candidate_mean_cycles=%.6f baseline_mean_instructions=%.6f candidate_mean_instructions=%.6f mean_faster_pct=%.6f ci95_low=%.6f ci95_high=%.6f baseline_median_cycles=%.6f candidate_median_cycles=%.6f\n", *search32FixedBenchProtocol, search32FixedMean(baselineCycles), search32FixedMean(candidateCycles), search32FixedMean(baselineInstructions), search32FixedMean(candidateInstructions), meanDelta, meanDelta-halfWidth, meanDelta+halfWidth, search32FixedMedian(baselineCycles), search32FixedMedian(candidateCycles))
}

func search32FixedBenchSelection(t *testing.T, protocol string) (string, func(dst, packed, masks, indices unsafe.Pointer, symLenByte, count int) int, search32FixedRunFunc) {
	t.Helper()
	switch protocol {
	case "idm":
		return "01010101010101010001011010100011", searchAlignedCandidates32IDMA72, search32FixedRunIDM
	case "r900":
		return "00000000000000001110010101100100", searchAlignedCandidates32R900A72, search32FixedRunR900
	default:
		t.Fatalf("unknown search32fixed protocol %q", protocol)
		return "", nil, nil
	}
}

func openSearch32FixedCounters(t *testing.T) search32FixedCounters {
	t.Helper()
	open := func(config uint64) int {
		attr := unix.PerfEventAttr{Type: unix.PERF_TYPE_HARDWARE, Size: unix.PERF_ATTR_SIZE_VER0, Config: config, Bits: unix.PerfBitDisabled | unix.PerfBitExcludeKernel | unix.PerfBitExcludeHv}
		fd, err := unix.PerfEventOpen(&attr, 0, -1, -1, 0)
		if err != nil {
			t.Fatalf("perf_event_open config %d: %v", config, err)
		}
		return fd
	}
	return search32FixedCounters{cycles: open(unix.PERF_COUNT_HW_CPU_CYCLES), instructions: open(unix.PERF_COUNT_HW_INSTRUCTIONS)}
}

func (c search32FixedCounters) close() {
	unix.Close(c.cycles)
	unix.Close(c.instructions)
}

func (c search32FixedCounters) measure(t *testing.T, fn search32FixedRunFunc, dst, packed, masks []byte, indices []int, calls int) search32FixedMeasure {
	t.Helper()
	for _, fd := range [...]int{c.cycles, c.instructions} {
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_RESET, 0); err != nil {
			t.Fatal(err)
		}
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_ENABLE, 0); err != nil {
			t.Fatal(err)
		}
	}
	fn(unsafe.Pointer(&dst[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&masks[0]), unsafe.Pointer(&indices[0]), 18, len(dst), calls)
	for _, fd := range [...]int{c.cycles, c.instructions} {
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_DISABLE, 0); err != nil {
			t.Fatal(err)
		}
	}
	readCounter := func(fd int) uint64 {
		var data [8]byte
		n, err := unix.Read(fd, data[:])
		if err != nil || n != len(data) {
			t.Fatalf("read perf counter: bytes=%d err=%v", n, err)
		}
		return binary.LittleEndian.Uint64(data[:])
	}
	return search32FixedMeasure{cycles: float64(readCounter(c.cycles)) / float64(calls), instructions: float64(readCounter(c.instructions)) / float64(calls)}
}

func search32FixedMean(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func search32FixedMeanSD(values []float64) (float64, float64) {
	mean := search32FixedMean(values)
	var sum float64
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	return mean, math.Sqrt(sum / float64(len(values)-1))
}

func search32FixedMedian(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	mid := len(ordered) / 2
	if len(ordered)&1 != 0 {
		return ordered[mid]
	}
	return (ordered[mid-1] + ordered[mid]) / 2
}

func search32FixedStudentT95(df int) float64 {
	table := [...]float64{0, 12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228, 2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086, 2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042}
	if df < len(table) {
		return table[df]
	}
	if df < 60 {
		return 2
	}
	return 1.96
}

//go:noescape
func search32FixedRunCurrent(dst, packed, masks, indices unsafe.Pointer, symLenByte, count, calls int)

//go:noescape
func search32FixedRunIDM(dst, packed, masks, indices unsafe.Pointer, symLenByte, count, calls int)

//go:noescape
func search32FixedRunR900(dst, packed, masks, indices unsafe.Pointer, symLenByte, count, calls int)
