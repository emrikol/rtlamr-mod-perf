//go:build d3_power_neon && d4_power16_fusion && d4_fused_power && d4_power16_complete

package r900

import (
	"testing"

	"github.com/bemasher/rtlamr/protocol"
)

func TestPower16CorrelationDiagnosticsDeterministicCandidates(t *testing.T) {
	floatDecoder := protocol.NewDecoder()
	floatDecoder.RegisterProtocol(NewParser(72))
	floatDecoder.Allocate()
	powerDecoder := protocol.NewPower16AutomaticDecoderValidation(protocol.Power16ValidationReferenceRunner)
	powerDecoder.RegisterProtocol(NewParser(72))
	powerDecoder.Allocate()

	input := make([]byte, floatDecoder.Cfg.BlockSize2)
	state := uint64(0x900d4c0ffee)
	for block := 0; block < 15; block++ {
		for idx := range input {
			state = state*6364136223846793005 + 1442695040888963407
			input[idx] = byte(state >> 56)
		}
		_ = floatDecoder.Decode(input)
		_ = powerDecoder.Decode(input)
	}
	diagnostic := ComparePower16CorrelationsExperiment(
		&floatDecoder,
		&powerDecoder,
		[]int{0, 0, floatDecoder.Cfg.BlockSize, floatDecoder.Cfg.BlockSize + 1},
	)
	if diagnostic.Candidates != 2 || diagnostic.Symbols != PayloadSymbols*2 {
		t.Fatalf("correlation coverage candidates=%d symbols=%d, want 2/%d",
			diagnostic.Candidates, diagnostic.Symbols, PayloadSymbols*2)
	}
	if diagnostic.ForbiddenSymbolMismatches != 0 {
		t.Fatalf("forbidden R900 symbol mismatches=%d", diagnostic.ForbiddenSymbolMismatches)
	}
	const wantSHA256 = "43dd86208b46afcc54a651cca2b142469f17962f39d4e8aa49f534bc157bba9a"
	if diagnostic.CorrelationSHA256 != wantSHA256 {
		t.Fatalf("R900 correlation diagnostic sha256=%s, want %s", diagnostic.CorrelationSHA256, wantSHA256)
	}
	t.Logf("candidates=%d symbols=%d integer-boundaries=%d float-boundaries=%d mismatches=%d boundary-mismatches=%d sha256=%s",
		diagnostic.Candidates,
		diagnostic.Symbols,
		diagnostic.IntegerBoundarySymbols,
		diagnostic.FloatBoundarySymbols,
		diagnostic.SymbolMismatches,
		diagnostic.BoundarySymbolMismatches,
		diagnostic.CorrelationSHA256,
	)
}

func TestPower16QuantizerDiagnosticsSelectedAgainstIndependentOracle(t *testing.T) {
	powerDecoder := protocol.NewPower16AutomaticDecoderValidation(protocol.Power16ValidationReferenceRunner)
	powerDecoder.RegisterProtocol(NewParser(72))
	powerDecoder.Allocate()

	input := make([]byte, powerDecoder.Cfg.BlockSize2)
	state := uint64(0x52335155414e5431)
	for block := 0; block < 15; block++ {
		for index := range input {
			state = state*6364136223846793005 + 1442695040888963407
			input[index] = byte(state >> 56)
		}
		_ = powerDecoder.Decode(input)
	}
	before := Power16QuantizerDispatchStatus()
	diagnostic := CompareSelectedPower16QuantizerExperiment(
		&powerDecoder,
		powerDecoder.Cfg,
		[]int{0, 0, powerDecoder.Cfg.BlockSize, powerDecoder.Cfg.BlockSize + 1},
	)
	after := Power16QuantizerDispatchStatus()
	if diagnostic.Candidates != 2 || diagnostic.Symbols != PayloadSymbols*2 || diagnostic.SymbolMismatches != 0 ||
		diagnostic.SelectedSHA256 != diagnostic.ReferenceSHA256 {
		t.Fatalf("quantizer diagnostic=%+v", diagnostic)
	}
	if after.NativeCalls != before.NativeCalls || after.PortableCalls != before.PortableCalls {
		t.Fatalf("diagnostic changed production counters before=%+v after=%+v", before, after)
	}
	const wantSHA256 = "51349ccb3b6e5de0f942390b86d46d372a914c60205a7901045e373c9a247fd0"
	if diagnostic.SelectedSHA256 != wantSHA256 {
		t.Fatalf("quantizer diagnostic sha256=%s want=%s", diagnostic.SelectedSHA256, wantSHA256)
	}
	t.Logf("selected/reference sha256=%s", diagnostic.SelectedSHA256)
}
