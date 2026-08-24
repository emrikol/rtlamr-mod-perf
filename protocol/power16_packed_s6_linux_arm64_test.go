//go:build linux && arm64 && gc && !purego && !race

package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"syscall"
	"testing"
	"unsafe"
)

const s6PackedCanary = byte(0xa6)

func s6PackedExpected(input []byte, history []uint16) (window []uint16, decisions, packed []byte) {
	window = make([]uint16, power16Window)
	copy(window, history)
	decisions = make([]byte, power16BlockSize)
	if !power16ReferenceBlock(decisions, window, input) {
		panic("S6 oracle rejected fixed geometry")
	}
	packed = make([]byte, power16BlockSize>>3)
	s6PackOracle(packed, decisions)
	return window, decisions, packed
}

func s6PackedHistory(seed int) []uint16 {
	history := make([]uint16, power16History)
	for idx := range history {
		history[idx] = uint16((idx*977 + idx/7*313 + seed*101 + 11) & 0xffff)
	}
	return history
}

func s6PutHistory(window []byte, history []uint16) {
	for idx, value := range history {
		binary.LittleEndian.PutUint16(window[idx*2:], value)
	}
}

func s6CallPacked(decisions, packed, window, input []byte) {
	fusedPowerManchesterPackedU8Mul32A72(
		unsafe.Pointer(&decisions[0]), unsafe.Pointer(&packed[0]),
		unsafe.Pointer(&window[0]), unsafe.Pointer(&input[0]),
	)
}

func s6CheckPackedResult(t *testing.T, label string, window, decisions, packed []byte, wantWindow []uint16, wantDecisions, wantPacked []byte) {
	t.Helper()
	if !bytes.Equal(decisions, wantDecisions) {
		t.Fatalf("%s: decision output differs", label)
	}
	if !bytes.Equal(packed, wantPacked) {
		t.Fatalf("%s: packed output differs", label)
	}
	for idx := power16History; idx < power16Window; idx++ {
		if got := binary.LittleEndian.Uint16(window[idx*2:]); got != wantWindow[idx] {
			t.Fatalf("%s: power[%d]=%d, want %d", label, idx-power16History, got, wantWindow[idx])
		}
	}
}

func TestS6RawPackMaterializerAllPatterns(t *testing.T) {
	decisions := make([]byte, 16)
	outputBacking := []byte{s6PackedCanary, 0, 0, s6PackedCanary}
	for pattern := 0; pattern < 1<<16; pattern++ {
		for idx := range decisions {
			decisions[idx] = byte(pattern>>uint(15-idx)) & 1
		}
		packDecision16S6A72(unsafe.Pointer(&outputBacking[1]), unsafe.Pointer(&decisions[0]))
		if got := binary.BigEndian.Uint16(outputBacking[1:3]); got != uint16(pattern) {
			t.Fatalf("pattern=%#04x packed=%#04x", pattern, got)
		}
		if outputBacking[0] != s6PackedCanary || outputBacking[3] != s6PackedCanary {
			t.Fatalf("pattern=%#04x changed materializer canary", pattern)
		}
	}
}

func TestS6FusedPackedExhaustiveIQ(t *testing.T) {
	for batch := 0; batch < 8; batch++ {
		input := make([]byte, power16BlockSize*2)
		for idx := 0; idx < power16BlockSize; idx++ {
			pair := batch*power16BlockSize + idx
			input[idx*2] = byte(pair >> 8)
			input[idx*2+1] = byte(pair)
		}
		history := s6PackedHistory(batch + 1)
		wantWindow, wantDecisions, wantPacked := s6PackedExpected(input, history)
		window := make([]byte, power16Window*2)
		s6PutHistory(window, history)
		decisions := make([]byte, power16BlockSize)
		packed := make([]byte, power16BlockSize>>3)
		s6CallPacked(decisions, packed, window, input)
		s6CheckPackedResult(t, fmt.Sprintf("batch=%d", batch), window, decisions, packed, wantWindow, wantDecisions, wantPacked)
	}
}

func TestS6FusedPackedAlignmentsAndCanaries(t *testing.T) {
	for residue := 0; residue < 16; residue++ {
		inputOffset := residue
		windowOffset := residue * 5 & 15
		decisionOffset := residue * 7 & 15
		packedOffset := residue * 11 & 15

		inputBacking := bytes.Repeat([]byte{s6PackedCanary}, inputOffset+power16BlockSize*2+16)
		input := inputBacking[inputOffset : inputOffset+power16BlockSize*2]
		for idx := range input {
			input[idx] = byte(idx*73 + idx/31*19 + residue*17 + 7)
		}
		inputDigest := sha256.Sum256(inputBacking)
		history := s6PackedHistory(0x100 + residue)
		wantWindow, wantDecisions, wantPacked := s6PackedExpected(input, history)

		windowBacking := bytes.Repeat([]byte{s6PackedCanary}, windowOffset+power16Window*2+16)
		window := windowBacking[windowOffset : windowOffset+power16Window*2]
		s6PutHistory(window, history)
		decisionBacking := bytes.Repeat([]byte{s6PackedCanary}, decisionOffset+power16BlockSize+16)
		decisions := decisionBacking[decisionOffset : decisionOffset+power16BlockSize]
		packedBacking := bytes.Repeat([]byte{s6PackedCanary}, packedOffset+(power16BlockSize>>3)+16)
		packed := packedBacking[packedOffset : packedOffset+(power16BlockSize>>3)]

		s6CallPacked(decisions, packed, window, input)
		label := fmt.Sprintf("input=%d/window=%d/decision=%d/packed=%d", inputOffset, windowOffset, decisionOffset, packedOffset)
		s6CheckPackedResult(t, label, window, decisions, packed, wantWindow, wantDecisions, wantPacked)
		if got := sha256.Sum256(inputBacking); got != inputDigest {
			t.Fatalf("%s: input changed", label)
		}
		s6CheckCanary(t, label+" window", windowBacking, windowOffset, len(window))
		s6CheckCanary(t, label+" decisions", decisionBacking, decisionOffset, len(decisions))
		s6CheckCanary(t, label+" packed", packedBacking, packedOffset, len(packed))
	}
}

func s6CheckCanary(t *testing.T, label string, backing []byte, offset, length int) {
	t.Helper()
	for idx, value := range backing[:offset] {
		if value != s6PackedCanary {
			t.Fatalf("%s prefix[%d]=%#02x", label, idx, value)
		}
	}
	for idx, value := range backing[offset+length:] {
		if value != s6PackedCanary {
			t.Fatalf("%s suffix[%d]=%#02x", label, idx, value)
		}
	}
}

func TestS6FusedPackedExactGuardPages(t *testing.T) {
	inputMapping, input := guardTerminatedBytes(t, power16BlockSize*2)
	windowMapping, window := guardTerminatedBytes(t, power16Window*2)
	decisionMapping, decisions := guardTerminatedBytes(t, power16BlockSize)
	packedMapping, packed := guardTerminatedBytes(t, power16BlockSize>>3)
	defer syscall.Munmap(inputMapping)
	defer syscall.Munmap(windowMapping)
	defer syscall.Munmap(decisionMapping)
	defer syscall.Munmap(packedMapping)

	for idx := range input {
		input[idx] = byte(idx*73 + idx/31*19 + 7)
	}
	history := s6PackedHistory(0x5678)
	wantWindow, wantDecisions, wantPacked := s6PackedExpected(input, history)
	s6PutHistory(window, history)
	s6CallPacked(decisions, packed, window, input)
	s6CheckPackedResult(t, "guard", window, decisions, packed, wantWindow, wantDecisions, wantPacked)
}

func TestS6FusedPackedWrapperRejectsGeometryAndOverlap(t *testing.T) {
	decisions := make([]byte, power16BlockSize)
	packed := make([]byte, power16BlockSize>>3)
	window := make([]uint16, power16Window)
	input := make([]byte, power16BlockSize*2)
	if power16FusedPackedA72(decisions[:len(decisions)-1], packed, window, input) ||
		power16FusedPackedA72(decisions, packed[:len(packed)-1], window, input) ||
		power16FusedPackedA72(decisions, packed, window[:len(window)-1], input) ||
		power16FusedPackedA72(decisions, packed, window, input[:len(input)-1]) {
		t.Fatal("invalid geometry reached S6 leaf")
	}
	if power16FusedPackedA72(decisions, decisions[:len(packed)], window, input) {
		t.Fatal("overlapping decisions/packed reached S6 leaf")
	}
	windowBytes := (*[power16Window * 2]byte)(unsafe.Pointer(&window[0]))[:]
	if power16FusedPackedA72(decisions, windowBytes[:len(packed)], window, input) {
		t.Fatal("overlapping packed/window reached S6 leaf")
	}
}
