//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"encoding/binary"
	"math/rand"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestSearch32DualA72MatchesSeparateLeaves(t *testing.T) {
	if !searchAlignedCandidates32DualFixedAvailable() {
		t.Skip("Cortex-A72 dual fixed search is unavailable")
	}
	const (
		count     = 1024
		packedLen = count + 18*31
	)
	iterations := 10000
	if os.Getenv("RTLAMR_STRESS_NEON") != "" {
		iterations = 200000
	}
	rng := rand.New(rand.NewSource(0x32d0a172))
	packed := make([]byte, packedLen)
	for iteration := 0; iteration < iterations; iteration++ {
		rng.Read(packed)
		if iteration < 8 {
			for idx := range packed {
				packed[idx] = byte(iteration * 0x11)
			}
		}
		if iteration%3 == 0 {
			implantSearch32DualPreamble(packed, searchAlignedCandidates32IDMPreamble[:], (iteration*977)&(count*8-1))
		}
		if iteration%5 == 0 {
			implantSearch32DualPreamble(packed, searchAlignedCandidates32R900Preamble[:], (iteration*619+7)&(count*8-1))
		}
		checkSearch32Dual(t, packed)
	}
}

func TestSearch32DualA72PackedResidues(t *testing.T) {
	if !searchAlignedCandidates32DualFixedAvailable() {
		t.Skip("Cortex-A72 dual fixed search is unavailable")
	}
	const (
		count     = 1024
		packedLen = count + 18*31
	)
	for residue := 0; residue < 64; residue++ {
		packed := search32DualBytesAtResidue(packedLen, residue)
		for idx := range packed {
			packed[idx] = byte(idx*73 + idx/17*29 + residue*41 + 11)
		}
		implantSearch32DualPreamble(packed, searchAlignedCandidates32IDMPreamble[:], residue&7)
		implantSearch32DualPreamble(packed, searchAlignedCandidates32R900Preamble[:], count*8-1-(residue&7))
		checkSearch32Dual(t, packed)
	}
}

func TestSearch32DualA72DoesNotCrossGuardPages(t *testing.T) {
	if !searchAlignedCandidates32DualFixedAvailable() {
		t.Skip("Cortex-A72 dual fixed search is unavailable")
	}
	const (
		count     = 1024
		packedLen = count + 18*31
		outputLen = count * 8 * 8
	)
	packedMapping, packed := guardTerminatedBytes(t, packedLen)
	defer unix.Munmap(packedMapping)
	idmMapping, idmOutput := guardTerminatedBytes(t, outputLen)
	defer unix.Munmap(idmMapping)
	r900Mapping, r900Output := guardTerminatedBytes(t, outputLen)
	defer unix.Munmap(r900Mapping)
	for idx := range packed {
		packed[idx] = byte(idx*73 + idx/17*29 + 11)
	}
	implantSearch32DualPreamble(packed, searchAlignedCandidates32IDMPreamble[:], 0)
	implantSearch32DualPreamble(packed, searchAlignedCandidates32R900Preamble[:], count*8-1)

	wantIDM, wantR900 := search32DualSeparate(packed)
	gotIDM := make([]int, count*8)
	gotR900 := make([]int, count*8)
	idmN, r900N := searchAlignedCandidates32IDMR900A72(unsafe.Pointer(&packed[0]), unsafe.Pointer(&idmOutput[0]), unsafe.Pointer(&r900Output[0]), count)
	gotIDM = gotIDM[:idmN]
	for idx := range gotIDM {
		gotIDM[idx] = int(binary.LittleEndian.Uint64(idmOutput[idx*8:]))
	}
	gotR900 = gotR900[:r900N]
	for idx := range gotR900 {
		gotR900[idx] = int(binary.LittleEndian.Uint64(r900Output[idx*8:]))
	}
	if !equalInts(gotIDM, wantIDM) {
		t.Fatalf("guard-page IDM mismatch: got=%d want=%d", len(gotIDM), len(wantIDM))
	}
	if !equalInts(gotR900, wantR900) {
		t.Fatalf("guard-page R900 mismatch: got=%d want=%d", len(gotR900), len(wantR900))
	}
}

func implantSearch32DualPreamble(packed, preamble []byte, sample int) {
	qByte := sample >> 3
	phaseMask := byte(0x80 >> uint(sample&7))
	for symbol, bit := range preamble {
		idx := qByte + symbol*18
		if bit == 0 {
			packed[idx] &^= phaseMask
		} else {
			packed[idx] |= phaseMask
		}
	}
}

func checkSearch32Dual(t *testing.T, packed []byte) {
	t.Helper()
	wantIDM, wantR900 := search32DualSeparate(packed)
	const count = 1024
	gotIDM := make([]int, count*8)
	gotR900 := make([]int, count*8)
	idmN, r900N := searchAlignedCandidates32IDMR900A72(unsafe.Pointer(&packed[0]), unsafe.Pointer(&gotIDM[0]), unsafe.Pointer(&gotR900[0]), count)
	gotIDM = gotIDM[:idmN]
	gotR900 = gotR900[:r900N]
	if !equalInts(gotIDM, wantIDM) {
		t.Fatalf("IDM mismatch: got n=%d %v want n=%d %v", len(gotIDM), gotIDM, len(wantIDM), wantIDM)
	}
	if !equalInts(gotR900, wantR900) {
		t.Fatalf("R900 mismatch: got n=%d %v want n=%d %v", len(gotR900), gotR900, len(wantR900), wantR900)
	}
}

func search32DualSeparate(packed []byte) ([]int, []int) {
	const count = 1024
	scratch := make([]byte, count)
	idm := make([]int, count*8)
	r900 := make([]int, count*8)
	idmN := searchAlignedCandidates32IDMA72(unsafe.Pointer(&scratch[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&scratch[0]), unsafe.Pointer(&idm[0]), 18, count)
	r900N := searchAlignedCandidates32R900A72(unsafe.Pointer(&scratch[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&scratch[0]), unsafe.Pointer(&r900[0]), 18, count)
	return idm[:idmN], r900[:r900N]
}

func search32DualBytesAtResidue(size, residue int) []byte {
	backing := make([]byte, size+64)
	for offset := 0; offset < 64; offset++ {
		if int(uintptr(unsafe.Pointer(&backing[offset]))&63) == residue {
			return backing[offset : offset+size]
		}
	}
	panic("unreachable cache-line residue")
}
