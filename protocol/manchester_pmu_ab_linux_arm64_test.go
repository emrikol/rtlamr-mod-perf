//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
)

const (
	manchesterPMUABScreenCalls    = 262144
	manchesterPMUABScreenQuartets = 6
)

type manchesterPMUABQuartet struct {
	baseline  [2]manchesterPMUSample
	candidate [2]manchesterPMUSample
}

// TestManchesterPMUABScreen compares the literal Go fallback and the single
// production A72 leaf through the same wrapper in one binary, changing only
// the already-tested availability flag between arms. This preserves dispatch
// and fixture costs and avoids cross-process PMU contamination.
func TestManchesterPMUABScreen(t *testing.T) {
	if os.Getenv("RTLAMR_MANCHESTER_PMU_AB") != "1" {
		t.Skip("set RTLAMR_MANCHESTER_PMU_AB=1 to run the opt-in Go-vs-production A72 screen")
	}
	if !filterManchesterA72Enabled {
		t.Fatal("A72 Manchester leaf did not pass its CPU gate and startup self-test")
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
	run := func(calls int, candidate bool) {
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

	originalEnabled := filterManchesterA72Enabled
	defer func() { filterManchesterA72Enabled = originalEnabled }()
	measure := func(enabled bool) manchesterPMUSample {
		filterManchesterA72Enabled = enabled
		run(manchesterPMUWarmupCalls, enabled)
		sample, err := group.measure(func() { run(manchesterPMUABScreenCalls, enabled) })
		if err != nil {
			t.Fatalf("measure enabled=%v: %v", enabled, err)
		}
		return sample
	}

	t.Logf("MANCHESTER_PMU_AB_CONFIG fixture=%s cpu=%d calls=%d quartets=%d mode=go-vs-production go=%s runtime=%q midr=%s original_affinity=%x order=alternating-ABBA-BAAB",
		fixture.Digest, cpu, manchesterPMUABScreenCalls, manchesterPMUABScreenQuartets,
		runtime.GOOS+"/"+runtime.GOARCH, runtime.Version(), midr, restoreAffinity)
	quartets := make([]manchesterPMUABQuartet, manchesterPMUABScreenQuartets)
	for quartet := range quartets {
		order := [4]bool{false, true, true, false}
		orderName := "ABBA"
		if quartet%2 == 1 {
			order = [4]bool{true, false, false, true}
			orderName = "BAAB"
		}
		baselineIndex, candidateIndex := 0, 0
		for position, enabled := range order {
			sample := measure(enabled)
			label := "A"
			if enabled {
				quartets[quartet].candidate[candidateIndex] = sample
				candidateIndex++
				label = "B"
			} else {
				quartets[quartet].baseline[baselineIndex] = sample
				baselineIndex++
			}
			t.Logf("MANCHESTER_PMU_AB_RAW quartet=%d order=%s position=%d arm=%s cycles=%.3f instructions=%.3f enabled=%d running=%d wall_ns=%d",
				quartet+1, orderName, position+1, label,
				float64(sample.cycles)/manchesterPMUABScreenCalls,
				float64(sample.instructions)/manchesterPMUABScreenCalls,
				sample.timeEnabled, sample.timeRunning, sample.elapsed.Nanoseconds())
		}
	}
	reportManchesterPMUAB(t, quartets)
}

func reportManchesterPMUAB(t *testing.T, quartets []manchesterPMUABQuartet) {
	t.Helper()
	effects := make([]float64, len(quartets))
	var baselineCycles, candidateCycles, baselineInstructions, candidateInstructions float64
	for idx, quartet := range quartets {
		baseCycles := float64(quartet.baseline[0].cycles+quartet.baseline[1].cycles) / (2 * manchesterPMUABScreenCalls)
		candCycles := float64(quartet.candidate[0].cycles+quartet.candidate[1].cycles) / (2 * manchesterPMUABScreenCalls)
		baseInstructions := float64(quartet.baseline[0].instructions+quartet.baseline[1].instructions) / (2 * manchesterPMUABScreenCalls)
		candInstructions := float64(quartet.candidate[0].instructions+quartet.candidate[1].instructions) / (2 * manchesterPMUABScreenCalls)
		baselineCycles += baseCycles
		candidateCycles += candCycles
		baselineInstructions += baseInstructions
		candidateInstructions += candInstructions
		effects[idx] = 100 * (baseCycles - candCycles) / baseCycles
	}
	n := float64(len(quartets))
	baselineCycles /= n
	candidateCycles /= n
	baselineInstructions /= n
	candidateInstructions /= n
	effectMean, effectSD := meanAndSampleSDManchester(effects)
	tCritical, ok := studentT95Manchester(len(effects) - 1)
	if !ok {
		t.Fatalf("unsupported Student-t degrees of freedom %d", len(effects)-1)
	}
	halfWidth := tCritical * effectSD / math.Sqrt(n)
	t.Logf("MANCHESTER_PMU_AB_SUMMARY calls=%d quartets=%d baseline_cycles=%.3f candidate_cycles=%.3f baseline_instructions=%.3f candidate_instructions=%.3f paired_improvement=%.6f%% sample_sd=%.6f ci95=[%.6f,%.6f]",
		manchesterPMUABScreenCalls, len(quartets), baselineCycles, candidateCycles,
		baselineInstructions, candidateInstructions, effectMean, effectSD,
		effectMean-halfWidth, effectMean+halfWidth)
}
