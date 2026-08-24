//go:build d3_power_neon && d4_power16_fusion && d4_fused_power && d4_power16_complete

package protocol

import "testing"

func TestPower16RollingIntegerMarginsExhaustiveValues(t *testing.T) {
	for value := 0; value <= 1<<16-1; value++ {
		power := []uint16{uint16(value), uint16(1<<16 - 1 - value), uint16(value ^ 0x5a5a)}
		state, err := newPower16IntegerMarginStateExperiment(power, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := state.margin(), power16DirectIntegerMarginExperiment(power, 0, 1); got != want {
			t.Fatalf("value=%d rolling margin=%d, direct=%d", value, got, want)
		}
		state.advance()
	}
}

func TestPower16RollingIntegerMarginsProperty(t *testing.T) {
	outputLengths := [...]int{0, 1, 2, 7, 31, 32, 33, 257, 8192}
	for _, chipLength := range integerPipelineOracleChipLengths {
		for _, outputLength := range outputLengths {
			power := make([]uint16, outputLength+chipLength*2)
			stateValue := uint64(chipLength)<<32 | uint64(outputLength+1)
			for idx := range power {
				stateValue = stateValue*6364136223846793005 + 1442695040888963407
				// Exercise the complete exact-Power16 value domain, including
				// both bounds, without relying on the rolling implementation.
				switch idx % 17 {
				case 0:
					power[idx] = 0
				case 1:
					power[idx] = 65025
				default:
					power[idx] = uint16(stateValue % 65026)
				}
			}
			rolling, err := newPower16IntegerMarginStateExperiment(power, outputLength, chipLength)
			if err != nil {
				t.Fatalf("chip=%d output=%d: %v", chipLength, outputLength, err)
			}
			for output := 0; output < outputLength; output++ {
				got := rolling.margin()
				want := power16DirectIntegerMarginExperiment(power, output, chipLength)
				if got != want {
					t.Fatalf("chip=%d output-length=%d at=%d rolling=%d direct=%d", chipLength, outputLength, output, got, want)
				}
				rolling.advance()
			}
		}
	}
}

func TestPower16RollingIntegerMarginsRejectInvalidGeometry(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		power        []uint16
		outputLength int
		chipLength   int
	}{
		{name: "negative-output", outputLength: -1, chipLength: 1},
		{name: "zero-chip", outputLength: 0, chipLength: 0},
		{name: "short", power: make([]uint16, 3), outputLength: 2, chipLength: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := newPower16IntegerMarginStateExperiment(testCase.power, testCase.outputLength, testCase.chipLength); err == nil {
				t.Fatal("invalid rolling-margin geometry was accepted")
			}
		})
	}
}

func power16D4SetLogicalDecisionExperiment(decoder *Decoder, logical int, value byte) {
	physical := decoder.quantizedStart + logical
	if physical >= len(decoder.Quantized) {
		physical -= len(decoder.Quantized)
	}
	decoder.Quantized[physical] = value
}

func power16D4WritePreambleExperiment(decoder *Decoder, preamble string, start int) {
	for idx, value := range []byte(preamble) {
		power16D4SetLogicalDecisionExperiment(decoder, start+idx*decoder.Cfg.SymbolLength, value-'0')
	}
}

func TestPower16DecodedBlockDiagnosticsCountCandidateDifference(t *testing.T) {
	floatParser := newPower16DecoderTestParser(72, true, 72*4)
	floatDecoder := NewPower16FloatDecoderValidation()
	floatDecoder.RegisterProtocol(floatParser)
	floatDecoder.Allocate()
	powerParser := newPower16DecoderTestParser(72, true, 72*4)
	powerDecoder := NewPower16AutomaticDecoderValidation(Power16ValidationReferenceRunner)
	powerDecoder.RegisterProtocol(powerParser)
	powerDecoder.Allocate()
	input := integerPipelineOracleIQ(floatDecoder.Cfg.BlockSize, 0xd4cadd1f, 0)
	_ = floatDecoder.Decode(input)
	_ = powerDecoder.Decode(input)

	state := uint64(0xcad1da7a)
	for logical := range floatDecoder.Quantized {
		state = state*6364136223846793005 + 1442695040888963407
		value := byte(state >> 63)
		power16D4SetLogicalDecisionExperiment(&floatDecoder, logical, value)
		power16D4SetLogicalDecisionExperiment(&powerDecoder, logical, value)
	}
	const commonStart = 100
	const powerOnlyStart = 101
	power16D4WritePreambleExperiment(&floatDecoder, fixedR900PreambleASCII, commonStart)
	power16D4WritePreambleExperiment(&powerDecoder, fixedR900PreambleASCII, commonStart)
	power16D4WritePreambleExperiment(&powerDecoder, fixedR900PreambleASCII, powerOnlyStart)
	packQuantizedRing(floatDecoder.packed, floatDecoder.Quantized, floatDecoder.quantizedStart)
	packQuantizedRing(powerDecoder.packed, powerDecoder.Quantized, powerDecoder.quantizedStart)

	diagnostic, err := ComparePower16DecodedBlockExperiment(&floatDecoder, &powerDecoder)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.FloatOracleMismatches != 0 || diagnostic.IntegerOracleMismatches != 0 || diagnostic.ForbiddenDecisionMismatches != 0 {
		t.Fatalf("decision oracle counts float=%d power16=%d forbidden=%d",
			diagnostic.FloatOracleMismatches,
			diagnostic.IntegerOracleMismatches,
			diagnostic.ForbiddenDecisionMismatches,
		)
	}
	if diagnostic.FloatCandidates == 0 || diagnostic.Power16Candidates == 0 {
		t.Fatalf("candidate fixture is vacuous: float=%d power16=%d", diagnostic.FloatCandidates, diagnostic.Power16Candidates)
	}
	if diagnostic.Power16OnlyCandidates == 0 || diagnostic.CandidateMismatches == 0 {
		t.Fatalf("candidate difference not observed: float-only=%d power16-only=%d mismatches=%d",
			diagnostic.FloatOnlyCandidates,
			diagnostic.Power16OnlyCandidates,
			diagnostic.CandidateMismatches,
		)
	}
	if diagnostic.FloatCandidateSHA256 == diagnostic.Power16CandidateSHA256 {
		t.Fatal("different candidate snapshots have identical diagnostic digest")
	}
}

func TestPower16DecodedBlockDiagnosticsRejectNativePackedDifference(t *testing.T) {
	floatParser := newPower16DecoderTestParser(72, true, 72*4)
	floatDecoder := NewPower16FloatDecoderValidation()
	floatDecoder.RegisterProtocol(floatParser)
	floatDecoder.Allocate()

	packedPlatform := power16DecoderTestPlatform()
	packedPlatform.implementation = "test-packed-power16"
	packedPlatform.runPacked = func(decisions, packed []byte, window []uint16, input []byte) bool {
		if !power16ReferenceBlock(decisions, window, input) {
			return false
		}
		s6PackOracle(packed, decisions)
		return true
	}
	packedParser := newPower16DecoderTestParser(72, true, 72*4)
	packedDecoder := newPower16AutomaticTestDecoder(packedPlatform)
	packedDecoder.RegisterProtocol(packedParser)
	packedDecoder.Allocate()

	input := integerPipelineOracleIQ(floatDecoder.Cfg.BlockSize, 0x56a6d1a6, 2)
	_ = floatDecoder.Decode(input)
	_ = packedDecoder.Decode(input)
	diagnostic, err := ComparePower16DecodedBlockExperiment(&floatDecoder, &packedDecoder)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.PackedSearchBytes != len(packedDecoder.packed) || diagnostic.PackedSearchBytes == 0 ||
		diagnostic.PackedMismatches != 0 || diagnostic.Power16PackedSHA256 == "" ||
		diagnostic.Power16PackedSHA256 != diagnostic.ReferencePackedSHA256 {
		t.Fatalf("unexpected packed diagnostic: %+v", diagnostic)
	}

	// Corrupt only the native packed view. Quantized, filter decisions, and
	// Power16 history remain exact, so a diagnostic that called public Search
	// would silently repair this byte and miss the defect.
	packedDecoder.packed[0] ^= 0x01
	if _, err := ComparePower16DecodedBlockExperiment(&floatDecoder, &packedDecoder); err == nil {
		t.Fatal("native packed corruption was overwritten or accepted")
	}
}
