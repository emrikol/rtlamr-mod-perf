package protocol

import (
	"math/rand"
	"testing"
)

func dutyReplayDecoder(t *testing.T) Decoder {
	t.Helper()
	decoder := newPower16AutomaticTestDecoder(power16DecoderTestPlatform())
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	decoder.Allocate()
	return decoder
}

func TestFreshDecoderCollarReconstructsLogicalDecisionHistory(t *testing.T) {
	baseline := dutyReplayDecoder(t)
	rebuilt := dutyReplayDecoder(t)
	random := rand.New(rand.NewSource(49469))
	const startReplay = 31
	warmup := (baseline.Cfg.BufferLength + baseline.Cfg.BlockSize - 1) / baseline.Cfg.BlockSize
	blocks := make([][]byte, 100)
	for idx := range blocks {
		blocks[idx] = make([]byte, baseline.Cfg.BlockSize2)
		if _, err := random.Read(blocks[idx]); err != nil {
			t.Fatal(err)
		}
	}
	for idx, block := range blocks {
		baselineMessages := baseline.Decode(block)
		if idx < startReplay {
			continue
		}
		rebuiltMessages := rebuilt.Decode(block)
		if idx < startReplay+warmup-1 {
			continue
		}
		if len(baselineMessages) != len(rebuiltMessages) {
			t.Fatalf("block %d message count=%d, want %d", idx, len(rebuiltMessages), len(baselineMessages))
		}
		for logical := 0; logical < baseline.Cfg.BufferLength; logical++ {
			if baseline.quantizedAt(logical) != rebuilt.quantizedAt(logical) {
				t.Fatalf("block %d logical decision %d differs after %d warmup blocks", idx, logical, warmup)
			}
		}
	}
}
