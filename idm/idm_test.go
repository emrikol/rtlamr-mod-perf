package idm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/bemasher/rtlamr/protocol"
)

func legacyNewIDM(data protocol.Data) (idm IDM) {
	idm.Preamble = binary.BigEndian.Uint32(data.Bytes[0:4])
	idm.PacketTypeID = data.Bytes[4]
	idm.PacketLength = data.Bytes[5]
	idm.HammingCode = data.Bytes[6]
	idm.ApplicationVersion = data.Bytes[7]
	idm.ERTType = data.Bytes[8] & 0x0f
	idm.ERTSerialNumber = binary.BigEndian.Uint32(data.Bytes[9:13])
	idm.ConsumptionIntervalCount = data.Bytes[13]
	idm.ModuleProgrammingState = data.Bytes[14]
	idm.TamperCounters = append([]byte(nil), data.Bytes[15:21]...)
	idm.AsynchronousCounters = binary.BigEndian.Uint16(data.Bytes[21:23])
	idm.PowerOutageFlags = append([]byte(nil), data.Bytes[23:29]...)
	idm.LastConsumptionCount = binary.BigEndian.Uint32(data.Bytes[29:33])

	offset := 264
	for idx := range idm.DifferentialConsumptionIntervals {
		interval, err := strconv.ParseUint(data.Bits[offset:offset+9], 2, 9)
		if err != nil {
			panic(err)
		}
		idm.DifferentialConsumptionIntervals[idx] = uint16(interval)
		offset += 9
	}

	idm.TransmitTimeOffset = binary.BigEndian.Uint16(data.Bytes[86:88])
	idm.SerialNumberCRC = binary.BigEndian.Uint16(data.Bytes[88:90])
	idm.PacketCRC = binary.BigEndian.Uint16(data.Bytes[90:92])
	return
}

func assertIDMMatchesLegacy(t *testing.T, packet []byte) {
	t.Helper()
	data := protocol.NewData(packet)
	want := legacyNewIDM(data)
	for name, got := range map[string]IDM{
		"byte-only": NewIDM(protocol.Data{Bytes: packet}),
		"public":    NewIDM(data),
	} {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s decoded IDM differs for packet %x\ngot:  %+v\nwant: %+v", name, packet, got, want)
		}
	}
}

func TestNewIDMMatchesLegacyAcrossGeneratedPackets(t *testing.T) {
	tests := make([][]byte, 0, 3+53*8)
	tests = append(tests, make([]byte, 92))
	allOnes := make([]byte, 92)
	for idx := range allOnes {
		allOnes[idx] = 0xff
	}
	tests = append(tests, allOnes)
	incrementing := make([]byte, 92)
	for idx := range incrementing {
		incrementing[idx] = byte(idx)
	}
	tests = append(tests, incrementing)

	// Pin every bit boundary in the 53-byte interval payload.
	for bit := 0; bit < 53*8; bit++ {
		packet := make([]byte, 92)
		packet[33+bit/8] = 1 << uint(7-bit%8)
		tests = append(tests, packet)
	}

	for idx, packet := range tests {
		t.Run(fmt.Sprintf("boundary-%03d", idx), func(t *testing.T) {
			assertIDMMatchesLegacy(t, packet)
		})
	}

	rng := rand.New(rand.NewSource(0x1d4d))
	packet := make([]byte, 92)
	for iteration := 0; iteration < 4096; iteration++ {
		if _, err := rng.Read(packet); err != nil {
			t.Fatal(err)
		}
		assertIDMMatchesLegacy(t, packet)
	}
}

func TestNewIDMDoesNotRequireBitProjection(t *testing.T) {
	packet := make([]byte, 92)
	for idx := range packet {
		packet[idx] = byte(idx)
	}
	got := NewIDM(protocol.Data{Bytes: packet})
	want := legacyNewIDM(protocol.NewData(packet))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("byte-only decode differs\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestNewIDMDoesNotAliasPacketBuffer(t *testing.T) {
	data := protocol.Data{
		Bits:  strings.Repeat("0", 92*8),
		Bytes: make([]byte, 92),
	}
	for idx := range data.Bytes {
		data.Bytes[idx] = byte(idx)
	}

	message := NewIDM(data)
	wantTamper := append([]byte(nil), message.TamperCounters...)
	wantPower := append([]byte(nil), message.PowerOutageFlags...)

	for idx := range data.Bytes {
		data.Bytes[idx] ^= 0xff
	}

	if !bytes.Equal(message.TamperCounters, wantTamper) {
		t.Fatalf("tamper counters changed with source packet: got %x, want %x", message.TamperCounters, wantTamper)
	}
	if !bytes.Equal(message.PowerOutageFlags, wantPower) {
		t.Fatalf("power outage flags changed with source packet: got %x, want %x", message.PowerOutageFlags, wantPower)
	}
}

func TestParserReportsPower16Compatibility(t *testing.T) {
	parser := NewParser(72)
	compatible, ok := parser.(protocol.Power16Compatible)
	if !ok || !compatible.Power16Compatible() {
		t.Fatal("IDM parser did not report Power16 compatibility")
	}
}

func TestParserReportsBytesOnlyCapability(t *testing.T) {
	parser := NewParser(72)
	bytesOnly, ok := parser.(protocol.PacketBytesOnly)
	if !ok || !bytesOnly.PacketBytesOnly() {
		t.Fatal("IDM parser did not report byte-only packet compatibility")
	}
}

func TestParserReusesDedupeStorageWithoutCrossCallState(t *testing.T) {
	parser := NewParser(72).(*Parser)
	first := protocol.Data{Bytes: make([]byte, 92)}
	parser.Parse([]protocol.Data{first, first}, nil)
	if len(parser.seen) != 1 {
		t.Fatalf("first parse retained %d packet keys, want 1", len(parser.seen))
	}

	second := protocol.Data{Bytes: make([]byte, 92)}
	second.Bytes[0] = 1
	parser.Parse([]protocol.Data{second}, nil)
	if len(parser.seen) != 1 {
		t.Fatalf("second parse retained %d packet keys, want 1", len(parser.seen))
	}
	var key [92]byte
	copy(key[:], second.Bytes)
	if _, ok := parser.seen[key]; !ok {
		t.Fatal("second parse retained stale key instead of current packet")
	}
}

var benchmarkIDM IDM

func benchmarkIDMDecode(b *testing.B, decode func(protocol.Data) IDM) {
	packet := make([]byte, 92)
	rand.New(rand.NewSource(0x1d4d)).Read(packet)
	data := protocol.NewData(packet)
	b.ReportAllocs()
	b.SetBytes(int64(len(packet)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		benchmarkIDM = decode(data)
	}
}

func BenchmarkNewIDMLegacy(b *testing.B) {
	benchmarkIDMDecode(b, legacyNewIDM)
}

func BenchmarkNewIDMBytes(b *testing.B) {
	benchmarkIDMDecode(b, func(data protocol.Data) IDM {
		data.Bits = ""
		return NewIDM(data)
	})
}
