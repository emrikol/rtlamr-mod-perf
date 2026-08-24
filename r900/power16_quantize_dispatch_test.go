package r900

import (
	"sync"
	"testing"
)

func TestR900Power16QuantizerStatusAndPortableGeometry(t *testing.T) {
	_ = NewParser(power16R900SIMDChipLength)
	status := Power16QuantizerDispatchStatus()
	if status.Implementation == "" {
		t.Fatal("empty Power16 quantizer implementation")
	}
	if status.NativeAvailable && status.Implementation != power16R900SIMDImplementation {
		t.Fatalf("native implementation=%q", status.Implementation)
	}
	if !status.NativeAvailable && status.Implementation != power16R900PortableImplementation {
		t.Fatalf("portable implementation=%q", status.Implementation)
	}

	power16R900ResetCountersForTest()
	const chipLength = 71
	window := make([]uint16, chipLength*4)
	for index := range window {
		window[index] = uint16((index*977 + 31) % 65026)
	}
	want := quantizePower16WindowGo(window, chipLength)
	if got := quantizePower16Window(window, chipLength); got != want {
		t.Fatalf("portable geometry got=%d want=%d", got, want)
	}
	status = Power16QuantizerDispatchStatus()
	if status.NativeCalls != 0 || status.PortableCalls != 1 {
		t.Fatalf("portable geometry counters native=%d portable=%d", status.NativeCalls, status.PortableCalls)
	}
}

func TestR900Power16QuantizerParserDispatchCounters(t *testing.T) {
	const blockSize = 8192
	history := &r900Power16TrackingHistory{values: make([]uint16, blockSize+power16R900SIMDChipLength*4)}
	for index := range history.values {
		history.values[index] = uint16((index*1543 + 71) % 65026)
	}
	parser := NewParser(power16R900SIMDChipLength).(*Parser)
	parser.SetPower16History(history)
	power16R900ResetCountersForTest()
	want := quantizePower16WindowGo(history.values[:], power16R900SIMDChipLength)
	if got := parser.quantizeSignalAt(0); got != want {
		t.Fatalf("parser dispatch got=%d want=%d", got, want)
	}
	status := Power16QuantizerDispatchStatus()
	if status.NativeCalls+status.PortableCalls != 1 {
		t.Fatalf("parser dispatch counters native=%d portable=%d", status.NativeCalls, status.PortableCalls)
	}
	if status.NativeAvailable && status.NativeCalls != 1 {
		t.Fatalf("native parser dispatch counters: %+v", status)
	}
	if !status.NativeAvailable && status.PortableCalls != 1 {
		t.Fatalf("portable parser dispatch counters: %+v", status)
	}
}

func TestR900Power16QuantizerConcurrentCounters(t *testing.T) {
	const (
		chipLength = 71
		workers    = 8
		calls      = 257
	)
	window := make([]uint16, chipLength*4)
	for index := range window {
		window[index] = uint16((index*2017 + 97) % 65026)
	}
	power16R900ResetCountersForTest()
	var group sync.WaitGroup
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer group.Done()
			for call := 0; call < calls; call++ {
				_ = quantizePower16Window(window, chipLength)
			}
		}()
	}
	group.Wait()
	status := Power16QuantizerDispatchStatus()
	if status.NativeCalls != 0 || status.PortableCalls != workers*calls {
		t.Fatalf("concurrent counters native=%d portable=%d want portable=%d", status.NativeCalls, status.PortableCalls, workers*calls)
	}
}
