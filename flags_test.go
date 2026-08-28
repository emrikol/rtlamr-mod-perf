package main

import (
	"bytes"
	"reflect"
	"testing"
)

func TestNewOutputEncoderRejectsInvalidFormat(t *testing.T) {
	if _, err := newOutputEncoder("yaml", &bytes.Buffer{}, "samples.bin"); err == nil {
		t.Fatal("newOutputEncoder accepted an invalid format")
	}
}

func TestNewProtocolDecoderRejectsInvalidMessageType(t *testing.T) {
	receiver := Receiver{protocolNames: []string{"not-a-protocol"}}
	if _, err := receiver.newProtocolDecoder(); err == nil {
		t.Fatal("newProtocolDecoder accepted an invalid message type")
	}
}

func TestSortedProtocolNames(t *testing.T) {
	types := StringMap{"r900": true, "idm": true, "scm": true}
	want := []string{"idm", "r900", "scm"}
	for iteration := 0; iteration < 100; iteration++ {
		if got := sortedProtocolNames(types); !reflect.DeepEqual(got, want) {
			t.Fatalf("sortedProtocolNames() = %v, want %v", got, want)
		}
	}
}

func TestValidateDirectKernelBatchBlocks(t *testing.T) {
	for _, blocks := range []int{1, receiverReadBlocks, 36, 256} {
		if err := validateDirectKernelBatchBlocks(blocks); err != nil {
			t.Fatalf("validateDirectKernelBatchBlocks(%d): %v", blocks, err)
		}
	}
	for _, blocks := range []int{-1, 0, 257} {
		if err := validateDirectKernelBatchBlocks(blocks); err == nil {
			t.Fatalf("validateDirectKernelBatchBlocks(%d) succeeded", blocks)
		}
	}
}
