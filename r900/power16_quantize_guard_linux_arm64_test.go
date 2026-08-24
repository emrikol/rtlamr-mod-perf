//go:build linux && arm64 && gc && !purego && !race

package r900

import (
	"reflect"
	"syscall"
	"testing"
	"unsafe"
)

func TestR900Power16SIMDExactGuardPages(t *testing.T) {
	page := syscall.Getpagesize()
	const bytes = r900Power16TestChipLength * 4 * 2
	if bytes >= page {
		t.Fatalf("fixture bytes=%d page=%d", bytes, page)
	}
	mapping, err := syscall.Mmap(-1, 0, page*3, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Mprotect(mapping[:page], syscall.PROT_READ|syscall.PROT_WRITE)
		_ = syscall.Mprotect(mapping[page*2:], syscall.PROT_READ|syscall.PROT_WRITE)
		_ = syscall.Munmap(mapping)
	}()
	if err := syscall.Mprotect(mapping[:page], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mprotect(mapping[page*2:], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}

	for _, offset := range []int{page, page*2 - bytes} {
		accessible := mapping[offset : offset+bytes]
		power := (*[r900Power16TestChipLength * 4]uint16)(unsafe.Pointer(&accessible[0]))[:]
		for index := range power {
			power[index] = uint16((index*977 + offset/2) % 65026)
		}
		before := append([]uint16(nil), power...)
		want := r900Power16Oracle(power)
		got := byte(quantizePower16L72A72(unsafe.Pointer(&power[0])))
		if got != want.symbol {
			t.Fatalf("offset=%d got=%d want=%d values=%v", offset, got, want.symbol, want.values)
		}
		if !reflect.DeepEqual(power, before) {
			t.Fatalf("offset=%d leaf modified exact guard-page input", offset)
		}
	}
}
