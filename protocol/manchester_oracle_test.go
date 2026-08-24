package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"testing"
)

const (
	manchesterFixtureVersion = "manchester-filter-fixture-v1"
	manchesterFixtureDigest  = "971999a8f7235d45c38418426f0555780ba378b810ea1ee3e641f873154fc2d4"
	manchesterFixtureBlocks  = 14
	manchesterFixtureBlock   = 8192
	manchesterFixtureChip    = 72
)

var (
	manchesterDecisionSink byte
	manchesterDigestSink   [sha256.Size]byte
)

var manchesterSupportedChipLengths = []int{8, 32, 40, 48, 56, 64, 72, 80, 88, 96}

type manchesterStateCheckpoint struct {
	Index     int
	LowerBits uint64
	UpperBits uint64
	Decision  byte
	NextLower uint64
	NextUpper uint64
}

type manchesterTrace struct {
	Checkpoints    []manchesterStateCheckpoint
	FinalLowerBits uint64
	FinalUpperBits uint64
}

// manchesterFilterExact042 is an independent test oracle frozen to the
// recurrence introduced by ce27b9d and retained by 0422675's bounds-check
// elimination. It deliberately does not call any production filtering helper.
func manchesterFilterExact042(output []byte, lowerInput, middleInput, upperInput []float64, lower, upper float64, checkpointEvery, checkpointLimit int) manchesterTrace {
	n := len(output)
	if len(lowerInput) < n || len(middleInput) < n || len(upperInput) < n {
		panic("manchester oracle: input shorter than output")
	}
	trace := manchesterTrace{}
	if checkpointLimit > 0 {
		trace.Checkpoints = make([]manchesterStateCheckpoint, 0, checkpointLimit)
	}
	for idx := range output {
		f := lower - upper
		decision := 1 - byte(math.Float64bits(f)>>63)
		output[idx] = decision
		preLower := math.Float64bits(lower)
		preUpper := math.Float64bits(upper)
		lower += middleInput[idx] - lowerInput[idx]
		upper += upperInput[idx] - middleInput[idx]
		if checkpointEvery > 0 && idx%checkpointEvery == 0 && len(trace.Checkpoints) < checkpointLimit {
			trace.Checkpoints = append(trace.Checkpoints, manchesterStateCheckpoint{
				Index: idx, LowerBits: preLower, UpperBits: preUpper,
				Decision: decision, NextLower: math.Float64bits(lower), NextUpper: math.Float64bits(upper),
			})
		}
	}
	trace.FinalLowerBits = math.Float64bits(lower)
	trace.FinalUpperBits = math.Float64bits(upper)
	return trace
}

func manchesterExact042(input []float64, output []byte, chipLength int, checkpointEvery, checkpointLimit int) manchesterTrace {
	if chipLength < 0 || len(input) < len(output)+chipLength*2 {
		panic("manchester oracle: signal shorter than output plus symbol")
	}
	var lower, upper float64
	for idx := 0; idx < chipLength; idx++ {
		lower += input[idx]
		upper += input[idx+chipLength]
	}
	n := len(output)
	return manchesterFilterExact042(
		output,
		input[:n],
		input[chipLength:chipLength+n],
		input[chipLength*2:chipLength*2+n],
		lower,
		upper,
		checkpointEvery,
		checkpointLimit,
	)
}

func compareManchesterExact(t testing.TB, label string, input []float64, chipLength int, got []byte) {
	t.Helper()
	want := make([]byte, len(got))
	trace := manchesterExact042(input, want, chipLength, 1024, 12)
	if bytes.Equal(got, want) {
		return
	}
	details := make([]string, 0, 8)
	for idx := range want {
		if got[idx] != want[idx] && len(details) < cap(details) {
			checkpoint := manchesterCheckpointAt042(input, chipLength, idx)
			details = append(details, fmt.Sprintf("sample=%d got=%d want=%d state=%+v", idx, got[idx], want[idx], checkpoint))
		}
	}
	t.Fatalf("%s: Manchester decisions differ (%v); bounded-reference-checkpoints=%v final=(%016x,%016x)",
		label, details, trace.Checkpoints, trace.FinalLowerBits, trace.FinalUpperBits)
}

func manchesterCheckpointAt042(input []float64, chipLength, target int) manchesterStateCheckpoint {
	var lower, upper float64
	for idx := 0; idx < chipLength; idx++ {
		lower += input[idx]
		upper += input[idx+chipLength]
	}
	for idx := 0; idx <= target; idx++ {
		f := lower - upper
		checkpoint := manchesterStateCheckpoint{
			Index: idx, LowerBits: math.Float64bits(lower), UpperBits: math.Float64bits(upper),
			Decision: 1 - byte(math.Float64bits(f)>>63),
		}
		lower += input[idx+chipLength] - input[idx]
		upper += input[idx+chipLength*2] - input[idx+chipLength]
		checkpoint.NextLower = math.Float64bits(lower)
		checkpoint.NextUpper = math.Float64bits(upper)
		if idx == target {
			return checkpoint
		}
	}
	panic("unreachable Manchester checkpoint")
}

type manchesterXorShift64 struct{ state uint64 }

func (x *manchesterXorShift64) next() uint64 {
	x.state ^= x.state << 13
	x.state ^= x.state >> 7
	x.state ^= x.state << 17
	return x.state
}

func manchesterProductionSignal(seed uint64, count int) ([]byte, []float64) {
	if seed == 0 {
		panic("Manchester fixture seed must be nonzero")
	}
	raw := make([]byte, count*2)
	rng := manchesterXorShift64{state: seed}
	for idx := range raw {
		raw[idx] = byte(rng.next() >> 56)
	}
	lut := NewMagLUT()
	signal := make([]float64, count)
	for idx := range signal {
		signal[idx] = lut[raw[idx*2]] + lut[raw[idx*2+1]]
	}
	return raw, signal
}

type manchesterFixture struct {
	Raw       []byte
	Signal    []float64
	Windows   [][]float64
	Decisions [][]byte
	Digest    string
}

func newManchesterFixture() manchesterFixture {
	count := manchesterFixtureBlocks*manchesterFixtureBlock + manchesterFixtureChip*2
	raw, signal := manchesterProductionSignal(0x6d616e6368657374, count)
	fixture := manchesterFixture{
		Raw:       raw,
		Signal:    signal,
		Windows:   make([][]float64, manchesterFixtureBlocks),
		Decisions: make([][]byte, manchesterFixtureBlocks),
	}
	h := sha256.New()
	h.Write([]byte(manchesterFixtureVersion))
	var encoded [8]byte
	for _, value := range []uint64{manchesterFixtureBlocks, manchesterFixtureBlock, manchesterFixtureChip} {
		binary.LittleEndian.PutUint64(encoded[:], value)
		h.Write(encoded[:])
	}
	h.Write(raw)
	for _, value := range signal {
		binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(value))
		h.Write(encoded[:])
	}
	for block := 0; block < manchesterFixtureBlocks; block++ {
		start := block * manchesterFixtureBlock
		window := signal[start : start+manchesterFixtureBlock+manchesterFixtureChip*2]
		decisions := make([]byte, manchesterFixtureBlock)
		manchesterExact042(window, decisions, manchesterFixtureChip, 0, 0)
		fixture.Windows[block] = window
		fixture.Decisions[block] = decisions
		h.Write(decisions)
	}
	fixture.Digest = hex.EncodeToString(h.Sum(nil))
	return fixture
}

func requireManchesterFixture(t testing.TB) manchesterFixture {
	t.Helper()
	fixture := newManchesterFixture()
	if fixture.Digest != manchesterFixtureDigest {
		t.Fatalf("Manchester fixture digest = %s, want %s", fixture.Digest, manchesterFixtureDigest)
	}
	decoder := Decoder{Cfg: PacketConfig{ChipLength: manchesterFixtureChip, SymbolLength: manchesterFixtureChip * 2}}
	got := make([]byte, manchesterFixtureBlock)
	for block := range fixture.Windows {
		decoder.Filter(fixture.Windows[block], got)
		if !bytes.Equal(got, fixture.Decisions[block]) {
			compareManchesterExact(t, fmt.Sprintf("fixture block %d", block), fixture.Windows[block], manchesterFixtureChip, got)
		}
	}
	return fixture
}

func TestManchesterExactOracleSupportedGeometries(t *testing.T) {
	lengths := []int{0, 1, 2, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 8191, 8192, 8193}
	for _, chipLength := range manchesterSupportedChipLengths {
		for _, outputLength := range lengths {
			name := fmt.Sprintf("chip=%d/n=%d", chipLength, outputLength)
			t.Run(name, func(t *testing.T) {
				_, input := manchesterProductionSignal(uint64(chipLength*65537+outputLength+1), outputLength+chipLength*2)
				const guard = 32
				backing := bytes.Repeat([]byte{0xa5}, outputLength+guard*2)
				output := backing[guard : guard+outputLength]
				inputBits := digestManchesterFloatBits(input)
				decoder := Decoder{Cfg: PacketConfig{ChipLength: chipLength, SymbolLength: chipLength * 2}}
				decoder.Filter(input, output)
				compareManchesterExact(t, name, input, chipLength, output)
				if !allManchesterBytes(backing[:guard], 0xa5) || !allManchesterBytes(backing[guard+outputLength:], 0xa5) {
					t.Fatalf("%s: output canary changed", name)
				}
				if got := digestManchesterFloatBits(input); got != inputBits {
					t.Fatalf("%s: input changed: %x -> %x", name, inputBits, got)
				}
			})
		}
	}
}

func TestManchesterExactOracleSpecialValues(t *testing.T) {
	patterns := []uint64{
		0x0000000000000000, 0x8000000000000000,
		0x0000000000000001, 0x8000000000000001,
		math.Float64bits(1), math.Float64bits(-1),
		math.Float64bits(math.MaxFloat64), math.Float64bits(-math.MaxFloat64),
		math.Float64bits(math.Inf(1)), math.Float64bits(math.Inf(-1)),
		0x7ff8000000000001, 0xfff80000000000a5,
	}
	const n = 96
	lowerInput := make([]float64, n)
	middleInput := make([]float64, n)
	upperInput := make([]float64, n)
	for idx := 0; idx < n; idx++ {
		lowerInput[idx] = math.Float64frombits(patterns[idx%len(patterns)])
		middleInput[idx] = math.Float64frombits(patterns[(idx*5+1)%len(patterns)])
		upperInput[idx] = math.Float64frombits(patterns[(idx*7+3)%len(patterns)])
	}
	initial := [][2]uint64{
		{0, 0}, {1 << 63, 0}, {0, 1 << 63},
		{math.Float64bits(1), math.Float64bits(-1)},
		{0x7ff8000000000001, 0xfff80000000000a5},
		{math.Float64bits(math.Inf(1)), math.Float64bits(math.Inf(-1))},
	}
	for caseIdx, pair := range initial {
		got := make([]byte, n)
		want := make([]byte, n)
		filterManchester(got, lowerInput, middleInput, upperInput, math.Float64frombits(pair[0]), math.Float64frombits(pair[1]))
		trace := manchesterFilterExact042(want, lowerInput, middleInput, upperInput, math.Float64frombits(pair[0]), math.Float64frombits(pair[1]), 16, 8)
		if !bytes.Equal(got, want) {
			t.Fatalf("special case %d differs: got=%x want=%x checkpoints=%v", caseIdx, got[:16], want[:16], trace.Checkpoints)
		}
	}
}

func TestManchesterDecoderFilterSpecialValueInitialization(t *testing.T) {
	const (
		chipLength   = 8
		outputLength = 33
	)
	patterns := []uint64{
		0, 1 << 63, 1, 1<<63 | 1,
		math.Float64bits(1), math.Float64bits(-1),
		math.Float64bits(1e100), math.Float64bits(-1e100),
	}
	input := make([]float64, outputLength+chipLength*2)
	for idx := range input {
		input[idx] = math.Float64frombits(patterns[(idx*7+3)%len(patterns)])
	}
	got := make([]byte, outputLength)
	decoder := Decoder{Cfg: PacketConfig{ChipLength: chipLength, SymbolLength: chipLength * 2}}
	decoder.Filter(input, got)
	compareManchesterExact(t, "special-value initialization", input, chipLength, got)
}

func TestManchesterDecoderStartupZeroMagnitudeHistory(t *testing.T) {
	const (
		chipLength   = manchesterFixtureChip
		outputLength = manchesterFixtureBlock
	)
	_, current := manchesterProductionSignal(0x7374617274757031, outputLength)
	input := make([]float64, outputLength+chipLength*2)
	copy(input[chipLength*2:], current)
	got := make([]byte, outputLength)
	decoder := Decoder{Cfg: PacketConfig{ChipLength: chipLength, SymbolLength: chipLength * 2}}
	decoder.Filter(input, got)
	compareManchesterExact(t, "startup zero magnitude history", input, chipLength, got)
}

func TestManchesterSignedZeroDecisions(t *testing.T) {
	tests := []struct {
		name         string
		lower, upper uint64
		want         byte
	}{
		{"positive-minus-positive", 0, 0, 1},
		{"negative-minus-positive", 1 << 63, 0, 0},
		{"positive-minus-negative", 0, 1 << 63, 1},
		{"negative-minus-negative", 1 << 63, 1 << 63, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := []byte{0xff}
			want := []byte{0xff}
			zero := []float64{0}
			filterManchester(got, zero, zero, zero, math.Float64frombits(test.lower), math.Float64frombits(test.upper))
			manchesterFilterExact042(want, zero, zero, zero, math.Float64frombits(test.lower), math.Float64frombits(test.upper), 1, 1)
			if got[0] != test.want || want[0] != test.want {
				t.Fatalf("decision got=%d oracle=%d want=%d", got[0], want[0], test.want)
			}
		})
	}
}

func TestManchesterFilterPanicBeforeWrite(t *testing.T) {
	const n = 17
	full := make([]float64, n)
	short := make([]float64, n-1)
	tests := []struct {
		name                 string
		lower, middle, upper []float64
	}{
		{"lower", short, full, full},
		{"middle", full, short, full},
		{"upper", full, full, short},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := bytes.Repeat([]byte{0xa5}, n)
			before := append([]byte(nil), output...)
			if !panicsManchester(func() { filterManchester(output, test.lower, test.middle, test.upper, 0, 0) }) {
				t.Fatal("short input did not panic")
			}
			if !bytes.Equal(output, before) {
				t.Fatalf("output changed before panic: got=%x want=%x", output, before)
			}
		})
	}
	filterManchester(nil, nil, nil, nil, math.Float64frombits(1<<63), 0)
}

func TestManchesterDecoderFilterPanicBeforeWrite(t *testing.T) {
	const (
		chipLength   = 8
		outputLength = 17
	)
	required := outputLength + chipLength*2
	lengths := []int{0, chipLength - 1, chipLength, chipLength*2 - 1, required - 1}
	decoder := Decoder{Cfg: PacketConfig{ChipLength: chipLength, SymbolLength: chipLength * 2}}
	for _, inputLength := range lengths {
		t.Run(fmt.Sprintf("input=%d", inputLength), func(t *testing.T) {
			output := bytes.Repeat([]byte{0xa5}, outputLength)
			before := append([]byte(nil), output...)
			if !panicsManchester(func() { decoder.Filter(make([]float64, inputLength), output) }) {
				t.Fatal("short public Filter input did not panic")
			}
			if !bytes.Equal(output, before) {
				t.Fatalf("public Filter output changed before panic: got=%x want=%x", output, before)
			}
		})
	}
}

func TestManchesterSequentialBlockBoundaries(t *testing.T) {
	const (
		blockSize = 257
		blocks    = 64
	)
	for _, chipLength := range manchesterSupportedChipLengths {
		name := fmt.Sprintf("chip=%d", chipLength)
		t.Run(name, func(t *testing.T) {
			_, signal := manchesterProductionSignal(uint64(0xb10c0000+chipLength), blocks*blockSize+chipLength*2)
			decoder := Decoder{Cfg: PacketConfig{ChipLength: chipLength, SymbolLength: chipLength * 2}}
			streamDigest := sha256.New()
			for block := 0; block < blocks; block++ {
				start := block * blockSize
				window := signal[start : start+blockSize+chipLength*2]
				got := make([]byte, blockSize)
				decoder.Filter(window, got)
				compareManchesterExact(t, fmt.Sprintf("%s/block=%d", name, block), window, chipLength, got)
				streamDigest.Write(got)
			}
			copy(manchesterDigestSink[:], streamDigest.Sum(nil))
		})
	}
}

func TestManchesterFixtureDigest(t *testing.T) {
	fixture := requireManchesterFixture(t)
	digestBytes, err := hex.DecodeString(fixture.Digest)
	if err != nil {
		t.Fatal(err)
	}
	copy(manchesterDigestSink[:], digestBytes)
}

func BenchmarkManchesterFilterFixture(b *testing.B) {
	fixture := requireManchesterFixture(b)
	decoder := Decoder{Cfg: PacketConfig{ChipLength: manchesterFixtureChip, SymbolLength: manchesterFixtureChip * 2}}
	output := make([]byte, manchesterFixtureBlock)
	b.ReportAllocs()
	b.ResetTimer()
	block := 0
	for idx := 0; idx < b.N; idx++ {
		decoder.Filter(fixture.Windows[block], output)
		block++
		if block == len(fixture.Windows) {
			block = 0
		}
	}
	b.StopTimer()
	manchesterDecisionSink = output[(b.N+len(output)-1)%len(output)]
	manchesterDigestSink = sha256.Sum256(output)
}

func digestManchesterFloatBits(values []float64) [sha256.Size]byte {
	h := sha256.New()
	var encoded [8]byte
	for _, value := range values {
		binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(value))
		h.Write(encoded[:])
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func allManchesterBytes(values []byte, want byte) bool {
	for _, value := range values {
		if value != want {
			return false
		}
	}
	return true
}

func panicsManchester(fn func()) (panicked bool) {
	defer func() {
		panicked = recover() != nil
	}()
	fn()
	return false
}
