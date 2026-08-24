package r900

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/bemasher/rtlamr/protocol"
)

type power16HistoryFixture []uint16

func (history power16HistoryFixture) Power16Window(idx, length int) []uint16 {
	return history[idx : idx+length]
}

func TestPower16CapabilityAndAttachment(t *testing.T) {
	parser := NewParser(72).(*Parser)
	if !parser.Power16Compatible() {
		t.Fatal("R900 parser did not report Power16 compatibility")
	}

	history := power16HistoryFixture(make([]uint16, 72*4))
	parser.SetPower16History(history)
	if parser.powerHistory == nil {
		t.Fatal("Power16 history was not attached")
	}
	parser.SetPower16History(nil)
	if parser.powerHistory != nil {
		t.Fatal("nil Power16 history did not restore the float path")
	}
}

func TestPower16QuantizerMatchesIndependentOracle(t *testing.T) {
	digest := sha256.New()
	for _, chipLength := range r900IntegerPipelineOracleChipLengths {
		parser := NewParser(chipLength).(*Parser)
		for pattern := 0; pattern < 3; pattern++ {
			for fixture := 0; fixture < 64; fixture++ {
				seed := uint64(0xd4900000 + chipLength*65537 + pattern*257 + fixture)
				iq := r900IntegerPipelineOracleIQ(chipLength*4+7, seed, pattern)
				power, _ := r900IntegerPipelineOraclePowerStreams(iq)
				parser.SetPower16History(power16HistoryFixture(power))

				// Call the ordinary seam, not its private integer helper. A nil
				// Decoder would panic if this accidentally selected float history.
				got := parser.quantizeSignalAt(7)
				want := r900IntegerPipelineOracleDirect(power[7:], chipLength)
				if got != want.symbol {
					t.Fatalf(
						"chip=%d pattern=%d fixture=%d symbol=%d, want %d values=%v tie=%t",
						chipLength, pattern, fixture, got, want.symbol, want.values, want.tie,
					)
				}
				for _, value := range want.values {
					r900IntegerPipelineOracleAppendUint64(digest, uint64(value))
				}
				_, _ = digest.Write([]byte{got})
			}
		}
	}

	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	const wantDigest = "4311176eb4df762aa7ce913f9fc0f9682d8cb73206c0ea5ef660412f1ad371b3"
	if gotDigest != wantDigest {
		t.Fatalf("Power16 R900 correlation digest=%s, want %s", gotDigest, wantDigest)
	}
}

func TestPower16QuantizerStrictTiePrecedence(t *testing.T) {
	testCases := [][3]int32{
		{0, 0, 0},
		{16, 16, 8},
		{-16, -16, -8},
		{8, 16, 16},
		{-8, -16, -16},
		{15, 16, 14},
		{-15, -16, -14},
	}
	for _, values := range testCases {
		want := r900IntegerPipelineOracleQuantize([3]int64{
			int64(values[0]), int64(values[1]), int64(values[2]),
		})
		if got := quantizePower16Symbol(values[0], values[1], values[2]); got != want.symbol {
			t.Fatalf("values=%v symbol=%d, want %d selected=%d tie=%t", values, got, want.symbol, want.selected, want.tie)
		}
	}
}

var _ protocol.Power16History = power16HistoryFixture(nil)
