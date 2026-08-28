package protocol

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
)

type decoderTestMessage struct {
	id uint32
}

func (m decoderTestMessage) Record() []string { return nil }
func (m decoderTestMessage) MsgType() string  { return "test" }
func (m decoderTestMessage) MeterID() uint32  { return m.id }
func (m decoderTestMessage) MeterType() uint8 { return 0 }
func (m decoderTestMessage) Checksum() []byte { return nil }

type decoderTestParser struct {
	id   uint32
	emit bool
}

func (p decoderTestParser) Parse(_ []Data, messages []Message) []Message {
	if !p.emit {
		return messages
	}
	return append(messages, decoderTestMessage{id: p.id})
}
func (p decoderTestParser) SetDecoder(*Decoder) {}
func (p decoderTestParser) Cfg() PacketConfig   { return PacketConfig{} }

type decoderIndexTestParser struct {
	id       uint32
	received *[]int
}

func (p *decoderIndexTestParser) Parse(_ []Data, _ []Message) []Message {
	panic("index-capable parser received materialized packets")
}
func (p *decoderIndexTestParser) ParseCandidateIndices(indices []int, messages []Message) []Message {
	*p.received = append((*p.received)[:0], indices...)
	return append(messages, decoderTestMessage{id: p.id})
}
func (p *decoderIndexTestParser) SetDecoder(*Decoder) {}
func (p *decoderIndexTestParser) Cfg() PacketConfig   { return PacketConfig{} }

type decoderBytesOnlyTestParser struct {
	received *[]Data
}

func (p *decoderBytesOnlyTestParser) Parse(packets []Data, messages []Message) []Message {
	*p.received = append((*p.received)[:0], packets...)
	return messages
}
func (p *decoderBytesOnlyTestParser) PacketBytesOnly() bool { return true }
func (p *decoderBytesOnlyTestParser) SetDecoder(*Decoder)   {}
func (p *decoderBytesOnlyTestParser) Cfg() PacketConfig     { return PacketConfig{} }

type decoderPacketTestParser struct {
	received *[]Data
}

func (p *decoderPacketTestParser) Parse(packets []Data, messages []Message) []Message {
	*p.received = append((*p.received)[:0], packets...)
	return messages
}
func (p *decoderPacketTestParser) SetDecoder(*Decoder) {}
func (p *decoderPacketTestParser) Cfg() PacketConfig   { return PacketConfig{} }

type decoderConfigTestParser struct {
	cfg PacketConfig
}

func (p decoderConfigTestParser) Parse(_ []Data, messages []Message) []Message { return messages }
func (p decoderConfigTestParser) SetDecoder(*Decoder)                          {}
func (p decoderConfigTestParser) Cfg() PacketConfig                            { return p.cfg }

func testDecoderConfig() PacketConfig {
	return PacketConfig{
		BlockSize:      8192,
		ChipLength:     72,
		SymbolLength:   144,
		PreambleLength: 4608,
		PacketLength:   105984,
		BufferLength:   114176,
	}
}

func TestRegisterProtocolCenterFrequencyIsOrderIndependent(t *testing.T) {
	const low = uint32(912380000)
	const high = uint32(912600155)
	parsers := []decoderConfigTestParser{
		{cfg: PacketConfig{CenterFreq: low, Preamble: "0"}},
		{cfg: PacketConfig{CenterFreq: high, Preamble: "1"}},
	}
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		decoder := NewDecoder()
		decoder.RegisterProtocol(parsers[order[0]])
		decoder.RegisterProtocol(parsers[order[1]])
		if got := decoder.Cfg.CenterFreq; got != high {
			t.Fatalf("registration order %v center frequency = %d, want %d", order, got, high)
		}
	}
}

func TestFilterMatchesReference(t *testing.T) {
	cfg := testDecoderConfig()
	rng := rand.New(rand.NewSource(0x1d00))
	input := make([]float64, cfg.BlockSize+cfg.SymbolLength)
	decoder := Decoder{Cfg: cfg}
	got := make([]byte, cfg.BlockSize)
	csum := make([]float64, len(input)+1)
	want := make([]byte, len(got))

	for iteration := 0; iteration < 64; iteration++ {
		for idx := range input {
			input[idx] = rng.Float64() * 2
		}
		decoder.Filter(input, got)

		var sum float64
		for idx, value := range input {
			sum += value
			csum[idx+1] = sum
		}
		for idx := range want {
			lower := csum[idx+cfg.ChipLength]
			upper := csum[idx+cfg.SymbolLength]
			want[idx] = 0
			if (lower-csum[idx])-(upper-lower) >= 0 {
				want[idx] = 1
			}
		}

		if !bytes.Equal(got, want) {
			for idx := range want {
				if got[idx] != want[idx] {
					t.Fatalf("iteration %d: decision %d = %d, want %d", iteration, idx, got[idx], want[idx])
				}
			}
		}
	}
}

func TestDecodeServicesParsersSynchronously(t *testing.T) {
	decoder := newParserOrderTestDecoder()

	var ids []uint32
	for _, message := range decoder.Decode(make([]byte, decoder.Cfg.BlockSize*2)) {
		ids = append(ids, message.MeterID())
	}
	if len(ids) != 2 {
		t.Fatalf("meter IDs = %v, want [1 2]", ids)
	}
	if !equalInts([]int{int(ids[0]), int(ids[1])}, []int{1, 2}) {
		t.Fatalf("meter IDs = %v, want [1 2]", ids)
	}
}

func TestDecodeFilteredServicesSameParserObjectsInOrder(t *testing.T) {
	decoder := newParserOrderTestDecoder()

	var ids []int
	for _, message := range decoder.decodeFiltered(make([]byte, decoder.Cfg.BlockSize)) {
		ids = append(ids, int(message.MeterID()))
	}
	if !equalInts(ids, []int{1, 2}) {
		t.Fatalf("meter IDs = %v, want [1 2]", ids)
	}
}

func newParserOrderTestDecoder() Decoder {
	const (
		blockSize      = 8
		chipLength     = 4
		symbolLength   = chipLength * 2
		packetLength   = 8
		preambleLength = 8
		bufferLength   = blockSize + packetLength
	)
	return Decoder{
		Cfg: PacketConfig{
			BlockSize:      blockSize,
			ChipLength:     chipLength,
			SymbolLength:   symbolLength,
			PacketLength:   packetLength,
			PreambleLength: preambleLength,
			BufferLength:   bufferLength,
		},
		Signal:       make([]float64, blockSize+symbolLength),
		Quantized:    make([]byte, bufferLength),
		demod:        NewMagLUT(),
		filterOutput: make([]byte, blockSize),
		preambles: map[string][]Parser{
			string([]byte{0}): {
				decoderTestParser{id: 1, emit: true},
				decoderTestParser{id: 2, emit: true},
			},
		},
		pkt:    make([]byte, 1),
		packed: make([]byte, (blockSize+preambleLength+7)>>3),
		sIdxA:  make([]int, 0, blockSize),
		sIdxB:  make([]int, 0, blockSize),
	}
}

func TestParserDestinationPrefixAndOrder(t *testing.T) {
	parsers := []Parser{
		decoderTestParser{},
		decoderTestParser{id: 1, emit: true},
		decoderTestParser{id: 2, emit: true},
	}
	messages := []Message{decoderTestMessage{id: 99}}
	for _, parser := range parsers {
		messages = parser.Parse(nil, messages)
	}

	var ids []int
	for _, message := range messages {
		ids = append(ids, int(message.MeterID()))
	}
	if !equalInts(ids, []int{99, 1, 2}) {
		t.Fatalf("meter IDs = %v, want [99 1 2]", ids)
	}
}

func TestParseCandidatesUsesIndexCapabilityAndPreservesOrder(t *testing.T) {
	indices := []int{0, 1}
	var firstIndices, thirdIndices []int
	parsers := []Parser{
		&decoderIndexTestParser{id: 1, received: &firstIndices},
		decoderTestParser{id: 2, emit: true},
		&decoderIndexTestParser{id: 3, received: &thirdIndices},
	}
	decoder := newParserOrderTestDecoder()
	messages := decoder.parseCandidates(indices, parsers, nil)

	var ids []int
	for _, message := range messages {
		ids = append(ids, int(message.MeterID()))
	}
	if !equalInts(ids, []int{1, 2, 3}) {
		t.Fatalf("meter IDs = %v, want [1 2 3]", ids)
	}
	if !equalInts(firstIndices, indices) || !equalInts(thirdIndices, indices) {
		t.Fatalf("index-capable parsers received %v and %v, want %v", firstIndices, thirdIndices, indices)
	}
}

func TestParseCandidatesIndexOnlyDoesNotMaterializePackets(t *testing.T) {
	indices := []int{17, 42}
	var received []int
	parser := &decoderIndexTestParser{id: 1, received: &received}
	decoder := Decoder{}
	messages := decoder.parseCandidates(indices, []Parser{parser}, nil)
	if len(messages) != 1 || messages[0].MeterID() != 1 {
		t.Fatalf("messages = %v, want one message from parser 1", messages)
	}
	if !equalInts(received, indices) {
		t.Fatalf("indices = %v, want %v", received, indices)
	}
}

func TestPacketBytesAtMatchesLogicalRingOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5041434b4554))
	for iteration := 0; iteration < 500; iteration++ {
		blockSize := 8 + rng.Intn(249)
		packetSymbols := 1 + rng.Intn(200)
		symbolLength := 1 + rng.Intn(16)
		ringLength := blockSize + packetSymbols*symbolLength + 17
		decoder := Decoder{
			Cfg: PacketConfig{
				BlockSize:     blockSize,
				PacketSymbols: packetSymbols,
				SymbolLength:  symbolLength,
			},
			Quantized:      make([]byte, ringLength),
			quantizedStart: rng.Intn(ringLength - blockSize),
			pkt:            make([]byte, (packetSymbols+7)>>3),
		}
		for index := range decoder.Quantized {
			decoder.Quantized[index] = byte(rng.Intn(2))
		}
		candidate := rng.Intn(blockSize + 1)
		got := make([]byte, len(decoder.pkt))
		rng.Read(got)
		want := append([]byte(nil), got...)
		for symbol := 0; symbol < packetSymbols; symbol++ {
			packetByte := symbol >> 3
			physical := (decoder.quantizedStart + candidate + symbol*symbolLength) % ringLength
			want[packetByte] = want[packetByte]<<1 | decoder.Quantized[physical]
		}
		if !decoder.PacketBytesAt(candidate, got) {
			t.Fatalf("iteration %d rejected valid candidate", iteration)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("iteration %d projection mismatch: got %x want %x", iteration, got, want)
		}
	}
}

func TestParseCandidatesOmitsBitsForBytesOnlyParsers(t *testing.T) {
	decoder := newParserOrderTestDecoder()
	var received []Data
	decoder.parseCandidates([]int{0}, []Parser{&decoderBytesOnlyTestParser{received: &received}}, nil)

	if len(received) != 1 {
		t.Fatalf("received %d packets, want 1", len(received))
	}
	if received[0].Idx != 0 || !bytes.Equal(received[0].Bytes, []byte{0}) {
		t.Fatalf("packet = %+v, want index 0 and one zero byte", received[0])
	}
	if received[0].Bits != "" {
		t.Fatalf("bits = %q, want omitted projection", received[0].Bits)
	}
}

func TestParseCandidatesPreservesBitsForMixedParsers(t *testing.T) {
	decoder := newParserOrderTestDecoder()
	var bytesOnlyPackets, ordinaryPackets []Data
	parsers := []Parser{
		&decoderBytesOnlyTestParser{received: &bytesOnlyPackets},
		&decoderPacketTestParser{received: &ordinaryPackets},
	}
	decoder.parseCandidates([]int{0}, parsers, nil)

	for name, packets := range map[string][]Data{
		"bytes-only": bytesOnlyPackets,
		"ordinary":   ordinaryPackets,
	} {
		if len(packets) != 1 || packets[0].Bits != "00000000" || !bytes.Equal(packets[0].Bytes, []byte{0}) {
			t.Fatalf("%s packets = %+v, want full shared packet projection", name, packets)
		}
	}
}

type parserFanoutEmitter struct {
	id   uint32
	emit bool
}

func (p parserFanoutEmitter) emitChannel(messages chan Message) {
	if p.emit {
		messages <- decoderTestMessage{id: p.id}
	}
}

func (p parserFanoutEmitter) appendSlice(messages []Message) []Message {
	if !p.emit {
		return messages
	}
	return append(messages, decoderTestMessage{id: p.id})
}

func runParserFanoutLegacy(parsers []parserFanoutEmitter) {
	messages := make(chan Message)
	go func() {
		for _, parser := range parsers {
			parser.emitChannel(messages)
		}
		close(messages)
	}()
	for range messages {
	}
}

func runParserFanoutSequential(parsers []parserFanoutEmitter) {
	var messages []Message
	for _, parser := range parsers {
		messages = parser.appendSlice(messages)
	}
}

func benchmarkParserFanout(b *testing.B, emit bool, run func([]parserFanoutEmitter)) {
	parsers := []parserFanoutEmitter{{id: 1, emit: emit}, {id: 2, emit: emit}}
	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		run(parsers)
	}
}

func BenchmarkParserFanoutLegacyNoMessages(b *testing.B) {
	benchmarkParserFanout(b, false, runParserFanoutLegacy)
}

func BenchmarkParserFanoutSynchronousNoMessages(b *testing.B) {
	benchmarkParserFanout(b, false, runParserFanoutSequential)
}

func BenchmarkParserFanoutLegacyTwoMessages(b *testing.B) {
	benchmarkParserFanout(b, true, runParserFanoutLegacy)
}

func BenchmarkParserFanoutSynchronousTwoMessages(b *testing.B) {
	benchmarkParserFanout(b, true, runParserFanoutSequential)
}

func TestQuantizedRingMatchesShift(t *testing.T) {
	cfg := testDecoderConfig()
	rng := rand.New(rand.NewSource(0x1d00))
	decoder := Decoder{Cfg: cfg, Quantized: make([]byte, cfg.BufferLength)}
	reference := make([]byte, cfg.BufferLength)
	block := make([]byte, cfg.BlockSize)
	packedGot := make([]byte, (cfg.BlockSize+cfg.PreambleLength+7)/8)
	packedWant := make([]byte, len(packedGot))

	for iteration := 0; iteration < 64; iteration++ {
		for idx := range block {
			block[idx] = byte(rng.Intn(2))
		}
		copy(reference, reference[cfg.BlockSize:])
		copy(reference[cfg.PacketLength:], block)
		decoder.appendQuantized(block)

		for idx, want := range reference {
			if got := decoder.quantizedAt(idx); got != want {
				t.Fatalf("iteration %d: decision %d = %d, want %d", iteration, idx, got, want)
			}
		}

		packQuantizedRing(packedGot, decoder.Quantized, decoder.quantizedStart)
		packQuantized(packedWant, reference)
		if !bytes.Equal(packedGot, packedWant) {
			t.Fatalf("iteration %d: packed ring differs from shifted reference", iteration)
		}
	}
}

func TestDirectQuantizedRingMatchesAppend(t *testing.T) {
	cfg := testDecoderConfig()
	reference := Decoder{Cfg: cfg, Quantized: make([]byte, cfg.BufferLength)}
	backing := make([]byte, cfg.BufferLength+cfg.BlockSize)
	direct := Decoder{Cfg: cfg, quantizedBacking: backing}
	direct.Quantized = backing[:cfg.BufferLength:cfg.BufferLength]
	if !direct.ownsQuantizedRing() {
		t.Fatal("decoder-owned quantized ring was not recognized")
	}
	if cap(direct.Quantized) != len(direct.Quantized) {
		t.Fatalf("exported Quantized capacity = %d, want %d", cap(direct.Quantized), len(direct.Quantized))
	}

	rng := rand.New(rand.NewSource(0xd1ec7))
	block := make([]byte, cfg.BlockSize)
	wrapped := 0
	for iteration := 0; iteration < 223*2; iteration++ {
		for idx := range block {
			block[idx] = byte(rng.Intn(2))
		}
		reference.appendQuantized(block)
		output, nextStart, tail := direct.nextQuantizedOutput()
		copy(output, block)
		if tail+len(output) > len(direct.Quantized) {
			wrapped++
		}
		direct.commitQuantizedOutput(nextStart, tail, len(output))

		if direct.quantizedStart != reference.quantizedStart {
			t.Fatalf("iteration %d: start=%d, want %d", iteration, direct.quantizedStart, reference.quantizedStart)
		}
		if !bytes.Equal(direct.Quantized, reference.Quantized) {
			t.Fatalf("iteration %d: physical ring differs", iteration)
		}
		for logical := range reference.Quantized {
			if got := direct.quantizedAt(logical); got != reference.quantizedAt(logical) {
				t.Fatalf("iteration %d: logical decision %d differs", iteration, logical)
			}
		}
	}
	if wrapped == 0 || wrapped == 223*2 {
		t.Fatalf("cross-boundary writes=%d, want both wrapped and in-ring writes", wrapped)
	}
}

func referenceSlice(d Decoder, indices []int) (pkts []Data) {
	for _, qIdx := range indices {
		if qIdx > d.Cfg.BlockSize {
			continue
		}
		for pIdx := 0; pIdx < d.Cfg.PacketSymbols; pIdx++ {
			d.pkt[pIdx>>3] <<= 1
			d.pkt[pIdx>>3] |= d.quantizedAt(qIdx + pIdx*d.Cfg.SymbolLength)
		}
		data := NewData(d.pkt)
		data.Idx = qIdx
		pkts = append(pkts, data)
	}
	return pkts
}

func TestSliceMatchesReferenceAcrossRing(t *testing.T) {
	cfg := testDecoderConfig()
	rng := rand.New(rand.NewSource(0x51ce))
	quantized := make([]byte, cfg.BufferLength)
	for idx := range quantized {
		quantized[idx] = byte(rng.Intn(2))
	}
	indices := []int{0, 1, cfg.BlockSize / 2, cfg.BlockSize, cfg.BlockSize + 1}

	for _, packetSymbols := range []int{0, 1, 7, 8, 9, 95, 96, 116, 736} {
		cfg.PacketSymbols = packetSymbols
		for _, start := range []int{0, 1, cfg.BufferLength - 1, cfg.BufferLength - cfg.BlockSize, cfg.PacketLength % cfg.BufferLength} {
			gotDecoder := Decoder{Cfg: cfg, Quantized: quantized, quantizedStart: start, pkt: make([]byte, (cfg.PacketSymbols+7)>>3)}
			wantDecoder := gotDecoder
			wantDecoder.pkt = make([]byte, len(gotDecoder.pkt))
			got := gotDecoder.Slice(indices)
			want := referenceSlice(wantDecoder, indices)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("symbols %d start %d: packets differ", packetSymbols, start)
			}
		}
	}
}

func BenchmarkDecoderFilter(b *testing.B) {
	cfg := testDecoderConfig()
	rng := rand.New(rand.NewSource(0x1d00))
	input := make([]float64, cfg.BlockSize+cfg.SymbolLength)
	for idx := range input {
		input[idx] = rng.Float64() * 2
	}
	output := make([]byte, cfg.BlockSize)
	decoder := Decoder{Cfg: cfg}

	b.ReportAllocs()
	b.SetBytes(int64(len(input) * 8))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		decoder.Filter(input, output)
	}
}

func TestSignalHistoryRingMatchesShift(t *testing.T) {
	tests := []struct {
		name       string
		cfg        PacketConfig
		iterations int
	}{
		{
			name: "small non-divisible history",
			cfg: PacketConfig{
				BlockSize:    8,
				SymbolLength: 4,
				PacketLength: 21,
				BufferLength: 29,
			},
			iterations: 16,
		},
		{name: "combined IDM/R900 configuration", cfg: testDecoderConfig(), iterations: 256},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := test.cfg
			rng := rand.New(rand.NewSource(0x519a1))
			decoder := Decoder{Cfg: cfg, demod: NewMagLUT(), signalHistoryOverlap: cfg.SymbolLength * 2}
			decoder.allocateSignalHistory()
			reference := make([]float64, cfg.BufferLength)
			block := make([]float64, cfg.BlockSize)
			input := make([]byte, cfg.BlockSize*2)
			lut := NewMagLUT()

			for idx := range reference {
				if got := decoder.SignalAt(idx); got != 0 {
					t.Fatalf("startup signal %d = %v, want 0", idx, got)
				}
			}

			for iteration := 0; iteration < test.iterations; iteration++ {
				for idx := range input {
					input[idx] = byte(rng.Intn(256))
				}
				lut.Execute(input, block)
				copy(reference, reference[cfg.BlockSize:])
				copy(reference[cfg.PacketLength:], block)
				decoder.appendSignal(input)

				for idx, want := range reference {
					if got := decoder.SignalAt(idx); got != want {
						t.Fatalf("iteration %d: signal %d = %v, want %v", iteration, idx, got, want)
					}
				}
				windowLength := cfg.SymbolLength * 2
				for _, idx := range []int{0, 1, cfg.BlockSize - windowLength, cfg.BlockSize - 1, cfg.PacketLength} {
					if idx+windowLength > len(reference) {
						continue
					}
					got := decoder.SignalWindow(idx, windowLength)
					for offset, want := range reference[idx : idx+windowLength] {
						if got[offset] != want {
							t.Fatalf("iteration %d: window %d sample %d = %v, want %v", iteration, idx, offset, got[offset], want)
						}
					}
				}
				wantWindow := reference[cfg.PacketLength-cfg.SymbolLength:]
				for idx, want := range wantWindow {
					if got := decoder.Signal[idx]; got != want {
						t.Fatalf("iteration %d: filter signal %d = %v, want %v", iteration, idx, got, want)
					}
				}
			}
		})
	}
}

func BenchmarkSignalHistoryCopy(b *testing.B) {
	cfg := testDecoderConfig()
	lut := NewMagLUT()
	input := make([]byte, cfg.BlockSize*2)
	signal := make([]float64, cfg.BlockSize+cfg.SymbolLength)
	history := make([]float64, cfg.BufferLength)
	historyStart := 0

	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		copy(signal, signal[cfg.BlockSize:])
		lut.Execute(input, signal[cfg.SymbolLength:])
		historyStart += cfg.BlockSize
		if historyStart >= len(history) {
			historyStart -= len(history)
		}
		tail := historyStart + cfg.PacketLength
		if tail >= len(history) {
			tail -= len(history)
		}
		block := signal[cfg.SymbolLength:]
		first := len(block)
		if remaining := len(history) - tail; remaining < first {
			first = remaining
		}
		copy(history[tail:], block[:first])
		copy(history, block[first:])
	}
}

func BenchmarkSignalHistoryBlocks(b *testing.B) {
	cfg := testDecoderConfig()
	decoder := Decoder{Cfg: cfg, demod: NewMagLUT(), signalHistoryOverlap: cfg.SymbolLength * 2}
	decoder.allocateSignalHistory()
	input := make([]byte, cfg.BlockSize*2)

	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		decoder.appendSignal(input)
	}
}

var benchmarkSignalSum float64

func benchmarkSignalCorrelations(b *testing.B, contiguous bool) {
	cfg := testDecoderConfig()
	decoder := Decoder{Cfg: cfg, signalHistoryOverlap: cfg.SymbolLength * 2}
	decoder.allocateSignalHistory()
	rng := rand.New(rand.NewSource(0x519a1))
	for idx := range decoder.signalBacking {
		decoder.signalBacking[idx] = rng.Float64()
	}
	windowLength := cfg.SymbolLength * 2

	b.ReportAllocs()
	b.ResetTimer()
	var sum float64
	for iteration := 0; iteration < b.N; iteration++ {
		for decision := 0; decision < 42; decision++ {
			idx := cfg.PreambleLength - cfg.SymbolLength + decision*windowLength
			if contiguous {
				for _, value := range decoder.SignalWindow(idx, windowLength) {
					sum += value
				}
				continue
			}
			for offset := 0; offset < windowLength; offset++ {
				sum += decoder.SignalAt(idx + offset)
			}
		}
	}
	benchmarkSignalSum = sum
}

func BenchmarkSignalHistorySamples(b *testing.B) {
	benchmarkSignalCorrelations(b, false)
}

func BenchmarkSignalHistoryWindows(b *testing.B) {
	benchmarkSignalCorrelations(b, true)
}

func BenchmarkQuantizedHistoryShiftAndPack(b *testing.B) {
	cfg := testDecoderConfig()
	quantized := make([]byte, cfg.BufferLength)
	block := make([]byte, cfg.BlockSize)
	packed := make([]byte, (cfg.BlockSize+cfg.PreambleLength+7)/8)

	b.ReportAllocs()
	b.SetBytes(int64(cfg.BufferLength + cfg.BlockSize))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		copy(quantized, quantized[cfg.BlockSize:])
		copy(quantized[cfg.PacketLength:], block)
		packQuantized(packed, quantized)
	}
}

func BenchmarkQuantizedHistoryRingAndPack(b *testing.B) {
	cfg := testDecoderConfig()
	decoder := Decoder{Cfg: cfg, Quantized: make([]byte, cfg.BufferLength)}
	block := make([]byte, cfg.BlockSize)
	packed := make([]byte, (cfg.BlockSize+cfg.PreambleLength+7)/8)

	b.ReportAllocs()
	b.SetBytes(int64(cfg.BlockSize))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		decoder.appendQuantized(block)
		packQuantizedRing(packed, decoder.Quantized, decoder.quantizedStart)
	}
}

func BenchmarkQuantizedHistoryAppend(b *testing.B) {
	cfg := testDecoderConfig()
	decoder := Decoder{Cfg: cfg, Quantized: make([]byte, cfg.BufferLength)}
	block := make([]byte, cfg.BlockSize)

	b.ReportAllocs()
	b.SetBytes(int64(cfg.BlockSize))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		decoder.appendQuantized(block)
	}
}

var benchmarkQuantizedStart int

func BenchmarkQuantizedHistoryDirectCommit(b *testing.B) {
	cfg := testDecoderConfig()
	backing := make([]byte, cfg.BufferLength+cfg.BlockSize)
	decoder := Decoder{Cfg: cfg, quantizedBacking: backing}
	decoder.Quantized = backing[:cfg.BufferLength:cfg.BufferLength]

	b.ReportAllocs()
	b.SetBytes(int64(cfg.BlockSize))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		output, nextStart, tail := decoder.nextQuantizedOutput()
		decoder.commitQuantizedOutput(nextStart, tail, len(output))
	}
	benchmarkQuantizedStart = decoder.quantizedStart
}

var benchmarkSlicePackets []Data

func BenchmarkDecoderSlice(b *testing.B) {
	cfg := testDecoderConfig()
	cfg.PacketSymbols = 736
	decoder := Decoder{
		Cfg:            cfg,
		Quantized:      make([]byte, cfg.BufferLength),
		quantizedStart: cfg.BufferLength - 7919,
		pkt:            make([]byte, (cfg.PacketSymbols+7)>>3),
	}
	rng := rand.New(rand.NewSource(0x51ce))
	for idx := range decoder.Quantized {
		decoder.Quantized[idx] = byte(rng.Intn(2))
	}
	indices := []int{17, cfg.BlockSize / 2, cfg.BlockSize}

	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		benchmarkSlicePackets = decoder.Slice(indices)
	}
}

func BenchmarkDecoderSliceBytesOnly(b *testing.B) {
	cfg := testDecoderConfig()
	cfg.PacketSymbols = 736
	decoder := Decoder{
		Cfg:            cfg,
		Quantized:      make([]byte, cfg.BufferLength),
		quantizedStart: cfg.BufferLength - 7919,
		pkt:            make([]byte, (cfg.PacketSymbols+7)>>3),
	}
	rng := rand.New(rand.NewSource(0x51ce))
	for idx := range decoder.Quantized {
		decoder.Quantized[idx] = byte(rng.Intn(2))
	}
	indices := []int{17, cfg.BlockSize / 2, cfg.BlockSize}

	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		benchmarkSlicePackets = decoder.slice(indices, false)
	}
}
