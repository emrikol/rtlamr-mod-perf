package scmplus

import (
	"encoding/json"
	"testing"
)

func TestSCMJSONUsesChecksumField(t *testing.T) {
	encoded, err := json.Marshal(SCM{PacketCRC: 0x1234})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["Checksum"]; !ok {
		t.Fatalf("Checksum field missing from %s", encoded)
	}
	if _, ok := fields["PacketCRC"]; ok {
		t.Fatalf("implementation field leaked into %s", encoded)
	}
}
