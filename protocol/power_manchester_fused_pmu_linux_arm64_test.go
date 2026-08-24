//go:build d3_power_neon && d4_fused_power && linux && arm64 && gc && !purego && !race

package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"testing"
	"unsafe"
)

const (
	d4FusedPMUDefaultQuartets = 6
	d4FusedPMUMaxQuartets     = 32
	d4FusedPMUMaxCalls        = 1 << 30
)

type d4FusedPMUArm struct {
	name      string
	calls     int
	inputMod  uintptr
	outputMod uintptr
	digest    string
	run       func(int)
	consume   func(int)
}

type d4FusedPMUObservation struct {
	arm    byte
	sample manchesterPMUSample
}

type d4FusedPMUQuartet struct {
	observations [4]d4FusedPMUObservation
}

// TestD4FusedPowerManchesterPMUAB is an opt-in, fixed-count same-binary A72
// harness. It first measures A/A with independent output buffers, then A/B.
// Odd quartets use ABBA and even quartets use BAAB. The surrounding controller
// owns service isolation, fixed frequency, telemetry, and restoration.
func TestD4FusedPowerManchesterPMUAB(t *testing.T) {
	if os.Getenv("RTLAMR_D4_FUSED_PMU") != "1" {
		t.Skip("set RTLAMR_D4_FUSED_PMU=1 to run the opt-in D4 A72 PMU comparison")
	}
	if !magnitudeLUTA72Available() || !filterManchesterA72Available() {
		t.Fatal("deployed exact-float A72 magnitude/Manchester leaves are unavailable")
	}
	fixture := newD4FusedPMUFixture()
	if fixture.digest != d4FusedPMUFixtureDigest {
		t.Fatalf("fixture digest=%s, want %s", fixture.digest, d4FusedPMUFixtureDigest)
	}

	selectorA := os.Getenv("RTLAMR_D4_FUSED_PMU_A")
	selectorB := os.Getenv("RTLAMR_D4_FUSED_PMU_B")
	armA := newD4FusedPMUArm(t, selectorA, "RTLAMR_D4_FUSED_PMU_A_CALLS", fixture)
	armAA := newD4FusedPMUArm(t, selectorA, "RTLAMR_D4_FUSED_PMU_A_CALLS", fixture)
	armB := newD4FusedPMUArm(t, selectorB, "RTLAMR_D4_FUSED_PMU_B_CALLS", fixture)
	for _, arm := range []*d4FusedPMUArm{&armA, &armAA, &armB} {
		if allocs := testing.AllocsPerRun(10, func() { arm.run(1) }); allocs != 0 {
			t.Fatalf("arm %s allocated %.3f objects per complete call", arm.name, allocs)
		}
	}

	cpu := d4FusedPMUCPUEnv(t)
	quartets := d4FusedPMUPositiveEnv(t, "RTLAMR_D4_FUSED_PMU_QUARTETS", d4FusedPMUDefaultQuartets, d4FusedPMUMaxQuartets)
	midr := requireD4FusedPMUA72R0P3(t, cpu)
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
		t.Fatalf("open pinned user cycles/instructions PMU group: %v", err)
	}
	defer group.close()

	t.Logf("D4_FUSED_PMU_CONFIG fixture_version=%s fixture=%s IQ_bytes=%d history=%d block=%d chip=%d A=%s A_calls=%d A_digest=%s A_input_mod64=%d A_output_mod64=%d AA_clone_digest=%s AA_clone_input_mod64=%d AA_clone_output_mod64=%d B=%s B_calls=%d B_digest=%s B_input_mod64=%d B_output_mod64=%d cpu=%d quartets=%d phases=AA,AB order=odd-ABBA/even-BAAB go=%s runtime=%q midr=%s original_affinity=%x freq_khz=%q temp_mc=%q scheduler=%q",
		d4FusedPMUFixtureVersion, fixture.digest, len(fixture.iq), d4FusedPMUHistory, d4FusedPMUBlockSize, d4FusedPMUChipLength,
		armA.name, armA.calls, armA.digest, armA.inputMod, armA.outputMod,
		armAA.digest, armAA.inputMod, armAA.outputMod,
		armB.name, armB.calls, armB.digest, armB.inputMod, armB.outputMod,
		cpu, quartets, runtime.GOOS+"/"+runtime.GOARCH, runtime.Version(), midr, restoreAffinity,
		readManchesterTextFile(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_cur_freq", cpu)),
		readManchesterTextFile("/sys/class/thermal/thermal_zone0/temp"), readManchesterScheduler())

	runD4FusedPMUPhase(t, group, "AA", armA, armAA, quartets)
	runD4FusedPMUPhase(t, group, "AB", armA, armB, quartets)
}

func newD4FusedPMUArm(t *testing.T, name, callsEnv string, fixture d4FusedPMUFixture) d4FusedPMUArm {
	t.Helper()
	defaultCalls := 0
	switch name {
	case "float", "materialized":
		defaultCalls = 1 << 18
	case "fused":
		defaultCalls = 1 << 20
	default:
		t.Fatalf("invalid D4 PMU selector %q; want float, materialized, or fused", name)
	}
	calls := d4FusedPMUPositiveEnv(t, callsEnv, defaultCalls, d4FusedPMUMaxCalls)

	if name == "float" {
		signal := make([]float64, d4FusedPMUWindow)
		copy(signal[:d4FusedPMUHistory], fixture.floatWindow[:d4FusedPMUHistory])
		decisions := make([]byte, d4FusedPMUBlockSize)
		lut := NewMagLUT()
		run := func(iterations int) {
			for iteration := 0; iteration < iterations; iteration++ {
				magnitudeLUTFloat64A72(
					unsafe.Pointer(&signal[d4FusedPMUHistory]), unsafe.Pointer(&fixture.iq[0]),
					unsafe.Pointer(&lut[0]), d4FusedPMUBlockSize,
				)
				filterManchesterA72TBXSSRABalanced(unsafe.Pointer(&decisions[0]), unsafe.Pointer(&signal[0]))
			}
		}
		run(1)
		for idx, want := range fixture.floatWindow {
			if math.Float64bits(signal[idx]) != math.Float64bits(want) {
				t.Fatalf("float preflight signal[%d]=%016x, want %016x", idx, math.Float64bits(signal[idx]), math.Float64bits(want))
			}
		}
		for idx, want := range fixture.floatDecisions {
			if decisions[idx] != want {
				t.Fatalf("float preflight decision[%d]=%d, want %d", idx, decisions[idx], want)
			}
		}
		digest := d4FusedPMUFloatDigest(signal, decisions)
		return d4FusedPMUArm{
			name: name, calls: calls, inputMod: uintptr(unsafe.Pointer(&fixture.iq[0])) % 64,
			outputMod: uintptr(unsafe.Pointer(&signal[d4FusedPMUHistory])) % 64, digest: digest, run: run,
			consume: func(iterations int) {
				powerUint16FloatSink = math.Float64bits(signal[(iterations+d4FusedPMUWindow-1)%d4FusedPMUWindow])
				manchesterDecisionSink = decisions[(iterations+d4FusedPMUBlockSize-1)%d4FusedPMUBlockSize]
				runtime.KeepAlive(signal)
				runtime.KeepAlive(decisions)
				runtime.KeepAlive(fixture.iq)
				runtime.KeepAlive(lut)
			},
		}
	}

	window := make([]uint16, d4FusedPMUWindow)
	copy(window[:d4FusedPMUHistory], fixture.integerWindow[:d4FusedPMUHistory])
	decisions := make([]byte, d4FusedPMUBlockSize)
	run := func(iterations int) {
		for iteration := 0; iteration < iterations; iteration++ {
			if name == "materialized" {
				powerIQUint16NEONU8Mul32Leaf(
					unsafe.Pointer(&window[d4FusedPMUHistory]), unsafe.Pointer(&fixture.iq[0]), d4FusedPMUBlockSize,
				)
				d4MaterializedIntegerManchesterControl(decisions, window)
			} else {
				fusedPowerManchesterU8Mul32A72(
					unsafe.Pointer(&decisions[0]), unsafe.Pointer(&window[0]), unsafe.Pointer(&fixture.iq[0]),
				)
			}
		}
	}
	run(1)
	for idx, want := range fixture.integerWindow {
		if window[idx] != want {
			t.Fatalf("%s preflight power[%d]=%d, want %d", name, idx, window[idx], want)
		}
	}
	for idx, want := range fixture.integerDecisions {
		if decisions[idx] != want {
			t.Fatalf("%s preflight decision[%d]=%d, want %d", name, idx, decisions[idx], want)
		}
	}
	digest := d4FusedPMUIntegerDigest(window, decisions)
	return d4FusedPMUArm{
		name: name, calls: calls, inputMod: uintptr(unsafe.Pointer(&fixture.iq[0])) % 64,
		outputMod: uintptr(unsafe.Pointer(&window[d4FusedPMUHistory])) % 64, digest: digest, run: run,
		consume: func(iterations int) {
			powerUint16ValueSink = window[(iterations+d4FusedPMUWindow-1)%d4FusedPMUWindow]
			manchesterDecisionSink = decisions[(iterations+d4FusedPMUBlockSize-1)%d4FusedPMUBlockSize]
			runtime.KeepAlive(window)
			runtime.KeepAlive(decisions)
			runtime.KeepAlive(fixture.iq)
		},
	}
}

//go:noinline
func d4MaterializedIntegerManchesterControl(output []byte, power []uint16) {
	var margin int32
	for idx := 0; idx < d4FusedPMUChipLength; idx++ {
		margin += int32(power[idx])
		margin -= int32(power[idx+d4FusedPMUChipLength])
	}
	for idx := range output {
		if margin >= 0 {
			output[idx] = 1
		} else {
			output[idx] = 0
		}
		margin += int32(power[idx+d4FusedPMUChipLength])*2 - int32(power[idx]) - int32(power[idx+d4FusedPMUChipLength*2])
	}
}

func d4FusedPMUIntegerDigest(window []uint16, decisions []byte) string {
	digest := sha256.New()
	var encoded [2]byte
	for _, value := range window {
		binary.LittleEndian.PutUint16(encoded[:], value)
		_, _ = digest.Write(encoded[:])
	}
	_, _ = digest.Write(decisions)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func d4FusedPMUFloatDigest(window []float64, decisions []byte) string {
	digest := sha256.New()
	var encoded [8]byte
	for _, value := range window {
		binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(value))
		_, _ = digest.Write(encoded[:])
	}
	_, _ = digest.Write(decisions)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func runD4FusedPMUPhase(t *testing.T, group *manchesterPMUGroup, phase string, armA, armB d4FusedPMUArm, quartets int) {
	t.Helper()
	warmA := d4FusedPMUWarmupCalls(armA.calls)
	warmB := d4FusedPMUWarmupCalls(armB.calls)
	armA.run(warmA)
	armA.consume(warmA)
	armB.run(warmB)
	armB.consume(warmB)

	results := make([]d4FusedPMUQuartet, quartets)
	for quartet := range results {
		order := [4]byte{'A', 'B', 'B', 'A'}
		orderName := "ABBA"
		if quartet&1 != 0 {
			order = [4]byte{'B', 'A', 'A', 'B'}
			orderName = "BAAB"
		}
		for position, label := range order {
			arm := &armA
			if label == 'B' {
				arm = &armB
			}
			sample, err := group.measure(func() { arm.run(arm.calls) })
			if err != nil {
				t.Fatalf("phase %s quartet %d position %d arm %c/%s: %v", phase, quartet+1, position+1, label, arm.name, err)
			}
			if sample.timeEnabled != sample.timeRunning {
				t.Fatalf("phase %s quartet %d position %d arm %c/%s: PMU multiplexed enabled=%d running=%d",
					phase, quartet+1, position+1, label, arm.name, sample.timeEnabled, sample.timeRunning)
			}
			arm.consume(arm.calls)
			results[quartet].observations[position] = d4FusedPMUObservation{arm: label, sample: sample}
		}
		t.Logf("D4_FUSED_PMU_RAW phase=%s quartet=%d order=%s %s",
			phase, quartet+1, orderName, formatD4FusedPMUQuartet(results[quartet], armA, armB))
	}
	reportD4FusedPMUSummary(t, phase, results, armA, armB)
}

func formatD4FusedPMUQuartet(quartet d4FusedPMUQuartet, armA, armB d4FusedPMUArm) string {
	formatted := ""
	for position, observation := range quartet.observations {
		arm := armA
		if observation.arm == 'B' {
			arm = armB
		}
		if position != 0 {
			formatted += " "
		}
		formatted += fmt.Sprintf("P%d=%c/%s cycles=%.3f instructions=%.3f enabled=%d running=%d wall_ns=%d",
			position+1, observation.arm, arm.name,
			float64(observation.sample.cycles)/float64(arm.calls),
			float64(observation.sample.instructions)/float64(arm.calls),
			observation.sample.timeEnabled, observation.sample.timeRunning, observation.sample.elapsed.Nanoseconds())
	}
	return formatted
}

func reportD4FusedPMUSummary(t *testing.T, phase string, quartets []d4FusedPMUQuartet, armA, armB d4FusedPMUArm) {
	t.Helper()
	effects := make([]float64, len(quartets))
	var aCycles, bCycles, aInstructions, bInstructions float64
	for quartetIdx, quartet := range quartets {
		var aCycleSum, bCycleSum, aInstructionSum, bInstructionSum float64
		var aCount, bCount int
		for _, observation := range quartet.observations {
			if observation.arm == 'A' {
				aCycleSum += float64(observation.sample.cycles) / float64(armA.calls)
				aInstructionSum += float64(observation.sample.instructions) / float64(armA.calls)
				aCount++
			} else {
				bCycleSum += float64(observation.sample.cycles) / float64(armB.calls)
				bInstructionSum += float64(observation.sample.instructions) / float64(armB.calls)
				bCount++
			}
		}
		if aCount != 2 || bCount != 2 {
			t.Fatalf("phase %s quartet %d has A=%d B=%d observations", phase, quartetIdx+1, aCount, bCount)
		}
		aCycleMean := aCycleSum / float64(aCount)
		bCycleMean := bCycleSum / float64(bCount)
		aCycles += aCycleMean
		bCycles += bCycleMean
		aInstructions += aInstructionSum / float64(aCount)
		bInstructions += bInstructionSum / float64(bCount)
		effects[quartetIdx] = 100 * (aCycleMean - bCycleMean) / aCycleMean
	}
	n := float64(len(quartets))
	aCycles /= n
	bCycles /= n
	aInstructions /= n
	bInstructions /= n
	effectMean, effectSD := meanAndSampleSDManchester(effects)
	tCritical, ok := studentT95Manchester(len(effects) - 1)
	if !ok {
		t.Fatalf("phase %s: unsupported Student-t degrees of freedom %d", phase, len(effects)-1)
	}
	halfWidth := tCritical * effectSD / math.Sqrt(n)
	low := effectMean - halfWidth
	high := effectMean + halfWidth
	noiseFloor := math.Max(math.Abs(low), math.Abs(high))
	t.Logf("D4_FUSED_PMU_SUMMARY phase=%s A=%s A_calls=%d B=%s B_calls=%d quartets=%d A_cycles=%.3f B_cycles=%.3f A_instructions=%.3f B_instructions=%.3f paired_effect=%.6f%% sample_sd=%.6f ci95=[%.6f,%.6f] conservative_noise_floor=%.6f%%",
		phase, armA.name, armA.calls, armB.name, armB.calls, len(quartets), aCycles, bCycles,
		aInstructions, bInstructions, effectMean, effectSD, low, high, noiseFloor)
}

func d4FusedPMUPositiveEnv(t *testing.T, name string, defaultValue, maximum int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || parsed > maximum {
		t.Fatalf("%s=%q is invalid; want an integer in [1,%d]", name, value, maximum)
	}
	return parsed
}

func d4FusedPMUCPUEnv(t *testing.T) int {
	t.Helper()
	value := os.Getenv("RTLAMR_D4_FUSED_PMU_CPU")
	if value == "" {
		return manchesterDefaultBenchmarkCPU
	}
	cpu, err := strconv.Atoi(value)
	if err != nil || cpu < 0 || cpu >= manchesterAffinityBytes*8 {
		t.Fatalf("RTLAMR_D4_FUSED_PMU_CPU=%q is invalid", value)
	}
	return cpu
}

func requireD4FusedPMUA72R0P3(t *testing.T, cpu int) string {
	t.Helper()
	path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/regs/identification/midr_el1", cpu)
	midr := readManchesterTextFile(path)
	if midr != manchesterA72R0P3MIDR {
		t.Fatalf("authoritative D4 PMU comparison requires Cortex-A72 r0p3 MIDR %s on CPU %d, got %q",
			manchesterA72R0P3MIDR, cpu, midr)
	}
	return midr
}

func d4FusedPMUWarmupCalls(calls int) int {
	warmup := calls / 64
	if warmup > 1<<16 {
		warmup = 1 << 16
	}
	if warmup < 1 {
		warmup = 1
	}
	return warmup
}
