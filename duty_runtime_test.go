package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/bemasher/rtlamr/internal/dutyscheduler"
	"github.com/bemasher/rtlamr/protocol"
)

type dutyCaptureEncoder struct {
	digests []protocol.Digest
}

var dutyTestSenders = []uint64{1001, 2002, 3003}

func (e *dutyCaptureEncoder) Encode(value interface{}) error {
	message, ok := value.(protocol.LogMessage)
	if !ok {
		return nil
	}
	e.digests = append(e.digests, protocol.NewDigest(message.Message))
	return nil
}

func dutyTestConfig() protocol.PacketConfig {
	return protocol.PacketConfig{
		SampleRate:   2359296,
		BlockSize:    8192,
		BlockSize2:   16384,
		BufferLength: 114176,
	}
}

func TestDutyRuntimeSampleClockIsExactAndContiguous(t *testing.T) {
	runtime, err := newDutyRuntime("shadow", dutyTestConfig(), []uint64{1})
	if err != nil {
		t.Fatal(err)
	}
	var previous time.Duration
	const blocks = 1000000
	for block := int64(0); block < blocks; block++ {
		start, end, decision := runtime.beginBlock()
		if start != previous || end <= start || !decision.Decode {
			t.Fatalf("block %d start=%s end=%s previous=%s decision=%+v", block, start, end, previous, decision)
		}
		previous = end
	}
	want := time.Duration(blocks * int64(dutyTestConfig().BlockSize) * int64(time.Second) / int64(dutyTestConfig().SampleRate))
	if previous != want {
		t.Fatalf("sample clock=%s, want %s", previous, want)
	}
}

func TestDutyCollarOwnsBytesAndPreservesWrappedOrder(t *testing.T) {
	runtime, err := newDutyRuntime("shadow", dutyTestConfig(), []uint64{1})
	if err != nil {
		t.Fatal(err)
	}
	count := len(runtime.collar) + 7
	block := make([]byte, runtime.blockBytes)
	for idx := 0; idx < count; idx++ {
		for offset := range block {
			block[offset] = byte(idx)
		}
		start, end, decision := runtime.beginBlock()
		runtime.finishBlock(block, start, end, time.Unix(int64(idx), 0), decision)
		block[0] ^= 0xff
	}
	ordered := runtime.orderedCollar()
	if len(ordered) != len(runtime.collar) {
		t.Fatalf("collar count=%d, want %d", len(ordered), len(runtime.collar))
	}
	first := count - len(runtime.collar)
	for idx, entry := range ordered {
		want := byte(first + idx)
		if !bytes.Equal(entry.data, bytes.Repeat([]byte{want}, runtime.blockBytes)) {
			t.Fatalf("collar entry %d does not own ordered source bytes", idx)
		}
	}
}

func TestDutyRuntimeReportIsAtomicAndComplete(t *testing.T) {
	runtime, err := newDutyRuntime("shadow", dutyTestConfig(), []uint64{3, 1, 2})
	if err != nil {
		t.Fatal(err)
	}
	start, end, decision := runtime.beginBlock()
	runtime.finishBlock(make([]byte, runtime.blockBytes), start, end, time.Now(), decision)
	path := filepath.Join(t.TempDir(), "report.json")
	if err := runtime.writeReport(path); err != nil {
		t.Fatal(err)
	}
	if err := runtime.writeReport(path); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report dutyRuntimeReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != "rtlamr-duty-scheduler-live-v4" || report.Mode != dutyscheduler.ModeShadow || report.CaptureTarget != 0.995 || report.Confidence != 0.95 || report.MinimumAudit != 0.10 || report.DecodedBlocks != 1 || report.SkippedBlocks != 0 || report.RefreshBlocks != 0 || report.SchedulerState.Schema != dutyscheduler.StateSchema {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.SenderIDs) != 3 || report.SenderIDs[0] != 1 || report.SenderIDs[1] != 2 || report.SenderIDs[2] != 3 {
		t.Fatalf("sender inventory is not canonical: %v", report.SenderIDs)
	}
}

func TestShadowCheckpointPromotesToGatedWithRecovery(t *testing.T) {
	directory := t.TempDir()
	shadow, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := shadow.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	for block := 0; block < 10; block++ {
		start, end, decision := shadow.beginBlock()
		shadow.finishBlock(make([]byte, shadow.blockBytes), start, end, time.Unix(int64(block), 0), decision)
	}
	if err := shadow.writeCheckpoint(time.Now(), "final"); err != nil {
		t.Fatal(err)
	}
	gatedConfig := dutyscheduler.DefaultConfig(dutyscheduler.ModeGated, dutyTestSenders)
	gated, err := newDutyRuntimeWithConfig(dutyTestConfig(), dutyTestSenders, gatedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := gated.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if gated.resume.Status != "RESTORED_SHADOW_TO_GATED" || gated.scheduler.Snapshot().State != "RECOVERY" || gated.decodedBlocks != shadow.decodedBlocks {
		t.Fatalf("shadow checkpoint did not promote safely: resume=%+v snapshot=%+v", gated.resume, gated.scheduler.Snapshot())
	}
}

func TestDutyTrustETAUsesSlowestSenderAndLabelsAssumption(t *testing.T) {
	runtime, err := newDutyRuntime("shadow", dutyTestConfig(), []uint64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := dutyscheduler.Snapshot{
		Mode:  dutyscheduler.ModeShadow,
		State: "SEED",
		Candidates: []dutyscheduler.CandidateSnapshot{{
			Name:               "candidate",
			Eligible:           true,
			EligibleDurationNS: int64(time.Hour),
			Senders: map[uint64]dutyscheduler.SenderSnapshot{
				1: {Protocol: "idm", EligibleEvents: 1000, RequiredEligibleEvents: 2995, RemainingEligibleEvents: 1995},
				2: {Protocol: "r900", EligibleEvents: 2000, RequiredEligibleEvents: 2995, RemainingEligibleEvents: 995},
			},
		}},
	}
	eta := runtime.trustETA(snapshot, time.Unix(0, 0))
	if eta.Status != "ESTIMATING" || eta.Candidate != "candidate" || eta.LimitingSenderID != 1 || eta.EstimatedRemainingSeconds == nil {
		t.Fatalf("unexpected ETA: %+v", eta)
	}
	if got, want := *eta.EstimatedRemainingSeconds, 1995.0/1000.0*3600.0; got != want {
		t.Fatalf("remaining seconds=%f want=%f", got, want)
	}
	if eta.Assumption == "" {
		t.Fatal("ETA omitted its no-further-miss assumption")
	}
}

func TestDutyHourlyCheckpointsAreImmutableAndLatestIsAtomic(t *testing.T) {
	runtime, err := newDutyRuntime("shadow", dutyTestConfig(), []uint64{1})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := runtime.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0).UTC()
	if err := runtime.maybeCheckpoint(start); err != nil {
		t.Fatal(err)
	}
	if err := runtime.maybeCheckpoint(start.Add(59 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := runtime.maybeCheckpoint(start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected directory %q", entry.Name())
		}
		if len(entry.Name()) >= len("checkpoint-") && entry.Name()[:len("checkpoint-")] == "checkpoint-" {
			checkpoints++
		}
		if len(entry.Name()) >= len(".latest-") && entry.Name()[:len(".latest-")] == ".latest-" {
			t.Fatalf("temporary latest file leaked: %q", entry.Name())
		}
	}
	if checkpoints != 2 {
		t.Fatalf("checkpoint files=%d want=2; entries=%v", checkpoints, entries)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report dutyRuntimeReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	if report.ReportKind != "hourly" || report.CheckpointSequence != 2 || report.TrustETA.Status == "" {
		t.Fatalf("unexpected latest checkpoint: %+v", report)
	}
}

func TestDutyCheckpointRestoresExactStateAndWritesReceipt(t *testing.T) {
	directory := t.TempDir()
	original, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	advanceDutyRuntime(original, 12_000)
	checkpointAt := original.startedUTC.Add(time.Hour)
	if err := original.writeCheckpoint(checkpointAt, "hourly"); err != nil {
		t.Fatal(err)
	}
	wantSnapshot := original.scheduler.Snapshot()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) > len("checkpoint-") && entry.Name()[:len("checkpoint-")] == "checkpoint-" {
			if _, err := original.readCheckpoint(filepath.Join(directory, entry.Name())); err != nil {
				t.Fatalf("freshly written checkpoint failed validation: %v", err)
			}
		}
	}

	restored, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if restored.resume.Status != "RESTORED_EXACT" || !restored.resume.PendingReceipt {
		t.Fatalf("unexpected resume metadata: %+v", restored.resume)
	}
	if restored.decodedBlocks != original.decodedBlocks || restored.sampleTime != original.sampleTime || restored.sampleRemainder != original.sampleRemainder {
		t.Fatalf("runtime counters were not restored: got blocks=%d time=%s remainder=%d", restored.decodedBlocks, restored.sampleTime, restored.sampleRemainder)
	}
	if got := restored.scheduler.Snapshot(); !reflect.DeepEqual(got, wantSnapshot) {
		t.Fatalf("exact scheduler snapshot differs\nwant=%+v\ngot=%+v", wantSnapshot, got)
	}
	start := restored.sampleTime
	blockStart, _, _ := restored.beginBlock()
	if blockStart != start {
		t.Fatalf("restored sample clock restarted: got=%s want=%s", blockStart, start)
	}
	if err := restored.maybeCheckpoint(checkpointAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt dutyRuntimeReport
	if err := json.Unmarshal(contents, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ReportKind != "resume" || receipt.Resume.Status != "RESTORED_EXACT" || receipt.Resume.SourceSHA256 == "" {
		t.Fatalf("resume receipt is incomplete: %+v", receipt)
	}
}

func TestDutyCheckpointImportsLegacyEvidenceConservatively(t *testing.T) {
	directory := t.TempDir()
	original, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	advanceDutyRuntime(original, 12_000)
	report, err := original.makeReport(original.startedUTC.Add(time.Hour), "hourly")
	if err != nil {
		t.Fatal(err)
	}
	wantEvidence := dutyEvidenceCount(report.Snapshot)
	report.Schema = "rtlamr-duty-scheduler-live-v2"
	report.SchedulerState = dutyscheduler.State{}
	report.SampleRemainder = 0
	report.CheckpointSequence = 7
	contents, err := marshalDutyReport(report)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "checkpoint-legacy-000007.json")
	if err := writeExclusiveSynced(path, contents); err != nil {
		t.Fatal(err)
	}

	restored, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if restored.resume.Status != "RESTORED_LEGACY" || dutyEvidenceCount(restored.scheduler.Snapshot()) != wantEvidence {
		t.Fatalf("legacy evidence was not restored: resume=%+v snapshot=%+v", restored.resume, restored.scheduler.Snapshot())
	}
	for _, candidate := range restored.scheduler.Snapshot().Candidates {
		if candidate.Eligible {
			t.Fatalf("legacy candidate %s bypassed safe cadence relearning", candidate.Name)
		}
	}
}

func TestDutyCheckpointMigratesCaptureTargetWithoutLosingEvidence(t *testing.T) {
	directory := t.TempDir()
	original, err := newDutyRuntimeWithCaptureTarget("shadow", dutyTestConfig(), dutyTestSenders, 0.999)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	advanceDutyRuntime(original, 12_000)
	if err := original.writeCheckpoint(original.startedUTC.Add(time.Hour), "hourly"); err != nil {
		t.Fatal(err)
	}
	wantEvidence := dutyEvidenceCount(original.scheduler.Snapshot())
	wantState, err := original.scheduler.ExportState()
	if err != nil {
		t.Fatal(err)
	}

	migrated, err := newDutyRuntimeWithCaptureTarget("shadow", dutyTestConfig(), dutyTestSenders, 0.995)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrated.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if migrated.resume.Status != "RESTORED_CAPTURE_TARGET" || dutyEvidenceCount(migrated.scheduler.Snapshot()) != wantEvidence {
		t.Fatalf("capture-target migration lost evidence: resume=%+v evidence=%d want=%d", migrated.resume, dutyEvidenceCount(migrated.scheduler.Snapshot()), wantEvidence)
	}
	migratedState, err := migrated.scheduler.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(migratedState.Senders, wantState.Senders) || !reflect.DeepEqual(migratedState.Protocols, wantState.Protocols) {
		t.Fatal("capture-target migration changed sender or protocol state")
	}
	for idx, candidate := range migratedState.Candidates {
		want := wantState.Candidates[idx]
		if !reflect.DeepEqual(candidate.Senders, want.Senders) {
			t.Fatalf("capture-target migration changed candidate %s evidence or cadence", candidate.Name)
		}
		if candidate.Qualified {
			t.Fatalf("capture-target migration retained candidate %s qualification", candidate.Name)
		}
	}
	if err := migrated.maybeCheckpoint(original.startedUTC.Add(time.Hour + time.Minute)); err != nil {
		t.Fatal(err)
	}
	exact, err := newDutyRuntimeWithCaptureTarget("shadow", dutyTestConfig(), dutyTestSenders, 0.995)
	if err != nil {
		t.Fatal(err)
	}
	if err := exact.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if exact.resume.Status != "RESTORED_EXACT" || dutyEvidenceCount(exact.scheduler.Snapshot()) != wantEvidence {
		t.Fatalf("migrated receipt did not restore exactly: resume=%+v", exact.resume)
	}
}

func TestDutyCheckpointTargetMigrationRejectsAdditionalPolicyChange(t *testing.T) {
	directory := t.TempDir()
	originalConfig := dutyscheduler.DefaultConfig(dutyscheduler.ModeShadow, dutyTestSenders)
	originalConfig.CaptureTarget = 0.999
	original, err := newDutyRuntimeWithConfig(dutyTestConfig(), dutyTestSenders, originalConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	advanceDutyRuntime(original, 12_000)
	if err := original.writeCheckpoint(original.startedUTC.Add(time.Hour), "hourly"); err != nil {
		t.Fatal(err)
	}

	changedConfig := originalConfig
	changedConfig.CaptureTarget = 0.995
	changedConfig.RefreshInterval = 8 * time.Hour
	migrated, err := newDutyRuntimeWithConfig(dutyTestConfig(), dutyTestSenders, changedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrated.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if migrated.resume.Status != "RESTORED_POLICY_MIGRATION" {
		t.Fatalf("additional policy change resumed as %s, want RESTORED_POLICY_MIGRATION", migrated.resume.Status)
	}
	for _, candidate := range migrated.scheduler.Snapshot().Candidates {
		if candidate.Eligible {
			t.Fatalf("additional policy change retained candidate %s cadence", candidate.Name)
		}
	}
}

func TestDutyCheckpointRestoresCustomPolicyExactly(t *testing.T) {
	directory := t.TempDir()
	config := dutyscheduler.DefaultConfig(dutyscheduler.ModeShadow, dutyTestSenders)
	config.RefreshInterval = 8 * time.Hour
	config.RefreshDuration = 12 * time.Minute
	config.MinimumAudit = 0.20
	for idx := range config.Candidates {
		config.Candidates[idx].AuditFraction = 0.20
	}
	original, err := newDutyRuntimeWithConfig(dutyTestConfig(), dutyTestSenders, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	advanceDutyRuntime(original, 12_000)
	if err := original.writeCheckpoint(original.startedUTC.Add(time.Hour), "hourly"); err != nil {
		t.Fatal(err)
	}

	restored, err := newDutyRuntimeWithConfig(dutyTestConfig(), dutyTestSenders, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if restored.resume.Status != "RESTORED_EXACT" || restored.refreshInterval != 8*time.Hour || restored.minimumAudit != 0.20 {
		t.Fatalf("custom policy did not resume exactly: resume=%+v refresh=%s audit=%f", restored.resume, restored.refreshInterval, restored.minimumAudit)
	}
}

func TestGatedCheckpointTakesPriorityOverLargerShadowSession(t *testing.T) {
	directory := t.TempDir()
	gated, err := newDutyRuntime("gated", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := gated.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	advanceDutyRuntime(gated, 7_000)
	if err := gated.writeCheckpoint(gated.startedUTC.Add(time.Hour), "hourly"); err != nil {
		t.Fatal(err)
	}

	shadow, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := shadow.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	advanceDutyRuntime(shadow, 15_000)
	if shadow.decodedBlocks <= gated.decodedBlocks {
		t.Fatal("shadow fixture is not the larger session")
	}
	if err := shadow.writeCheckpoint(shadow.startedUTC.Add(time.Hour), "hourly"); err != nil {
		t.Fatal(err)
	}

	restored, err := newDutyRuntime("gated", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if restored.resume.Status != "RESTORED_EXACT" || restored.decodedBlocks != gated.decodedBlocks {
		t.Fatalf("gated restore replaced gated evidence with shadow evidence: resume=%+v blocks=%d want=%d", restored.resume, restored.decodedBlocks, gated.decodedBlocks)
	}
}

func TestDutyCheckpointIgnoresCorruptFileAndSelectsLargestSession(t *testing.T) {
	directory := t.TempDir()
	makeLegacy := func(name string, blocks int) uint64 {
		runtime, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
		if err != nil {
			t.Fatal(err)
		}
		advanceDutyRuntime(runtime, blocks)
		report, err := runtime.makeReport(runtime.startedUTC.Add(time.Hour), "hourly")
		if err != nil {
			t.Fatal(err)
		}
		report.Schema = "rtlamr-duty-scheduler-live-v2"
		report.SchedulerState = dutyscheduler.State{}
		report.SampleRemainder = 0
		report.CheckpointSequence = 1
		contents, _ := marshalDutyReport(report)
		if err := writeExclusiveSynced(filepath.Join(directory, name), contents); err != nil {
			t.Fatal(err)
		}
		return dutyEvidenceCount(report.Snapshot)
	}
	small := makeLegacy("checkpoint-small-000001.json", 7_000)
	large := makeLegacy("checkpoint-large-000001.json", 15_000)
	if large <= small {
		t.Fatalf("test fixtures are not ordered: small=%d large=%d", small, large)
	}
	if err := os.WriteFile(filepath.Join(directory, "checkpoint-corrupt-000999.json"), []byte("{\n"), 0644); err != nil {
		t.Fatal(err)
	}

	restored, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := dutyEvidenceCount(restored.scheduler.Snapshot()); got != large {
		t.Fatalf("selected evidence=%d want largest=%d", got, large)
	}
	if restored.resume.SkippedCheckpoints != 1 {
		t.Fatalf("skipped checkpoints=%d want=1", restored.resume.SkippedCheckpoints)
	}
}

func advanceDutyRuntime(runtime *dutyRuntime, blocks int) {
	for block := 0; block < blocks; block++ {
		start, end, decision := runtime.beginBlock()
		if block%1_000 == 999 {
			runtime.observe(dutyTestSenders[0], "IDM", end)
			runtime.observe(dutyTestSenders[1], "R900", end)
			runtime.observe(dutyTestSenders[2], "R900", end)
		}
		runtime.finishBlock(make([]byte, runtime.blockBytes), start, end, time.Unix(0, int64(end)), decision)
	}
}

func BenchmarkDutyRuntimeShadowBlockAndCollar(b *testing.B) {
	runtime, err := newDutyRuntime("shadow", dutyTestConfig(), []uint64{1, 2, 3})
	if err != nil {
		b.Fatal(err)
	}
	block := make([]byte, runtime.blockBytes)
	b.SetBytes(int64(len(block)))
	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		start, end, decision := runtime.beginBlock()
		runtime.finishBlock(block, start, end, time.Time{}, decision)
	}
}

func TestDutyRestoreExternalCheckpoint(t *testing.T) {
	source := os.Getenv("RTLAMR_DUTY_CHECKPOINT_DIR")
	if source == "" {
		t.Skip("RTLAMR_DUTY_CHECKPOINT_DIR is not set")
	}
	directory := t.TempDir()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, entry.Name()), contents, 0644); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if runtime.resume.Status != "RESTORED_LEGACY" && runtime.resume.Status != "RESTORED_EXACT" && runtime.resume.Status != "RESTORED_CAPTURE_TARGET" && runtime.resume.Status != "RESTORED_POLICY_MIGRATION" {
		t.Fatalf("external checkpoint was not restored: %+v", runtime.resume)
	}
	if runtime.decodedBlocks == 0 || dutyEvidenceCount(runtime.scheduler.Snapshot()) == 0 {
		t.Fatalf("external checkpoint restoration was vacuous: blocks=%d snapshot=%+v", runtime.decodedBlocks, runtime.scheduler.Snapshot())
	}
	t.Logf("status=%s source=%s sha256=%s blocks=%d evidence=%d", runtime.resume.Status, runtime.resume.SourceFile, runtime.resume.SourceSHA256, runtime.decodedBlocks, dutyEvidenceCount(runtime.scheduler.Snapshot()))
	if err := runtime.maybeCheckpoint(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(directory, fmt.Sprintf("checkpoint-%s-%06d.json", runtime.startedUTC.Format("20060102T150405.000000000Z"), runtime.checkpointSequence))
	if _, err := runtime.readCheckpoint(receipt); err != nil {
		t.Fatalf("emitted exact checkpoint is not readable: %v", err)
	}
	exact, err := newDutyRuntime("shadow", dutyTestConfig(), dutyTestSenders)
	if err != nil {
		t.Fatal(err)
	}
	if err := exact.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if exact.resume.Status != "RESTORED_EXACT" || dutyEvidenceCount(exact.scheduler.Snapshot()) != dutyEvidenceCount(runtime.scheduler.Snapshot()) {
		t.Fatalf("legacy-to-exact continuation failed: resume=%+v", exact.resume)
	}
}

func TestDutyRestoreExternalCaptureTarget(t *testing.T) {
	checkpointPath := os.Getenv("RTLAMR_DUTY_TARGET_CHECKPOINT")
	policyPath := os.Getenv("RTLAMR_DUTY_POLICY")
	targetText := os.Getenv("RTLAMR_DUTY_CAPTURE_TARGET")
	if checkpointPath == "" || targetText == "" {
		t.Skip("external target checkpoint and target are not set")
	}
	target, err := strconv.ParseFloat(targetText, 64)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	var source dutyRuntimeReport
	if err := json.Unmarshal(contents, &source); err != nil {
		t.Fatal(err)
	}
	if source.WarmupBlocks < 1 || source.BlockSamples < 1 || source.BlockBytes < 1 || source.SampleRate < 1 {
		t.Fatal("external checkpoint has invalid runtime geometry")
	}
	packetConfig := protocol.PacketConfig{
		SampleRate:   int(source.SampleRate),
		BlockSize:    int(source.BlockSamples),
		BlockSize2:   source.BlockBytes,
		BufferLength: (source.WarmupBlocks-1)*int(source.BlockSamples) + 1,
	}
	schedulerConfig, err := loadDutySchedulerConfig(source.Mode, source.SenderIDs, target, policyPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newDutyRuntimeWithConfig(packetConfig, source.SenderIDs, schedulerConfig)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	checkpointName := filepath.Base(checkpointPath)
	if err := os.WriteFile(filepath.Join(directory, checkpointName), contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.configureCheckpoints(directory, time.Hour); err != nil {
		t.Fatal(err)
	}
	if runtime.resume.Status != "RESTORED_CAPTURE_TARGET" {
		t.Fatalf("external target checkpoint resumed as %s", runtime.resume.Status)
	}
	restored, err := runtime.scheduler.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Senders, source.SchedulerState.Senders) || !reflect.DeepEqual(restored.Protocols, source.SchedulerState.Protocols) {
		t.Fatal("external target migration changed sender or protocol state")
	}
	for idx, candidate := range restored.Candidates {
		if !reflect.DeepEqual(candidate.Senders, source.SchedulerState.Candidates[idx].Senders) {
			t.Fatalf("external target migration changed candidate %s evidence or cadence", candidate.Name)
		}
		if candidate.Qualified {
			t.Fatalf("external target migration retained candidate %s qualification", candidate.Name)
		}
	}
	t.Logf("status=%s source_sequence=%d blocks=%d evidence=%d", runtime.resume.Status, runtime.resume.SourceSequence, runtime.decodedBlocks, dutyEvidenceCount(runtime.scheduler.Snapshot()))
}

func TestDutyShadowPreservesExternalCorpusMessages(t *testing.T) {
	path := os.Getenv("RTLAMR_DUTY_CORPUS")
	if path == "" {
		t.Skip("RTLAMR_DUTY_CORPUS is not set")
	}
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	previousEncoder := encoder
	previousSingle := *single
	defer func() {
		encoder = previousEncoder
		*single = previousSingle
	}()
	*single = false

	makeReceiver := func(shadow bool) *Receiver {
		receiver := &Receiver{protocolNames: []string{"idm", "r900"}}
		receiver.d, err = receiver.newProtocolDecoder()
		if err != nil {
			t.Fatal(err)
		}
		if shadow {
			receiver.duty, err = newDutyRuntime("shadow", receiver.d.Cfg, dutyTestSenders)
			if err != nil {
				t.Fatal(err)
			}
		}
		return receiver
	}
	run := func(receiver *Receiver) []protocol.Digest {
		capture := &dutyCaptureEncoder{}
		encoder = capture
		state := receiverRunState{
			prev:     make(map[protocol.Digest]bool),
			next:     make(map[protocol.Digest]bool),
			dutyPrev: make(map[protocol.Digest]bool),
			dutyNext: make(map[protocol.Digest]bool),
		}
		blockBytes := receiver.d.Cfg.BlockSize2
		if len(input)%blockBytes != 0 {
			t.Fatalf("corpus size %d is not block aligned to %d", len(input), blockBytes)
		}
		for offset := 0; offset < len(input); offset += blockBytes {
			if !receiver.processBlock(&state, input[offset:offset+blockBytes]) {
				t.Fatalf("processBlock failed at offset %d: %v", offset, receiver.err)
			}
		}
		return capture.digests
	}

	baseline := run(makeReceiver(false))
	shadowReceiver := makeReceiver(true)
	shadow := run(shadowReceiver)
	if !reflect.DeepEqual(shadow, baseline) {
		t.Fatalf("shadow messages differ: baseline=%d shadow=%d", len(baseline), len(shadow))
	}
	if len(shadow) == 0 {
		t.Fatal("corpus replay was vacuous")
	}
	if shadowReceiver.duty.skippedBlocks != 0 || shadowReceiver.duty.decodedBlocks != uint64(len(input)/shadowReceiver.d.Cfg.BlockSize2) {
		t.Fatalf("shadow changed decode inventory: decoded=%d skipped=%d", shadowReceiver.duty.decodedBlocks, shadowReceiver.duty.skippedBlocks)
	}
}

func TestDutyGatedCollarReconstructionPreservesExternalCorpusMessages(t *testing.T) {
	path := os.Getenv("RTLAMR_DUTY_CORPUS")
	if path == "" {
		t.Skip("RTLAMR_DUTY_CORPUS is not set")
	}
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	previousEncoder := encoder
	previousSingle := *single
	defer func() {
		encoder = previousEncoder
		*single = previousSingle
	}()
	*single = false

	newReceiver := func(gated bool) *Receiver {
		receiver := &Receiver{protocolNames: []string{"idm", "r900"}}
		receiver.d, err = receiver.newProtocolDecoder()
		if err != nil {
			t.Fatal(err)
		}
		if gated {
			receiver.duty, err = newDutyRuntime("gated", receiver.d.Cfg, dutyTestSenders)
			if err != nil {
				t.Fatal(err)
			}
		}
		return receiver
	}
	newState := func() receiverRunState {
		return receiverRunState{
			prev:     make(map[protocol.Digest]bool),
			next:     make(map[protocol.Digest]bool),
			dutyPrev: make(map[protocol.Digest]bool),
			dutyNext: make(map[protocol.Digest]bool),
		}
	}

	baselineReceiver := newReceiver(false)
	if len(input)%baselineReceiver.d.Cfg.BlockSize2 != 0 {
		t.Fatalf("corpus size %d is not block aligned to %d", len(input), baselineReceiver.d.Cfg.BlockSize2)
	}
	baselineCapture := &dutyCaptureEncoder{}
	encoder = baselineCapture
	baselineState := newState()
	blockBytes := baselineReceiver.d.Cfg.BlockSize2
	blocks := len(input) / blockBytes
	baselineMessageBlocks := make([]int, 0)
	for block, offset := 0, 0; offset < len(input); block, offset = block+1, offset+blockBytes {
		before := len(baselineCapture.digests)
		messages := baselineReceiver.d.Decode(input[offset : offset+blockBytes])
		if _, keepRunning := baselineReceiver.processDecodedMessages(&baselineState, messages, 0, time.Time{}, true, false, false); !keepRunning {
			t.Fatalf("baseline decode failed at offset %d: %v", offset, baselineReceiver.err)
		}
		if len(baselineCapture.digests) > before {
			baselineMessageBlocks = append(baselineMessageBlocks, block)
		}
	}
	if len(baselineCapture.digests) == 0 {
		t.Fatal("corpus replay was vacuous")
	}
	protected := make([]bool, blocks)
	guardBlocks := 5 * int((baselineReceiver.d.Cfg.SampleRate+baselineReceiver.d.Cfg.BlockSize-1)/baselineReceiver.d.Cfg.BlockSize)
	for _, messageBlock := range baselineMessageBlocks {
		first := messageBlock - guardBlocks
		if first < 0 {
			first = 0
		}
		last := messageBlock + guardBlocks
		if last >= blocks {
			last = blocks - 1
		}
		for block := first; block <= last; block++ {
			protected[block] = true
		}
	}

	gatedReceiver := newReceiver(true)
	gatedCapture := &dutyCaptureEncoder{}
	encoder = gatedCapture
	gatedState := newState()
	warmup := len(gatedReceiver.duty.collar)
	if len(gatedReceiver.duty.collar) <= gatedReceiver.duty.warmupBlocks+2 {
		t.Fatal("test geometry cannot retain one sleep/wake boundary")
	}
	skipBurst := 1
	wakeBurst := int((gatedReceiver.duty.sampleRate + gatedReceiver.duty.blockSamples - 1) / gatedReceiver.duty.blockSamples)
	transition := 0
	for block, offset := 0, 0; offset < len(input); block, offset = block+1, offset+blockBytes {
		data := input[offset : offset+blockBytes]
		start, end, _ := gatedReceiver.duty.beginBlock()
		decode := block < warmup
		if !decode {
			cycle := (block - warmup) % (skipBurst + wakeBurst)
			decode = cycle >= skipBurst
		}
		if protected[block] {
			decode = true
		}
		decision := dutyscheduler.Decision{Decode: decode}
		if gatedReceiver.duty.needsRebuild(decision) {
			transition++
			if transition%2 == 0 {
				decision.Audit = true
			} else {
				decision.Refresh = true
			}
			if _, keepRunning := gatedReceiver.rebuildDutyDecoder(&gatedState); !keepRunning {
				t.Fatalf("collar rebuild failed at block %d: %v", block, gatedReceiver.err)
			}
		}
		if decision.Decode {
			messages := gatedReceiver.d.Decode(data)
			if _, keepRunning := gatedReceiver.processDecodedMessages(&gatedState, messages, end, time.Time{}, true, true, false); !keepRunning {
				t.Fatalf("gated decode failed at block %d: %v", block, gatedReceiver.err)
			}
		}
		gatedReceiver.duty.finishBlock(data, start, end, time.Time{}, decision)
	}
	if gatedReceiver.duty.wasSkipped {
		if _, keepRunning := gatedReceiver.rebuildDutyDecoder(&gatedState); !keepRunning {
			t.Fatalf("final collar rebuild failed: %v", gatedReceiver.err)
		}
	}
	if !reflect.DeepEqual(gatedCapture.digests, baselineCapture.digests) {
		baselineUnique := make(map[protocol.Digest]uint64)
		gatedUnique := make(map[protocol.Digest]uint64)
		for _, digest := range baselineCapture.digests {
			baselineUnique[digest]++
		}
		for _, digest := range gatedCapture.digests {
			gatedUnique[digest]++
		}
		sameCounts := reflect.DeepEqual(baselineUnique, gatedUnique)
		sharedUnique := 0
		for digest := range baselineUnique {
			if _, ok := gatedUnique[digest]; ok {
				sharedUnique++
			}
		}
		firstDifference := -1
		for idx := 0; idx < len(baselineCapture.digests) && idx < len(gatedCapture.digests); idx++ {
			if baselineCapture.digests[idx] != gatedCapture.digests[idx] {
				firstDifference = idx
				break
			}
		}
		t.Fatalf("gated collar messages differ: baseline=%d/%d-unique gated=%d/%d-unique shared-unique=%d same-counts=%v first-difference=%d", len(baselineCapture.digests), len(baselineUnique), len(gatedCapture.digests), len(gatedUnique), sharedUnique, sameCounts, firstDifference)
	}
	if gatedReceiver.duty.skippedBlocks == 0 || gatedReceiver.duty.rebuilds == 0 || gatedReceiver.duty.replayedBlocks == 0 || gatedReceiver.duty.auditedBlocks == 0 || gatedReceiver.duty.refreshBlocks == 0 {
		t.Fatalf("transition exercise was incomplete: skipped=%d rebuilds=%d replayed=%d audited=%d refresh=%d", gatedReceiver.duty.skippedBlocks, gatedReceiver.duty.rebuilds, gatedReceiver.duty.replayedBlocks, gatedReceiver.duty.auditedBlocks, gatedReceiver.duty.refreshBlocks)
	}
}
