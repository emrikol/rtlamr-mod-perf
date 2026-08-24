//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"math"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// TestMagLUTA72DoesNotCrossGuardPages proves that the native eight-result
// kernel neither preloads beyond the exact 16-byte IQ input nor stores beyond
// the exact 64-byte output. Ordinary slice canaries cannot detect a read-only
// overread when allocator padding remains mapped.
func TestMagLUTA72DoesNotCrossGuardPages(t *testing.T) {
	if !magnitudeLUTA72Available() {
		t.Skip("exact Cortex-A72 magnitude kernel unavailable")
	}
	pageSize := os.Getpagesize()
	inputMapping := guardedMapping(t, pageSize)
	defer syscall.Munmap(inputMapping)
	outputMapping := guardedMapping(t, pageSize)
	defer syscall.Munmap(outputMapping)

	input := inputMapping[pageSize-16 : pageSize]
	for idx := range input {
		input[idx] = byte(idx*29 + 7)
	}
	outputBytes := outputMapping[pageSize-64 : pageSize]
	output := (*[8]float64)(unsafe.Pointer(&outputBytes[0]))[:]
	lut := NewMagLUT()
	want := make([]float64, 8)
	magnitudeLUTGo(input, want, lut)

	magnitudeLUTA72Platform(output, input, lut)
	for idx := range want {
		if math.Float64bits(output[idx]) != math.Float64bits(want[idx]) {
			t.Fatalf("magnitude %d is %016x, want %016x", idx, math.Float64bits(output[idx]), math.Float64bits(want[idx]))
		}
	}
}

// TestMagLUTA72FullBlockDoesNotCrossGuardPages covers the production 8,192
// output geometry so a pipelined or unrolled leaf cannot hide a terminal input
// preload behind mapped allocator padding.
func TestMagLUTA72FullBlockDoesNotCrossGuardPages(t *testing.T) {
	if !magnitudeLUTA72Available() {
		t.Skip("exact Cortex-A72 magnitude kernel unavailable")
	}
	const count = 8192
	inputMapping, inputBytes := guardTerminatedBytes(t, count*2)
	defer syscall.Munmap(inputMapping)
	outputMapping, outputBytes := guardTerminatedBytes(t, count*8)
	defer syscall.Munmap(outputMapping)
	for idx := range inputBytes {
		inputBytes[idx] = byte(idx*73 + idx/17*29 + 11)
	}
	output := (*[count]float64)(unsafe.Pointer(&outputBytes[0]))[:]
	lut := NewMagLUT()
	want := make([]float64, count)
	magnitudeLUTGo(inputBytes, want, lut)
	magnitudeLUTA72Platform(output, inputBytes, lut)
	for idx := range want {
		if math.Float64bits(output[idx]) != math.Float64bits(want[idx]) {
			t.Fatalf("magnitude %d is %016x, want %016x", idx, math.Float64bits(output[idx]), math.Float64bits(want[idx]))
		}
	}
}

func guardedMapping(t *testing.T, pageSize int) []byte {
	t.Helper()
	mapping, err := syscall.Mmap(-1, 0, pageSize*2, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mprotect(mapping[pageSize:], syscall.PROT_NONE); err != nil {
		syscall.Munmap(mapping)
		t.Fatal(err)
	}
	return mapping
}

func guardTerminatedBytes(t *testing.T, size int) ([]byte, []byte) {
	t.Helper()
	pageSize := os.Getpagesize()
	dataSize := (size + pageSize - 1) &^ (pageSize - 1)
	mapping, err := syscall.Mmap(-1, 0, dataSize+pageSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mprotect(mapping[dataSize:], syscall.PROT_NONE); err != nil {
		syscall.Munmap(mapping)
		t.Fatal(err)
	}
	return mapping, mapping[dataSize-size : dataSize]
}
