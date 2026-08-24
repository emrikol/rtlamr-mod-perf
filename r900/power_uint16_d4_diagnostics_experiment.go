//go:build d3_power_neon && d4_power16_fusion

package r900

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"sort"

	"github.com/bemasher/rtlamr/protocol"
)

// Power16CorrelationDiagnosticsExperiment independently compares all R900
// payload correlations reached by the union of the two candidate-index sets.
// IntegerBoundarySymbols counts a selected zero correlation or an exact tie
// for maximum absolute correlation; only those symbols may legitimately
// differ from the rounded float path.
type Power16CorrelationDiagnosticsExperiment struct {
	Candidates                int
	Symbols                   int
	IntegerBoundarySymbols    int
	FloatBoundarySymbols      int
	SymbolMismatches          int
	BoundarySymbolMismatches  int
	ForbiddenSymbolMismatches int
	CorrelationSHA256         string
}

// Power16QuantizerDiagnosticsExperiment compares the process-selected R900
// Power16 backend with the independent integer oracle without incrementing the
// production dispatch counters. Parser-path counter deltas separately prove
// that the selected backend actually executed during Decode.
type Power16QuantizerDiagnosticsExperiment struct {
	Candidates       int
	Symbols          int
	SymbolMismatches int
	SelectedSHA256   string
	ReferenceSHA256  string
}

func power16AbsExperiment(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

// quantizePower16ValuesExperiment preserves production quantizeSymbol's
// strict-greater argmax and v0, then v1, then v2 tie precedence.
func quantizePower16ValuesExperiment(v0, v1, v2 int32) byte {
	symbol := byte(0)
	selected := v0
	maximum := power16AbsExperiment(v0)
	if candidate := power16AbsExperiment(v1); candidate > maximum {
		maximum = candidate
		selected = v1
		symbol = 1
	}
	if candidate := power16AbsExperiment(v2); candidate > maximum {
		selected = v2
		symbol = 2
	}
	if selected > 0 {
		symbol += 3
	}
	return symbol
}

func power16CorrelationValuesExperiment(history protocol.Power16History, idx, chipLength int) (int32, int32, int32) {
	signal := history.Power16Window(idx, chipLength*4)
	var chip [4]int32
	for segment := range chip {
		for offset := 0; offset < chipLength; offset++ {
			chip[segment] += int32(signal[segment*chipLength+offset])
		}
	}
	return chip[0] + chip[1] - chip[2] - chip[3],
		chip[0] - chip[1] + chip[2] - chip[3],
		chip[0] - chip[1] - chip[2] + chip[3]
}

func floatCorrelationValuesExperiment(decoder *protocol.Decoder, idx, chipLength int) (float64, float64, float64) {
	signal := decoder.SignalWindow(idx, chipLength*4)
	var sum float64
	for offset := 0; offset < chipLength; offset++ {
		sum += signal[offset]
	}
	c1 := sum * 2
	for offset := chipLength; offset < chipLength*2; offset++ {
		sum += signal[offset]
	}
	c2 := sum * 2
	for offset := chipLength * 2; offset < chipLength*3; offset++ {
		sum += signal[offset]
	}
	c3 := sum * 2
	for offset := chipLength * 3; offset < chipLength*4; offset++ {
		sum += signal[offset]
	}
	c4 := sum
	return c2 - c4, c1 - c2 + c3 - c4, c1 - c3 + c4
}

func power16IntegerBoundaryExperiment(v0, v1, v2 int32) bool {
	values := [...]int32{v0, v1, v2}
	maximum := int32(-1)
	maximumCount := 0
	selected := int32(0)
	for _, value := range values {
		absolute := power16AbsExperiment(value)
		if absolute > maximum {
			maximum = absolute
			maximumCount = 1
			selected = value
		} else if absolute == maximum {
			maximumCount++
		}
	}
	return maximumCount > 1 || selected == 0
}

func floatBoundaryExperiment(v0, v1, v2 float64) bool {
	values := [...]float64{v0, v1, v2}
	maximum := -1.0
	maximumCount := 0
	selected := 0.0
	for _, value := range values {
		absolute := math.Abs(value)
		if absolute > maximum {
			maximum = absolute
			maximumCount = 1
			selected = value
		} else if absolute == maximum {
			maximumCount++
		}
	}
	return maximumCount > 1 || selected == 0
}

// ComparePower16CorrelationsExperiment applies the production R900 payload
// geometry and operation order, but computes values independently from both
// parser implementations. Indices beyond BlockSize are excluded just as Parse
// excludes them. Repeated indices are counted once.
func ComparePower16CorrelationsExperiment(
	floatDecoder *protocol.Decoder,
	powerHistory protocol.Power16History,
	indices []int,
) Power16CorrelationDiagnosticsExperiment {
	var result Power16CorrelationDiagnosticsExperiment
	chipLength := floatDecoder.Cfg.ChipLength
	preambleLength := floatDecoder.Cfg.PreambleLength
	symbolLength := floatDecoder.Cfg.SymbolLength
	ordered := append([]int(nil), indices...)
	sort.Ints(ordered)
	digest := sha256.New()
	var encoded [8]byte
	previous := -1
	for _, candidate := range ordered {
		if candidate == previous || candidate > floatDecoder.Cfg.BlockSize {
			continue
		}
		previous = candidate
		result.Candidates++
		payloadIdx := candidate + preambleLength - symbolLength
		for symbol := 0; symbol < PayloadSymbols; symbol++ {
			idx := payloadIdx + symbol*chipLength*4
			i0, i1, i2 := power16CorrelationValuesExperiment(powerHistory, idx, chipLength)
			f0, f1, f2 := floatCorrelationValuesExperiment(floatDecoder, idx, chipLength)
			integerBoundary := power16IntegerBoundaryExperiment(i0, i1, i2)
			floatBoundary := floatBoundaryExperiment(f0, f1, f2)
			integerSymbol := quantizePower16ValuesExperiment(i0, i1, i2)
			floatSymbol := quantizeSymbol(f0, f1, f2)
			result.Symbols++
			if integerBoundary {
				result.IntegerBoundarySymbols++
			}
			if floatBoundary {
				result.FloatBoundarySymbols++
			}
			for _, value := range [...]uint64{
				uint64(uint32(i0)), uint64(uint32(i1)), uint64(uint32(i2)),
				math.Float64bits(f0), math.Float64bits(f1), math.Float64bits(f2),
			} {
				binary.LittleEndian.PutUint64(encoded[:], value)
				_, _ = digest.Write(encoded[:])
			}
			_, _ = digest.Write([]byte{integerSymbol, floatSymbol})
			if integerSymbol != floatSymbol {
				result.SymbolMismatches++
				if integerBoundary {
					result.BoundarySymbolMismatches++
				} else {
					result.ForbiddenSymbolMismatches++
				}
			}
		}
	}
	result.CorrelationSHA256 = hex.EncodeToString(digest.Sum(nil))
	return result
}

// CompareSelectedPower16QuantizerExperiment applies the exact production R900
// payload geometry to the selected candidate indices. It calls the selected
// raw backend directly (or the exact Go fallback) so diagnostic work cannot
// contaminate production NativeCalls/PortableCalls evidence.
func CompareSelectedPower16QuantizerExperiment(
	powerHistory protocol.Power16History,
	cfg protocol.PacketConfig,
	indices []int,
) Power16QuantizerDiagnosticsExperiment {
	var result Power16QuantizerDiagnosticsExperiment
	ordered := append([]int(nil), indices...)
	sort.Ints(ordered)
	selectedDigest := sha256.New()
	referenceDigest := sha256.New()
	platform := power16R900CurrentPlatform()
	previous := -1
	for _, candidate := range ordered {
		if candidate == previous || candidate > cfg.BlockSize {
			continue
		}
		previous = candidate
		result.Candidates++
		payloadIdx := candidate + cfg.PreambleLength - cfg.SymbolLength
		for symbol := 0; symbol < PayloadSymbols; symbol++ {
			idx := payloadIdx + symbol*cfg.ChipLength*4
			signal := powerHistory.Power16Window(idx, cfg.ChipLength*4)
			selected := quantizePower16WindowGo(signal, cfg.ChipLength)
			if cfg.ChipLength == power16R900SIMDChipLength && len(signal) >= power16R900SIMDChipLength*4 && platform.nativeAvailable {
				selected = platform.run(signal)
			}
			i0, i1, i2 := power16CorrelationValuesExperiment(powerHistory, idx, cfg.ChipLength)
			reference := quantizePower16ValuesExperiment(i0, i1, i2)
			result.Symbols++
			_, _ = selectedDigest.Write([]byte{selected})
			_, _ = referenceDigest.Write([]byte{reference})
			if selected != reference {
				result.SymbolMismatches++
			}
		}
	}
	result.SelectedSHA256 = hex.EncodeToString(selectedDigest.Sum(nil))
	result.ReferenceSHA256 = hex.EncodeToString(referenceDigest.Sum(nil))
	return result
}
