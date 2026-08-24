//go:build d3_power_neon && d4_fused_power && linux && arm64 && gc && !purego && !race

package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"syscall"
	"testing"
	"unsafe"
)

const fusedPowerManchesterCanary = byte(0xa5)

func fusedPowerManchesterTestHistory(seed uint64) []uint16 {
	iq := integerPipelineOracleIQ(fusedPowerManchesterHistory, seed, int(seed%3))
	history, _ := integerPipelineOraclePowerStreams(iq)
	return history
}

func fusedPowerManchesterExpected(iq []byte, history []uint16) ([]uint16, []byte) {
	if len(iq) != fusedPowerManchesterBlockSize*2 || len(history) != fusedPowerManchesterHistory {
		panic("invalid fused Power16/Manchester test geometry")
	}
	power := make([]uint16, fusedPowerManchesterBlockSize)
	for idx := range power {
		power[idx] = integerPipelineOraclePower(iq[idx*2], iq[idx*2+1])
	}
	window := make([]uint16, 0, fusedPowerManchesterWindow)
	window = append(window, history...)
	window = append(window, power...)
	trace := integerPipelineOracleManchester(window, fusedPowerManchesterBlockSize, fusedPowerManchesterChipLength)
	return power, trace.decisions
}

func putFusedPowerManchesterHistory(window []byte, history []uint16) {
	for idx, value := range history {
		binary.LittleEndian.PutUint16(window[idx*2:], value)
	}
}

func checkFusedPowerManchesterResult(t *testing.T, label string, window, decisions []byte, wantPower []uint16, wantDecisions []byte) {
	t.Helper()
	for idx, want := range wantPower {
		got := binary.LittleEndian.Uint16(window[(fusedPowerManchesterHistory+idx)*2:])
		if got != want {
			t.Fatalf("%s power[%d]=%d, want %d", label, idx, got, want)
		}
	}
	for idx, want := range wantDecisions {
		if decisions[idx] != want {
			t.Fatalf("%s decision[%d]=%d, want %d", label, idx, decisions[idx], want)
		}
	}
}

func callFusedPowerManchester(decisions, window, iq []byte) {
	fusedPowerManchesterU8Mul32A72(
		unsafe.Pointer(&decisions[0]), unsafe.Pointer(&window[0]), unsafe.Pointer(&iq[0]),
	)
}

func TestFusedPowerManchesterU8Mul32ExhaustiveIQ(t *testing.T) {
	for batch := 0; batch < 8; batch++ {
		iq := make([]byte, fusedPowerManchesterBlockSize*2)
		for idx := 0; idx < fusedPowerManchesterBlockSize; idx++ {
			pair := batch*fusedPowerManchesterBlockSize + idx
			iq[idx*2] = byte(pair >> 8)
			iq[idx*2+1] = byte(pair)
		}
		history := fusedPowerManchesterTestHistory(uint64(batch + 1))
		wantPower, wantDecisions := fusedPowerManchesterExpected(iq, history)
		window := make([]byte, fusedPowerManchesterWindow*2)
		putFusedPowerManchesterHistory(window, history)
		decisions := make([]byte, fusedPowerManchesterBlockSize)
		callFusedPowerManchester(decisions, window, iq)
		checkFusedPowerManchesterResult(t, fmt.Sprintf("batch=%d", batch), window, decisions, wantPower, wantDecisions)
	}
}

func TestFusedPowerManchesterU8Mul32AlignmentsCanariesAndRandom(t *testing.T) {
	for residue := 0; residue < 32; residue++ {
		inputOffset := residue
		windowOffset := residue * 5 & 31
		decisionOffset := residue * 11 & 31
		iqBacking := make([]byte, inputOffset+fusedPowerManchesterBlockSize*2+32)
		for idx := range iqBacking {
			iqBacking[idx] = fusedPowerManchesterCanary
		}
		iq := iqBacking[inputOffset : inputOffset+fusedPowerManchesterBlockSize*2]
		generated := integerPipelineOracleIQ(fusedPowerManchesterBlockSize, uint64(0xd4000000+residue), residue%3)
		copy(iq, generated)
		iqDigest := sha256.Sum256(iqBacking)

		history := fusedPowerManchesterTestHistory(uint64(0x100 + residue))
		wantPower, wantDecisions := fusedPowerManchesterExpected(iq, history)
		windowBacking := make([]byte, windowOffset+fusedPowerManchesterWindow*2+32)
		for idx := range windowBacking {
			windowBacking[idx] = fusedPowerManchesterCanary
		}
		window := windowBacking[windowOffset : windowOffset+fusedPowerManchesterWindow*2]
		putFusedPowerManchesterHistory(window, history)
		historyDigest := sha256.Sum256(window[:fusedPowerManchesterHistory*2])

		decisionBacking := make([]byte, decisionOffset+fusedPowerManchesterBlockSize+32)
		for idx := range decisionBacking {
			decisionBacking[idx] = fusedPowerManchesterCanary
		}
		decisions := decisionBacking[decisionOffset : decisionOffset+fusedPowerManchesterBlockSize]

		callFusedPowerManchester(decisions, window, iq)
		label := fmt.Sprintf("input=%d/window=%d/decision=%d", inputOffset, windowOffset, decisionOffset)
		checkFusedPowerManchesterResult(t, label, window, decisions, wantPower, wantDecisions)
		if got := sha256.Sum256(iqBacking); got != iqDigest {
			t.Fatalf("%s input changed", label)
		}
		if got := sha256.Sum256(window[:fusedPowerManchesterHistory*2]); got != historyDigest {
			t.Fatalf("%s history changed", label)
		}
		for idx, value := range windowBacking[:windowOffset] {
			if value != fusedPowerManchesterCanary {
				t.Fatalf("%s window prefix[%d]=%02x", label, idx, value)
			}
		}
		for idx, value := range windowBacking[windowOffset+len(window):] {
			if value != fusedPowerManchesterCanary {
				t.Fatalf("%s window suffix[%d]=%02x", label, idx, value)
			}
		}
		for idx, value := range decisionBacking[:decisionOffset] {
			if value != fusedPowerManchesterCanary {
				t.Fatalf("%s decision prefix[%d]=%02x", label, idx, value)
			}
		}
		for idx, value := range decisionBacking[decisionOffset+len(decisions):] {
			if value != fusedPowerManchesterCanary {
				t.Fatalf("%s decision suffix[%d]=%02x", label, idx, value)
			}
		}
	}
}

func TestFusedPowerManchesterU8Mul32SequentialBlockHistory(t *testing.T) {
	const (
		r900Overlap = fusedPowerManchesterChipLength * 4
		ringBlocks  = 14
	)
	blockBytes := (r900Overlap + fusedPowerManchesterBlockSize) * 2
	ring := make([][]byte, ringBlocks)
	for idx := range ring {
		ring[idx] = make([]byte, blockBytes)
	}
	history := make([]uint16, fusedPowerManchesterHistory)
	previous := ringBlocks - 1
	for block := 0; block < ringBlocks+3; block++ {
		current := block % ringBlocks
		copy(
			ring[current][:r900Overlap*2],
			ring[previous][(r900Overlap+fusedPowerManchesterBlockSize-r900Overlap)*2:],
		)
		windowStart := (r900Overlap - fusedPowerManchesterHistory) * 2
		window := ring[current][windowStart:]
		for idx := range history {
			history[idx] = binary.LittleEndian.Uint16(window[idx*2:])
		}
		iq := integerPipelineOracleIQ(fusedPowerManchesterBlockSize, uint64(0xd400000000+block), block%3)
		wantPower, wantDecisions := fusedPowerManchesterExpected(iq, history)
		decisions := make([]byte, fusedPowerManchesterBlockSize)
		callFusedPowerManchester(decisions, window, iq)
		checkFusedPowerManchesterResult(t, fmt.Sprintf("block=%d", block), window, decisions, wantPower, wantDecisions)
		previous = current
	}
}

func TestFusedPowerManchesterU8Mul32ExactGuardPages(t *testing.T) {
	iqMapping, iq := guardTerminatedBytes(t, fusedPowerManchesterBlockSize*2)
	windowMapping, window := guardTerminatedBytes(t, fusedPowerManchesterWindow*2)
	decisionMapping, decisions := guardTerminatedBytes(t, fusedPowerManchesterBlockSize)
	defer syscall.Munmap(iqMapping)
	defer syscall.Munmap(windowMapping)
	defer syscall.Munmap(decisionMapping)

	copy(iq, integerPipelineOracleIQ(fusedPowerManchesterBlockSize, 0xd4feedface, 0))
	history := fusedPowerManchesterTestHistory(0xd4)
	putFusedPowerManchesterHistory(window, history)
	wantPower, wantDecisions := fusedPowerManchesterExpected(iq, history)
	callFusedPowerManchester(decisions, window, iq)
	checkFusedPowerManchesterResult(t, "guard", window, decisions, wantPower, wantDecisions)
}
