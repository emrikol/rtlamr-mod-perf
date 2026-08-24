//go:build d3_power_neon && d4_fused_power

package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
)

const (
	d4FusedPMUFixtureVersion = "d4-fused-power-manchester-fixture-v1"
	d4FusedPMUBlockSize      = 8192
	d4FusedPMUChipLength     = 72
	d4FusedPMUHistory        = d4FusedPMUChipLength * 2
	d4FusedPMUWindow         = d4FusedPMUHistory + d4FusedPMUBlockSize
	d4FusedPMUFixtureDigest  = "a79574b1898383b952ed5368b3ceaaabfaeeafe4c330bd047f0995d71cd4a471"
)

type d4FusedPMUFixture struct {
	iq               []byte
	integerWindow    []uint16
	floatWindow      []float64
	integerDecisions []byte
	floatDecisions   []byte
	digest           string
}

func newD4FusedPMUFixture() d4FusedPMUFixture {
	historyIQ := integerPipelineOracleIQ(d4FusedPMUHistory, 0xd4f17e000144, 2)
	iq := integerPipelineOracleIQ(d4FusedPMUBlockSize, 0xd4f17e008192, 0)
	integerHistory, floatHistory := integerPipelineOraclePowerStreams(historyIQ)
	integerPower, floatPower := integerPipelineOraclePowerStreams(iq)
	integerWindow := append(append(make([]uint16, 0, d4FusedPMUWindow), integerHistory...), integerPower...)
	floatWindow := append(append(make([]float64, 0, d4FusedPMUWindow), floatHistory...), floatPower...)
	integerTrace := integerPipelineOracleManchester(integerWindow, d4FusedPMUBlockSize, d4FusedPMUChipLength)
	floatTrace := integerPipelineOracleFloatManchester(floatWindow, d4FusedPMUBlockSize, d4FusedPMUChipLength)

	digest := sha256.New()
	_, _ = digest.Write([]byte(d4FusedPMUFixtureVersion))
	_, _ = digest.Write(historyIQ)
	_, _ = digest.Write(iq)
	var encoded [8]byte
	for _, value := range integerWindow {
		binary.LittleEndian.PutUint16(encoded[:2], value)
		_, _ = digest.Write(encoded[:2])
	}
	for _, value := range floatWindow {
		binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(value))
		_, _ = digest.Write(encoded[:])
	}
	_, _ = digest.Write(integerTrace.decisions)
	_, _ = digest.Write(floatTrace.decisions)
	for _, margin := range integerTrace.margins {
		binary.LittleEndian.PutUint64(encoded[:], uint64(margin))
		_, _ = digest.Write(encoded[:])
	}
	for _, margin := range floatTrace.margins {
		binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(margin))
		_, _ = digest.Write(encoded[:])
	}

	return d4FusedPMUFixture{
		iq:               iq,
		integerWindow:    integerWindow,
		floatWindow:      floatWindow,
		integerDecisions: integerTrace.decisions,
		floatDecisions:   floatTrace.decisions,
		digest:           fmt.Sprintf("%x", digest.Sum(nil)),
	}
}

func TestD4FusedPMUFixtureDigest(t *testing.T) {
	fixture := newD4FusedPMUFixture()
	if fixture.digest != d4FusedPMUFixtureDigest {
		t.Fatalf("D4 PMU fixture digest=%s, want %s", fixture.digest, d4FusedPMUFixtureDigest)
	}
	t.Logf("D4 PMU fixture version=%s digest=%s IQ=%d history=%d decisions=%d",
		d4FusedPMUFixtureVersion, fixture.digest, len(fixture.iq), d4FusedPMUHistory, len(fixture.integerDecisions))
}
