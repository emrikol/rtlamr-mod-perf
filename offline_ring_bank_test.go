package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/bemasher/rtlamr/internal/dutyscheduler"
	"github.com/bemasher/rtlamr/protocol"
)

const offlineRingBytes = 4 << 20

type offlineRingExecutor string

const (
	offlineUserspaceBlock offlineRingExecutor = "userspace-block"
	offlineUserspaceBatch offlineRingExecutor = "userspace-batch"
	offlineKernelPlan     offlineRingExecutor = "kernel-plan"
)

type offlineRingBankResult struct {
	CorpusSHA256        string              `json:"corpus_sha256"`
	WakeHalfWidthMS     int                 `json:"wake_half_width_ms"`
	Executor            offlineRingExecutor `json:"executor"`
	BatchBlocks         int                 `json:"batch_blocks"`
	BatchBytes          int                 `json:"batch_bytes"`
	RingSlots           int                 `json:"ring_slots"`
	RetainBatches       int                 `json:"retain_batches"`
	RetainedBlocks      int                 `json:"retained_blocks"`
	BatchLatencyMS      float64             `json:"batch_latency_ms"`
	InputBlocks         int                 `json:"input_blocks"`
	Messages            int                 `json:"messages"`
	DecodedBlocks       uint64              `json:"decoded_blocks"`
	ReplayedBlocks      uint64              `json:"replayed_blocks"`
	EffectiveDSPBlocks  uint64              `json:"effective_dsp_blocks"`
	SkippedBlocks       uint64              `json:"skipped_blocks"`
	Rebuilds            uint64              `json:"rebuilds"`
	DecisionEvals       int                 `json:"decision_evaluations"`
	USBBatches          int                 `json:"usb_batches"`
	UserspaceBatches    int                 `json:"userspace_batches"`
	AutoRecycled        int                 `json:"kernel_auto_recycled_batches"`
	EffectiveDSPRatio   float64             `json:"effective_dsp_ratio"`
	UserspaceBatchRatio float64             `json:"userspace_batch_ratio"`
	ReplayElapsedMS     float64             `json:"replay_elapsed_ms"`
	Valid               bool                `json:"valid"`
	Reason              string              `json:"reason,omitempty"`
	Rank                int                 `json:"rank,omitempty"`
}

type offlineRingBaseline struct {
	digests       []protocol.Digest
	messageBlocks []int
	blockBytes    int
	blocks        int
	config        protocol.PacketConfig
}

func offlineRingReceiver(t *testing.T, gated bool, retainedBlocks int) *Receiver {
	t.Helper()
	receiver := &Receiver{protocolNames: []string{"idm", "r900"}}
	var err error
	receiver.d, err = receiver.newProtocolDecoder()
	if err != nil {
		t.Fatal(err)
	}
	if gated {
		receiver.duty, err = newDutyRuntime("gated", receiver.d.Cfg, dutyTestSenders)
		if err != nil {
			t.Fatal(err)
		}
		if err = receiver.duty.useBorrowedCollar(retainedBlocks); err != nil {
			t.Fatal(err)
		}
	}
	return receiver
}

func offlineRingState() receiverRunState {
	return receiverRunState{
		prev:     make(map[protocol.Digest]bool),
		next:     make(map[protocol.Digest]bool),
		dutyPrev: make(map[protocol.Digest]bool),
		dutyNext: make(map[protocol.Digest]bool),
	}
}

func offlineRingDecodeBaseline(t *testing.T, input []byte) offlineRingBaseline {
	t.Helper()
	receiver := offlineRingReceiver(t, false, 0)
	blockBytes := receiver.d.Cfg.BlockSize2
	if len(input) == 0 || len(input)%blockBytes != 0 {
		t.Fatalf("offline bank corpus size %d is not block aligned to %d", len(input), blockBytes)
	}
	capture := &dutyCaptureEncoder{}
	encoder = capture
	state := offlineRingState()
	messageBlocks := make([]int, 0)
	for block, offset := 0, 0; offset < len(input); block, offset = block+1, offset+blockBytes {
		before := len(capture.digests)
		messages := receiver.d.Decode(input[offset : offset+blockBytes])
		if _, keepRunning := receiver.processDecodedMessages(&state, messages, 0, time.Time{}, true, false, false); !keepRunning {
			t.Fatalf("offline baseline failed at block %d: %v", block, receiver.err)
		}
		if len(capture.digests) != before {
			messageBlocks = append(messageBlocks, block)
		}
	}
	if len(capture.digests) == 0 {
		t.Fatal("offline bank corpus replay was vacuous")
	}
	return offlineRingBaseline{
		digests:       append([]protocol.Digest(nil), capture.digests...),
		messageBlocks: messageBlocks,
		blockBytes:    blockBytes,
		blocks:        len(input) / blockBytes,
		config:        receiver.d.Cfg,
	}
}

func offlineRingWakePlan(baseline offlineRingBaseline, halfWidth time.Duration) []bool {
	plan := make([]bool, baseline.blocks)
	guardBlocks := int((int64(baseline.config.SampleRate)*int64(halfWidth) + int64(time.Second)*int64(baseline.config.BlockSize) - 1) /
		(int64(time.Second) * int64(baseline.config.BlockSize)))
	if guardBlocks < 1 {
		guardBlocks = 1
	}
	warmup := dutyCollarBlockCount(baseline.config)
	if warmup > len(plan) {
		warmup = len(plan)
	}
	for block := 0; block < warmup; block++ {
		plan[block] = true
	}
	for _, messageBlock := range baseline.messageBlocks {
		first := messageBlock - guardBlocks
		if first < 0 {
			first = 0
		}
		last := messageBlock + guardBlocks
		if last >= len(plan) {
			last = len(plan) - 1
		}
		for block := first; block <= last; block++ {
			plan[block] = true
		}
	}
	return plan
}

func offlineRingCoarsenPlan(plan []bool, batchBlocks int) []bool {
	result := append([]bool(nil), plan...)
	for first := 0; first < len(result); first += batchBlocks {
		last := first + batchBlocks
		if last > len(result) {
			last = len(result)
		}
		awake := false
		for _, decode := range result[first:last] {
			awake = awake || decode
		}
		if awake {
			for block := first; block < last; block++ {
				result[block] = true
			}
		}
	}
	return result
}

func offlineRingAdvanceClock(duty *dutyRuntime) (time.Duration, time.Duration) {
	start := duty.sampleTime
	numerator := duty.blockSamples*int64(time.Second) + duty.sampleRemainder
	delta := numerator / duty.sampleRate
	duty.sampleRemainder = numerator % duty.sampleRate
	end := start + time.Duration(delta)
	duty.sampleTime = end
	return start, end
}

func offlineRingProjectedBatchEnd(duty *dutyRuntime, blocks int) time.Duration {
	numerator := int64(blocks)*duty.blockSamples*int64(time.Second) + duty.sampleRemainder
	return duty.sampleTime + time.Duration(numerator/duty.sampleRate)
}

func offlineRingBatchInventory(plan []bool, batchBlocks, retainBatches int, executor offlineRingExecutor) (usbBatches, userspaceBatches, autoRecycled int) {
	usbBatches = (len(plan) + batchBlocks - 1) / batchBlocks
	if executor != offlineKernelPlan {
		return usbBatches, usbBatches, 0
	}
	awake := make([]bool, usbBatches)
	for block, decode := range plan {
		if decode {
			awake[block/batchBlocks] = true
		}
	}
	delivered := append([]bool(nil), awake...)
	for batch := range awake {
		if !awake[batch] || (batch > 0 && awake[batch-1]) {
			continue
		}
		for prior := batch - retainBatches; prior < batch; prior++ {
			if prior >= 0 {
				delivered[prior] = true
			}
		}
	}
	for _, value := range delivered {
		if value {
			userspaceBatches++
		}
	}
	return usbBatches, userspaceBatches, usbBatches - userspaceBatches
}

func offlineRingRunCandidate(t *testing.T, input []byte, baseline offlineRingBaseline, executor offlineRingExecutor, batchBlocks int, halfWidth time.Duration, corpusHash string) offlineRingBankResult {
	t.Helper()
	batchBytes := batchBlocks * baseline.blockBytes
	ringSlots := offlineRingBytes / batchBytes
	if ringSlots < 4 {
		ringSlots = 4
	}
	collarBlocks := dutyCollarBlockCount(baseline.config)
	retainBatches := (collarBlocks + batchBlocks - 1) / batchBlocks
	retainedBlocks := retainBatches * batchBlocks
	result := offlineRingBankResult{
		CorpusSHA256:    corpusHash,
		WakeHalfWidthMS: int(halfWidth / time.Millisecond),
		Executor:        executor,
		BatchBlocks:     batchBlocks,
		BatchBytes:      batchBytes,
		RingSlots:       ringSlots,
		RetainBatches:   retainBatches,
		RetainedBlocks:  retainedBlocks,
		BatchLatencyMS: float64(int64(batchBlocks)*int64(baseline.config.BlockSize)*int64(time.Second)) /
			float64(int64(baseline.config.SampleRate)*int64(time.Millisecond)),
		InputBlocks: baseline.blocks,
		Messages:    len(baseline.digests),
	}
	if ringSlots < retainBatches+2 {
		result.Reason = fmt.Sprintf("ring slots %d cannot retain %d batches plus two inflight slots", ringSlots, retainBatches)
		return result
	}

	plan := offlineRingWakePlan(baseline, halfWidth)
	// A batch-level userspace decision deliberately tests the tempting but
	// lossy design that applies one decision to every block in an URB. The
	// kernel actuator instead drops only wholly asleep batches; delivered
	// batches retain the exact block-level plan so batching cannot change
	// decoder/debounce semantics.
	if executor == offlineUserspaceBatch {
		plan = offlineRingCoarsenPlan(plan, batchBlocks)
	}
	result.USBBatches, result.UserspaceBatches, result.AutoRecycled = offlineRingBatchInventory(plan, batchBlocks, retainBatches, executor)
	if executor == offlineUserspaceBlock {
		result.DecisionEvals = baseline.blocks
	} else {
		result.DecisionEvals = result.USBBatches
	}

	receiver := offlineRingReceiver(t, true, retainedBlocks)
	capture := &dutyCaptureEncoder{}
	encoder = capture
	state := offlineRingState()
	started := time.Now()
	for block, offset := 0, 0; offset < len(input); block, offset = block+1, offset+baseline.blockBytes {
		if executor == offlineUserspaceBatch && block%batchBlocks == 0 {
			remaining := baseline.blocks - block
			if remaining > batchBlocks {
				remaining = batchBlocks
			}
			batchStart := receiver.duty.sampleTime
			batchEnd := offlineRingProjectedBatchEnd(receiver.duty, remaining)
			_ = receiver.duty.scheduler.Advance(batchStart, batchEnd)
		}

		var start, end time.Duration
		if executor == offlineUserspaceBlock {
			start, end, _ = receiver.duty.beginBlock()
		} else {
			start, end = offlineRingAdvanceClock(receiver.duty)
		}
		decision := dutyscheduler.Decision{Decode: plan[block]}
		if receiver.duty.needsRebuild(decision) {
			if _, keepRunning := receiver.rebuildDutyDecoder(&state); !keepRunning {
				result.Reason = fmt.Sprintf("collar rebuild failed at block %d: %v", block, receiver.err)
				return result
			}
		}
		data := input[offset : offset+baseline.blockBytes]
		if decision.Decode {
			messages := receiver.d.Decode(data)
			if _, keepRunning := receiver.processDecodedMessages(&state, messages, end, time.Time{}, true, true, false); !keepRunning {
				result.Reason = fmt.Sprintf("decode failed at block %d: %v", block, receiver.err)
				return result
			}
		}
		receiver.duty.finishBlock(data, start, end, time.Time{}, decision)
	}
	if receiver.duty.wasSkipped {
		if _, keepRunning := receiver.rebuildDutyDecoder(&state); !keepRunning {
			result.Reason = fmt.Sprintf("final collar rebuild failed: %v", receiver.err)
			return result
		}
	}
	result.ReplayElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
	result.DecodedBlocks = receiver.duty.decodedBlocks
	result.ReplayedBlocks = receiver.duty.replayedBlocks
	result.EffectiveDSPBlocks = result.DecodedBlocks + result.ReplayedBlocks
	result.SkippedBlocks = receiver.duty.skippedBlocks
	result.Rebuilds = receiver.duty.rebuilds
	result.EffectiveDSPRatio = float64(result.EffectiveDSPBlocks) / float64(result.InputBlocks)
	result.UserspaceBatchRatio = float64(result.UserspaceBatches) / float64(result.USBBatches)
	if !reflect.DeepEqual(capture.digests, baseline.digests) {
		result.Reason = fmt.Sprintf("ordered message digests differ: baseline=%d candidate=%d", len(baseline.digests), len(capture.digests))
		return result
	}
	result.Valid = true
	return result
}

func offlineRingRank(results []offlineRingBankResult) {
	byWidth := make(map[int][]int)
	for idx := range results {
		if results[idx].Valid {
			byWidth[results[idx].WakeHalfWidthMS] = append(byWidth[results[idx].WakeHalfWidthMS], idx)
		}
	}
	for _, indices := range byWidth {
		sort.Slice(indices, func(left, right int) bool {
			a := results[indices[left]]
			b := results[indices[right]]
			if a.EffectiveDSPBlocks != b.EffectiveDSPBlocks {
				return a.EffectiveDSPBlocks < b.EffectiveDSPBlocks
			}
			if a.UserspaceBatches != b.UserspaceBatches {
				return a.UserspaceBatches < b.UserspaceBatches
			}
			if a.DecisionEvals != b.DecisionEvals {
				return a.DecisionEvals < b.DecisionEvals
			}
			if a.ReplayElapsedMS != b.ReplayElapsedMS {
				return a.ReplayElapsedMS < b.ReplayElapsedMS
			}
			if a.BatchBlocks != b.BatchBlocks {
				return a.BatchBlocks < b.BatchBlocks
			}
			return a.Executor < b.Executor
		})
		for rank, idx := range indices {
			results[idx].Rank = rank + 1
		}
	}
}

func TestOfflineRingCoarseningOnlyWidensWakePlan(t *testing.T) {
	plan := []bool{false, true, false, false, false, true, false}
	coarsened := offlineRingCoarsenPlan(plan, 4)
	want := []bool{true, true, true, true, true, true, true}
	if !reflect.DeepEqual(coarsened, want) {
		t.Fatalf("coarsened plan=%v, want %v", coarsened, want)
	}
	for block, decode := range plan {
		if decode && !coarsened[block] {
			t.Fatalf("coarsening cleared awake block %d", block)
		}
	}
}

func TestOfflineRingKernelPlanDeliversRecoveryCollar(t *testing.T) {
	// Five four-block batches, with the first wake in batch three. A two-batch
	// collar requires batches one and two plus the wake batch to reach userspace.
	plan := make([]bool, 20)
	plan[12] = true
	usb, userspace, recycled := offlineRingBatchInventory(plan, 4, 2, offlineKernelPlan)
	if usb != 5 || userspace != 3 || recycled != 2 {
		t.Fatalf("inventory usb=%d userspace=%d recycled=%d, want 5/3/2", usb, userspace, recycled)
	}
	usb, userspace, recycled = offlineRingBatchInventory(plan, 4, 2, offlineUserspaceBlock)
	if usb != 5 || userspace != 5 || recycled != 0 {
		t.Fatalf("userspace inventory usb=%d userspace=%d recycled=%d, want 5/5/0", usb, userspace, recycled)
	}
}

func TestOfflineKernelRingCandidateBank(t *testing.T) {
	path := os.Getenv("RTLAMR_OFFLINE_BANK_CORPUS")
	if path == "" {
		t.Skip("RTLAMR_OFFLINE_BANK_CORPUS is not set")
	}
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(input)
	corpusHash := hex.EncodeToString(digest[:])

	previousEncoder := encoder
	previousSingle := *single
	defer func() {
		encoder = previousEncoder
		*single = previousSingle
	}()
	*single = false

	baseline := offlineRingDecodeBaseline(t, input)
	wakeWidths := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 1500 * time.Millisecond}
	batchBlocks := []int{16, 32, 64}
	executors := []offlineRingExecutor{offlineUserspaceBlock, offlineUserspaceBatch, offlineKernelPlan}
	results := make([]offlineRingBankResult, 0, len(wakeWidths)*len(batchBlocks)*len(executors))
	for _, width := range wakeWidths {
		for _, blocks := range batchBlocks {
			for _, executor := range executors {
				results = append(results, offlineRingRunCandidate(t, input, baseline, executor, blocks, width, corpusHash))
			}
		}
	}
	offlineRingRank(results)

	valid := 0
	for _, result := range results {
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("OFFLINE_RING_BANK %s", encoded)
		if result.Valid {
			valid++
		}
	}
	if valid == 0 {
		t.Fatal("offline ring bank produced no valid candidate")
	}
}
