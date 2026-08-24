//go:build linux && arm64
// +build linux,arm64

package protocol

import (
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
	// At the current roughly 32 us/filter baseline, 262,144 calls keep each
	// authoritative PMU sample near eight seconds. This is intentionally long:
	// the effect sizes we care about are smaller than short-run interrupt and
	// scheduling jitter.
	manchesterPMUCalls       = 262144
	manchesterPMUQuartets    = 6
	manchesterPMUWarmupCalls = 256

	manchesterPerfTypeHardware      = 0
	manchesterPerfCountCPUCycles    = 0
	manchesterPerfCountInstructions = 1
	manchesterPerfFormatTimeEnabled = 1 << 0
	manchesterPerfFormatTimeRunning = 1 << 1
	manchesterPerfFormatID          = 1 << 2
	manchesterPerfFormatGroup       = 1 << 3
	manchesterPerfAttrDisabled      = 1 << 0
	manchesterPerfAttrPinned        = 1 << 2
	manchesterPerfAttrExcludeKernel = 1 << 5
	manchesterPerfAttrExcludeHV     = 1 << 6
	manchesterPerfEventIOCEnable    = 0x2400
	manchesterPerfEventIOCDisable   = 0x2401
	manchesterPerfEventIOCReset     = 0x2403
	manchesterPerfEventIOCID        = 0x80082407
	manchesterPerfIOCFlagGroup      = 1
	manchesterPerfEventOpenARM64    = 241
	manchesterDefaultBenchmarkCPU   = 3
	manchesterAffinityBytes         = 128
	manchesterA72R0P3MIDR           = "0x00000000410fd083"
)

// perfEventAttrV0 is the stable 64-byte PERF_ATTR_SIZE_VER0 prefix. Keeping
// the test harness on the v0 ABI avoids coupling it to a host kernel's newer
// optional perf_event_attr fields.
type perfEventAttrV0 struct {
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

type manchesterPMUGroup struct {
	leader         int
	member         int
	cyclesID       uint64
	instructionsID uint64
}

type manchesterPMUSample struct {
	cycles       uint64
	instructions uint64
	timeEnabled  uint64
	timeRunning  uint64
	elapsed      time.Duration
}

type manchesterPMUQuartet struct {
	samples [4]manchesterPMUSample
}

func TestManchesterPMUAABaseline(t *testing.T) {
	if os.Getenv("RTLAMR_MANCHESTER_PMU") != "1" {
		t.Skip("set RTLAMR_MANCHESTER_PMU=1 to run the opt-in Linux/ARM64 PMU baseline")
	}
	fixture := requireManchesterFixture(t)
	cpu := manchesterPMUCPU(t)
	midr := requireManchesterA72R0P3(t)

	oldProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldProcs)
	runtime.GC()
	oldGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGCPercent)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	restoreAffinity, err := pinManchesterThread(cpu)
	if err != nil {
		t.Fatalf("pin benchmark thread to CPU %d: %v", cpu, err)
	}
	defer func() {
		if err := restoreManchesterAffinity(restoreAffinity); err != nil {
			t.Errorf("restore benchmark affinity: %v", err)
		}
	}()

	group, err := openManchesterPMUGroup()
	if err != nil {
		t.Fatalf("open user cycles/instructions PMU group: %v", err)
	}
	defer group.close()

	decoder := Decoder{Cfg: PacketConfig{ChipLength: manchesterFixtureChip, SymbolLength: manchesterFixtureChip * 2}}
	output := make([]byte, manchesterFixtureBlock)
	full := func(calls int) {
		block := 0
		for call := 0; call < calls; call++ {
			decoder.Filter(fixture.Windows[block], output)
			block++
			if block == len(fixture.Windows) {
				block = 0
			}
		}
		manchesterDecisionSink = output[(calls+len(output)-1)%len(output)]
	}
	initialize := func(calls int) {
		block := 0
		for call := 0; call < calls; call++ {
			// This is the real production wrapper with an empty destination, not
			// a cloned initialization loop. Linked-code review verifies the calls
			// remain and therefore include Filter's initialization and slicing.
			decoder.Filter(fixture.Windows[block], output[:0])
			block++
			if block == len(fixture.Windows) {
				block = 0
			}
		}
	}

	t.Logf("MANCHESTER_PMU_CONFIG fixture=%s cpu=%d calls=%d quartets=%d warmup_calls=%d go=%s runtime=%q midr=%s original_affinity=%x input_mod64=%d output_mod64=%d freq_khz=%q temp_mc=%q scheduler=%q",
		fixture.Digest, cpu, manchesterPMUCalls, manchesterPMUQuartets, manchesterPMUWarmupCalls,
		runtime.GOOS+"/"+runtime.GOARCH, runtime.Version(), midr, restoreAffinity,
		uintptr(unsafe.Pointer(&fixture.Windows[0][0]))%64, uintptr(unsafe.Pointer(&output[0]))%64,
		readManchesterTextFile(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_cur_freq", cpu)),
		readManchesterTextFile("/sys/class/thermal/thermal_zone0/temp"), readManchesterScheduler())
	runManchesterPMUAABaseline(t, group, "full-filter", full)
	runManchesterPMUAABaseline(t, group, "production-zero-output", initialize)
}

func runManchesterPMUAABaseline(t *testing.T, group *manchesterPMUGroup, target string, run func(int)) {
	t.Helper()
	run(manchesterPMUWarmupCalls)
	quartets := make([]manchesterPMUQuartet, manchesterPMUQuartets)
	for quartet := range quartets {
		for position := 0; position < 4; position++ {
			sample, err := group.measure(func() { run(manchesterPMUCalls) })
			if err != nil {
				t.Fatalf("%s quartet %d position %d: %v", target, quartet+1, position+1, err)
			}
			quartets[quartet].samples[position] = sample
		}
		a1 := quartets[quartet].samples[0]
		b1 := quartets[quartet].samples[1]
		b2 := quartets[quartet].samples[2]
		a2 := quartets[quartet].samples[3]
		t.Logf("MANCHESTER_PMU_RAW target=%s quartet=%d order=ABBA A1_cycles=%.3f A1_instructions=%.3f A1_enabled=%d A1_running=%d A1_wall_ns=%d B1_cycles=%.3f B1_instructions=%.3f B1_enabled=%d B1_running=%d B1_wall_ns=%d B2_cycles=%.3f B2_instructions=%.3f B2_enabled=%d B2_running=%d B2_wall_ns=%d A2_cycles=%.3f A2_instructions=%.3f A2_enabled=%d A2_running=%d A2_wall_ns=%d",
			target, quartet+1,
			float64(a1.cycles)/manchesterPMUCalls, float64(a1.instructions)/manchesterPMUCalls, a1.timeEnabled, a1.timeRunning, a1.elapsed.Nanoseconds(),
			float64(b1.cycles)/manchesterPMUCalls, float64(b1.instructions)/manchesterPMUCalls, b1.timeEnabled, b1.timeRunning, b1.elapsed.Nanoseconds(),
			float64(b2.cycles)/manchesterPMUCalls, float64(b2.instructions)/manchesterPMUCalls, b2.timeEnabled, b2.timeRunning, b2.elapsed.Nanoseconds(),
			float64(a2.cycles)/manchesterPMUCalls, float64(a2.instructions)/manchesterPMUCalls, a2.timeEnabled, a2.timeRunning, a2.elapsed.Nanoseconds())
	}
	reportManchesterPMUSummary(t, target, quartets)
}

func reportManchesterPMUSummary(t *testing.T, target string, quartets []manchesterPMUQuartet) {
	t.Helper()
	effects := make([]float64, len(quartets))
	var aCycles, bCycles, aInstructions, bInstructions float64
	for idx, quartet := range quartets {
		aCycleMean := float64(quartet.samples[0].cycles+quartet.samples[3].cycles) / (2 * manchesterPMUCalls)
		bCycleMean := float64(quartet.samples[1].cycles+quartet.samples[2].cycles) / (2 * manchesterPMUCalls)
		aInstructionMean := float64(quartet.samples[0].instructions+quartet.samples[3].instructions) / (2 * manchesterPMUCalls)
		bInstructionMean := float64(quartet.samples[1].instructions+quartet.samples[2].instructions) / (2 * manchesterPMUCalls)
		aCycles += aCycleMean
		bCycles += bCycleMean
		aInstructions += aInstructionMean
		bInstructions += bInstructionMean
		effects[idx] = 100 * (aCycleMean - bCycleMean) / aCycleMean
	}
	n := float64(len(quartets))
	aCycles /= n
	bCycles /= n
	aInstructions /= n
	bInstructions /= n
	effectMean, effectSD := meanAndSampleSDManchester(effects)
	tCritical, ok := studentT95Manchester(len(effects) - 1)
	if !ok {
		t.Fatalf("%s: unsupported Student-t degrees of freedom %d", target, len(effects)-1)
	}
	halfWidth := tCritical * effectSD / math.Sqrt(n)
	low := effectMean - halfWidth
	high := effectMean + halfWidth
	noiseFloor := math.Max(math.Abs(low), math.Abs(high))
	t.Logf("MANCHESTER_PMU_SUMMARY target=%s calls=%d quartets=%d A_cycles=%.3f B_cycles=%.3f A_instructions=%.3f B_instructions=%.3f paired_effect=%.6f%% sample_sd=%.6f ci95=[%.6f,%.6f] conservative_noise_floor=%.6f%%",
		target, manchesterPMUCalls, len(quartets), aCycles, bCycles, aInstructions, bInstructions,
		effectMean, effectSD, low, high, noiseFloor)
}

func meanAndSampleSDManchester(values []float64) (float64, float64) {
	var mean float64
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	if len(values) < 2 {
		return mean, 0
	}
	var sumSquares float64
	for _, value := range values {
		delta := value - mean
		sumSquares += delta * delta
	}
	return mean, math.Sqrt(sumSquares / float64(len(values)-1))
}

func studentT95Manchester(degrees int) (float64, bool) {
	// Two-sided 95% Student-t critical values. The harness has six quartets,
	// but retaining the short table makes a future fixed quartet-count change
	// fail conservatively rather than silently switching to a normal interval.
	critical := []float64{0, 12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
		2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
		2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042}
	if degrees > 0 && degrees < len(critical) {
		return critical[degrees], true
	}
	return 0, false
}

func openManchesterPMUGroup() (*manchesterPMUGroup, error) {
	leader, err := openManchesterPerfEvent(manchesterPerfCountCPUCycles, -1)
	if err != nil {
		return nil, fmt.Errorf("cycles: %w", err)
	}
	member, err := openManchesterPerfEvent(manchesterPerfCountInstructions, leader)
	if err != nil {
		syscall.Close(leader)
		return nil, fmt.Errorf("instructions: %w", err)
	}
	cyclesID, err := readManchesterPerfEventID(leader)
	if err != nil {
		syscall.Close(member)
		syscall.Close(leader)
		return nil, fmt.Errorf("cycles event ID: %w", err)
	}
	instructionsID, err := readManchesterPerfEventID(member)
	if err != nil {
		syscall.Close(member)
		syscall.Close(leader)
		return nil, fmt.Errorf("instructions event ID: %w", err)
	}
	return &manchesterPMUGroup{
		leader: leader, member: member, cyclesID: cyclesID, instructionsID: instructionsID,
	}, nil
}

func openManchesterPerfEvent(config uint64, groupFD int) (int, error) {
	flags := uint64(manchesterPerfAttrDisabled | manchesterPerfAttrExcludeKernel | manchesterPerfAttrExcludeHV)
	if groupFD < 0 {
		flags |= manchesterPerfAttrPinned
	}
	attr := perfEventAttrV0{
		Type:   manchesterPerfTypeHardware,
		Size:   uint32(unsafe.Sizeof(perfEventAttrV0{})),
		Config: config,
		ReadFormat: manchesterPerfFormatGroup | manchesterPerfFormatID |
			manchesterPerfFormatTimeEnabled | manchesterPerfFormatTimeRunning,
		Flags: flags,
	}
	fd, _, errno := syscall.RawSyscall6(
		manchesterPerfEventOpenARM64,
		uintptr(unsafe.Pointer(&attr)),
		0,
		^uintptr(0),
		uintptr(groupFD),
		0,
		0,
	)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func (group *manchesterPMUGroup) close() {
	if group.member >= 0 {
		syscall.Close(group.member)
	}
	if group.leader >= 0 {
		syscall.Close(group.leader)
	}
}

func (group *manchesterPMUGroup) measure(run func()) (manchesterPMUSample, error) {
	if err := ioctlManchesterPerf(group.leader, manchesterPerfEventIOCReset, manchesterPerfIOCFlagGroup); err != nil {
		return manchesterPMUSample{}, fmt.Errorf("reset PMU group: %w", err)
	}
	if err := ioctlManchesterPerf(group.leader, manchesterPerfEventIOCEnable, manchesterPerfIOCFlagGroup); err != nil {
		return manchesterPMUSample{}, fmt.Errorf("enable PMU group: %w", err)
	}
	started := time.Now()
	run()
	elapsed := time.Since(started)
	if err := ioctlManchesterPerf(group.leader, manchesterPerfEventIOCDisable, manchesterPerfIOCFlagGroup); err != nil {
		return manchesterPMUSample{}, fmt.Errorf("disable PMU group: %w", err)
	}
	// nr, time_enabled, time_running, followed by two value/ID pairs.
	var encoded [56]byte
	n, err := syscall.Read(group.leader, encoded[:])
	if err != nil {
		return manchesterPMUSample{}, fmt.Errorf("read PMU group: %w", err)
	}
	if n != len(encoded) || binary.LittleEndian.Uint64(encoded[0:8]) != 2 {
		return manchesterPMUSample{}, fmt.Errorf("PMU group read returned %d bytes and %d values", n, binary.LittleEndian.Uint64(encoded[0:8]))
	}
	sample := manchesterPMUSample{
		timeEnabled: binary.LittleEndian.Uint64(encoded[8:16]),
		timeRunning: binary.LittleEndian.Uint64(encoded[16:24]),
		elapsed:     elapsed,
	}
	for offset := 24; offset < len(encoded); offset += 16 {
		value := binary.LittleEndian.Uint64(encoded[offset : offset+8])
		id := binary.LittleEndian.Uint64(encoded[offset+8 : offset+16])
		switch id {
		case group.cyclesID:
			sample.cycles = value
		case group.instructionsID:
			sample.instructions = value
		default:
			return manchesterPMUSample{}, fmt.Errorf("PMU group returned unknown event ID %d", id)
		}
	}
	if sample.cycles == 0 || sample.instructions == 0 {
		return manchesterPMUSample{}, fmt.Errorf("pinned PMU group returned cycles=%d instructions=%d", sample.cycles, sample.instructions)
	}
	if sample.timeEnabled == 0 || sample.timeRunning == 0 {
		return manchesterPMUSample{}, fmt.Errorf("pinned PMU group returned enabled=%d running=%d", sample.timeEnabled, sample.timeRunning)
	}
	return sample, nil
}

func readManchesterPerfEventID(fd int) (uint64, error) {
	var id uint64
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), manchesterPerfEventIOCID, uintptr(unsafe.Pointer(&id)))
	if errno != 0 {
		return 0, errno
	}
	if id == 0 {
		return 0, fmt.Errorf("kernel returned zero event ID")
	}
	return id, nil
}

func ioctlManchesterPerf(fd int, request, value uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, value)
	if errno != 0 {
		return errno
	}
	return nil
}

func manchesterPMUCPU(t *testing.T) int {
	t.Helper()
	value := os.Getenv("RTLAMR_MANCHESTER_CPU")
	if value == "" {
		return manchesterDefaultBenchmarkCPU
	}
	cpu, err := strconv.Atoi(value)
	if err != nil || cpu < 0 || cpu >= manchesterAffinityBytes*8 {
		t.Fatalf("RTLAMR_MANCHESTER_CPU=%q is invalid", value)
	}
	return cpu
}

func pinManchesterThread(cpu int) ([]byte, error) {
	original := make([]byte, manchesterAffinityBytes)
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(original)), uintptr(unsafe.Pointer(&original[0])))
	if errno != 0 {
		return nil, errno
	}
	mask := make([]byte, manchesterAffinityBytes)
	mask[cpu/8] = byte(1 << uint(cpu%8))
	_, _, errno = syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0])))
	if errno != 0 {
		return nil, errno
	}
	return original, nil
}

func restoreManchesterAffinity(mask []byte) error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

func requireManchesterA72R0P3(t *testing.T) string {
	t.Helper()
	midr := readManchesterTextFile("/sys/devices/system/cpu/cpu0/regs/identification/midr_el1")
	if midr != manchesterA72R0P3MIDR {
		t.Fatalf("authoritative Manchester PMU baseline requires Cortex-A72 r0p3 MIDR %s, got %q", manchesterA72R0P3MIDR, midr)
	}
	return midr
}

func readManchesterTextFile(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "unavailable:" + err.Error()
	}
	return strings.TrimSpace(string(contents))
}

func readManchesterScheduler() string {
	tid := syscall.Gettid()
	contents, err := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/sched", tid))
	if err != nil {
		return "unavailable:" + err.Error()
	}
	lines := strings.Split(string(contents), "\n")
	selected := make([]string, 0, 2)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "policy") || strings.HasPrefix(trimmed, "prio") {
			selected = append(selected, trimmed)
		}
	}
	if len(selected) == 0 {
		return "unavailable:no policy/prio fields"
	}
	return strings.Join(selected, ";")
}
