//go:build r900_power16_simd && linux && arm64 && gc && !purego && !race

package r900

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const (
	r900Power16PMUCalls       = 1 << 24
	r900Power16PMUQuartets    = 6
	r900Power16PMUWarmupCalls = 1 << 16
	r900Power16PMUCPUDefault  = 3
	r900Power16PMUA72MIDR     = "0x00000000410fd083"
	r900Power16AffinityBytes  = 128

	r900PerfTypeHardware      = 0
	r900PerfCountCycles       = 0
	r900PerfCountInstructions = 1
	r900PerfFormatEnabled     = 1 << 0
	r900PerfFormatRunning     = 1 << 1
	r900PerfFormatID          = 1 << 2
	r900PerfFormatGroup       = 1 << 3
	r900PerfAttrDisabled      = 1 << 0
	r900PerfAttrPinned        = 1 << 2
	r900PerfAttrExcludeKernel = 1 << 5
	r900PerfAttrExcludeHV     = 1 << 6
	r900PerfIOCEnable         = 0x2400
	r900PerfIOCDisable        = 0x2401
	r900PerfIOCReset          = 0x2403
	r900PerfIOCID             = 0x80082407
	r900PerfIOCGroup          = 1
	r900PerfEventOpenARM64    = 241
)

type r900PerfAttrV0 struct {
	Type         uint32
	Size         uint32
	Config       uint64
	SamplePeriod uint64
	SampleType   uint64
	ReadFormat   uint64
	Flags        uint64
	WakeupEvents uint32
	BPType       uint32
	Config1      uint64
}

type r900Power16PMUGroup struct {
	leader, member     int
	cyclesID, instrsID uint64
}

type r900Power16PMUSample struct {
	cycles, instructions uint64
	enabled, running     uint64
	wall                 time.Duration
}

type r900Power16PMUQuartet struct {
	a, b [2]r900Power16PMUSample
}

func TestR900Power16L72PMU(t *testing.T) {
	mode := os.Getenv("RTLAMR_R900_POWER16_PMU")
	if mode != "aa" && mode != "ab" {
		t.Skip("set RTLAMR_R900_POWER16_PMU=aa or ab for the opt-in A72 same-binary PMU screen")
	}
	midr := strings.TrimSpace(r900Power16ReadFile("/sys/devices/system/cpu/cpu0/regs/identification/midr_el1"))
	if midr != r900Power16PMUA72MIDR {
		t.Fatalf("R900 Power16 PMU requires Cortex-A72 r0p3 MIDR %s, got %q", r900Power16PMUA72MIDR, midr)
	}
	cpu := r900Power16PMUCPU(t)
	fixture := r900Power16BenchmarkFixture()
	fixtureBytes := unsafeBytesR900Power16(fixture)
	fixtureSHA := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes))
	for slot := 0; slot < 42; slot++ {
		window := fixture[slot*r900Power16TestChipLength*4:]
		want := r900Power16Oracle(window).symbol
		if goValue, selected := quantizePower16WindowGo(window, r900Power16TestChipLength), quantizePower16Window(window, r900Power16TestChipLength); goValue != want || selected != want {
			t.Fatalf("pre-PMU oracle gate slot=%d oracle=%d go=%d selected=%d", slot, want, goValue, selected)
		}
	}

	oldProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldProcs)
	runtime.GC()
	oldGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGC)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	originalAffinity, err := r900Power16PinThread(cpu)
	if err != nil {
		t.Fatalf("pin CPU %d: %v", cpu, err)
	}
	defer func() {
		if err := r900Power16RestoreAffinity(originalAffinity); err != nil {
			t.Errorf("restore affinity: %v", err)
		}
	}()
	group, err := r900Power16OpenPMUGroup()
	if err != nil {
		t.Fatalf("open PMU group: %v", err)
	}
	defer group.close()

	goRun := r900Power16PMURunner(fixture, quantizePower16WindowGo)
	selectedRun := r900Power16PMURunner(fixture, quantizePower16Window)
	aRun, bRun := selectedRun, selectedRun
	aLabel, bLabel := "selected-a", "selected-b"
	if mode == "ab" {
		aRun, bRun = goRun, selectedRun
		aLabel, bLabel = "go", "simd"
	}
	aRun(r900Power16PMUWarmupCalls)
	bRun(r900Power16PMUWarmupCalls)
	t.Logf("R900_POWER16_PMU_CONFIG mode=%s fixture=%s windows=42 chip=72 calls=%d warmup=%d quartets=%d cpu=%d midr=%s go=%s runtime=%q affinity=%x A=%s B=%s",
		mode, fixtureSHA, r900Power16PMUCalls, r900Power16PMUWarmupCalls, r900Power16PMUQuartets,
		cpu, midr, runtime.GOOS+"/"+runtime.GOARCH, runtime.Version(), originalAffinity, aLabel, bLabel)

	quartets := make([]r900Power16PMUQuartet, r900Power16PMUQuartets)
	for quartet := range quartets {
		order := [4]byte{'A', 'B', 'B', 'A'}
		if quartet&1 != 0 {
			order = [4]byte{'B', 'A', 'A', 'B'}
		}
		var aIndex, bIndex int
		for position, arm := range order {
			run := aRun
			if arm == 'B' {
				run = bRun
			}
			frequency := r900Power16ReadFile(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_cur_freq", cpu))
			temperature := r900Power16ReadFile("/sys/class/thermal/thermal_zone0/temp")
			sample, err := group.measure(func() { run(r900Power16PMUCalls) })
			if err != nil {
				t.Fatalf("quartet=%d position=%d arm=%c: %v", quartet+1, position+1, arm, err)
			}
			if sample.enabled != sample.running {
				t.Fatalf("quartet=%d position=%d arm=%c enabled=%d running=%d", quartet+1, position+1, arm, sample.enabled, sample.running)
			}
			if arm == 'A' {
				quartets[quartet].a[aIndex], aIndex = sample, aIndex+1
			} else {
				quartets[quartet].b[bIndex], bIndex = sample, bIndex+1
			}
			t.Logf("R900_POWER16_PMU_RAW quartet=%d position=%d arm=%c label=%s cycles_per_call=%.6f instructions_per_call=%.6f enabled=%d running=%d wall_ns=%d freq_khz=%q temp_mc=%q",
				quartet+1, position+1, arm, map[bool]string{true: bLabel, false: aLabel}[arm == 'B'],
				float64(sample.cycles)/r900Power16PMUCalls, float64(sample.instructions)/r900Power16PMUCalls,
				sample.enabled, sample.running, sample.wall.Nanoseconds(), frequency, temperature)
		}
	}
	r900Power16ReportPMU(t, mode, aLabel, bLabel, quartets)
}

func unsafeBytesR900Power16(values []uint16) []byte {
	if len(values) == 0 {
		return nil
	}
	return (*[1 << 30]byte)(unsafe.Pointer(&values[0]))[: len(values)*2 : len(values)*2]
}

func r900Power16PMURunner(fixture []uint16, quantize func([]uint16, int) byte) func(int) {
	return func(calls int) {
		var sink byte
		slot := 0
		for call := 0; call < calls; call++ {
			window := fixture[slot*r900Power16TestChipLength*4:]
			sink ^= quantize(window, r900Power16TestChipLength)
			slot++
			if slot == 42 {
				slot = 0
			}
		}
		r900Power16BenchmarkSink = sink
	}
}

func r900Power16ReportPMU(t *testing.T, mode, aLabel, bLabel string, quartets []r900Power16PMUQuartet) {
	t.Helper()
	effects := make([]float64, len(quartets))
	var aCycles, bCycles, aInstructions, bInstructions float64
	for index, quartet := range quartets {
		aCycleMean := float64(quartet.a[0].cycles+quartet.a[1].cycles) / (2 * r900Power16PMUCalls)
		bCycleMean := float64(quartet.b[0].cycles+quartet.b[1].cycles) / (2 * r900Power16PMUCalls)
		aInstructionMean := float64(quartet.a[0].instructions+quartet.a[1].instructions) / (2 * r900Power16PMUCalls)
		bInstructionMean := float64(quartet.b[0].instructions+quartet.b[1].instructions) / (2 * r900Power16PMUCalls)
		aCycles += aCycleMean
		bCycles += bCycleMean
		aInstructions += aInstructionMean
		bInstructions += bInstructionMean
		effects[index] = 100 * (aCycleMean - bCycleMean) / aCycleMean
	}
	n := float64(len(quartets))
	aCycles, bCycles = aCycles/n, bCycles/n
	aInstructions, bInstructions = aInstructions/n, bInstructions/n
	mean, standardDeviation := r900Power16MeanSD(effects)
	halfWidth := 2.571 * standardDeviation / math.Sqrt(n)
	t.Logf("R900_POWER16_PMU_SUMMARY mode=%s A=%s B=%s calls=%d quartets=%d A_cycles=%.6f B_cycles=%.6f A_instructions=%.6f B_instructions=%.6f paired_effect=%.6f%% sample_sd=%.6f ci95=[%.6f,%.6f]",
		mode, aLabel, bLabel, r900Power16PMUCalls, len(quartets), aCycles, bCycles, aInstructions, bInstructions,
		mean, standardDeviation, mean-halfWidth, mean+halfWidth)
}

func r900Power16MeanSD(values []float64) (float64, float64) {
	var mean float64
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	var squares float64
	for _, value := range values {
		delta := value - mean
		squares += delta * delta
	}
	return mean, math.Sqrt(squares / float64(len(values)-1))
}

func r900Power16OpenPMUGroup() (*r900Power16PMUGroup, error) {
	leader, err := r900Power16OpenPerf(r900PerfCountCycles, -1)
	if err != nil {
		return nil, err
	}
	member, err := r900Power16OpenPerf(r900PerfCountInstructions, leader)
	if err != nil {
		_ = syscall.Close(leader)
		return nil, err
	}
	cyclesID, err := r900Power16PerfID(leader)
	if err != nil {
		_ = syscall.Close(member)
		_ = syscall.Close(leader)
		return nil, err
	}
	instrsID, err := r900Power16PerfID(member)
	if err != nil {
		_ = syscall.Close(member)
		_ = syscall.Close(leader)
		return nil, err
	}
	return &r900Power16PMUGroup{leader: leader, member: member, cyclesID: cyclesID, instrsID: instrsID}, nil
}

func r900Power16OpenPerf(config uint64, group int) (int, error) {
	flags := uint64(r900PerfAttrDisabled | r900PerfAttrExcludeKernel | r900PerfAttrExcludeHV)
	if group < 0 {
		flags |= r900PerfAttrPinned
	}
	attr := r900PerfAttrV0{Type: r900PerfTypeHardware, Size: uint32(unsafe.Sizeof(r900PerfAttrV0{})), Config: config,
		ReadFormat: r900PerfFormatGroup | r900PerfFormatID | r900PerfFormatEnabled | r900PerfFormatRunning, Flags: flags}
	fd, _, errno := syscall.RawSyscall6(r900PerfEventOpenARM64, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), uintptr(group), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func (group *r900Power16PMUGroup) close() {
	_ = syscall.Close(group.member)
	_ = syscall.Close(group.leader)
}

func (group *r900Power16PMUGroup) measure(run func()) (r900Power16PMUSample, error) {
	if err := r900Power16IOCTL(group.leader, r900PerfIOCReset, r900PerfIOCGroup); err != nil {
		return r900Power16PMUSample{}, err
	}
	if err := r900Power16IOCTL(group.leader, r900PerfIOCEnable, r900PerfIOCGroup); err != nil {
		return r900Power16PMUSample{}, err
	}
	started := time.Now()
	run()
	elapsed := time.Since(started)
	if err := r900Power16IOCTL(group.leader, r900PerfIOCDisable, r900PerfIOCGroup); err != nil {
		return r900Power16PMUSample{}, err
	}
	var encoded [56]byte
	read, err := syscall.Read(group.leader, encoded[:])
	if err != nil || read != len(encoded) || binary.LittleEndian.Uint64(encoded[:8]) != 2 {
		return r900Power16PMUSample{}, fmt.Errorf("invalid PMU group read bytes=%d err=%v", read, err)
	}
	sample := r900Power16PMUSample{enabled: binary.LittleEndian.Uint64(encoded[8:16]), running: binary.LittleEndian.Uint64(encoded[16:24]), wall: elapsed}
	for offset := 24; offset < len(encoded); offset += 16 {
		value, id := binary.LittleEndian.Uint64(encoded[offset:offset+8]), binary.LittleEndian.Uint64(encoded[offset+8:offset+16])
		switch id {
		case group.cyclesID:
			sample.cycles = value
		case group.instrsID:
			sample.instructions = value
		default:
			return r900Power16PMUSample{}, fmt.Errorf("unknown PMU ID %d", id)
		}
	}
	if sample.cycles == 0 || sample.instructions == 0 || sample.enabled == 0 || sample.running == 0 {
		return r900Power16PMUSample{}, fmt.Errorf("empty PMU sample %+v", sample)
	}
	return sample, nil
}

func r900Power16PerfID(fd int) (uint64, error) {
	var id uint64
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), r900PerfIOCID, uintptr(unsafe.Pointer(&id)))
	if errno != 0 || id == 0 {
		return 0, fmt.Errorf("perf ID=%d errno=%v", id, errno)
	}
	return id, nil
}

func r900Power16IOCTL(fd int, request, value uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, value)
	if errno != 0 {
		return errno
	}
	return nil
}

func r900Power16PMUCPU(t *testing.T) int {
	t.Helper()
	value := os.Getenv("RTLAMR_R900_POWER16_CPU")
	if value == "" {
		return r900Power16PMUCPUDefault
	}
	cpu, err := strconv.Atoi(value)
	if err != nil || cpu < 0 || cpu >= r900Power16AffinityBytes*8 {
		t.Fatalf("invalid RTLAMR_R900_POWER16_CPU=%q", value)
	}
	return cpu
}

func r900Power16PinThread(cpu int) ([]byte, error) {
	original := make([]byte, r900Power16AffinityBytes)
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(original)), uintptr(unsafe.Pointer(&original[0])))
	if errno != 0 {
		return nil, errno
	}
	mask := make([]byte, r900Power16AffinityBytes)
	mask[cpu/8] = 1 << uint(cpu%8)
	_, _, errno = syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0])))
	if errno != 0 {
		return nil, errno
	}
	return original, nil
}

func r900Power16RestoreAffinity(mask []byte) error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

func r900Power16ReadFile(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "unavailable:" + err.Error()
	}
	return strings.TrimSpace(string(contents))
}
