package dutyscheduler

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestStateRoundTripContinuesExactly(t *testing.T) {
	original, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	next := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	period := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	protocol := map[uint64]string{1: "IDM", 2: "R900", 3: "R900"}
	advanceScheduler(t, original, 0, 2*time.Hour, next, period, protocol)

	state, err := original.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded State
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	restored, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreState(decoded); err != nil {
		t.Fatal(err)
	}
	restoredState, err := restored.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, restoredState) {
		t.Fatalf("restored state differs\noriginal=%+v\nrestored=%+v", state, restoredState)
	}

	restoredNext := make(map[uint64]time.Duration, len(next))
	for id, at := range next {
		restoredNext[id] = at
	}
	for second := int64((2 * time.Hour) / time.Second); second < int64((3*time.Hour)/time.Second); second++ {
		start := time.Duration(second) * time.Second
		end := start + time.Second
		leftDecision := original.Advance(start, end)
		rightDecision := restored.Advance(start, end)
		if leftDecision != rightDecision {
			t.Fatalf("decision differs at %s: left=%+v right=%+v", start, leftDecision, rightDecision)
		}
		for id := uint64(1); id <= 3; id++ {
			if next[id] == end {
				original.Observe(id, protocol[id], end)
				next[id] += period[id]
			}
			if restoredNext[id] == end {
				restored.Observe(id, protocol[id], end)
				restoredNext[id] += period[id]
			}
		}
	}
	left, _ := original.ExportState()
	right, _ := restored.ExportState()
	if !reflect.DeepEqual(left, right) {
		t.Fatal("restored scheduler diverged after identical continuation")
	}
}

func TestCaptureTargetRestorePreservesIndependentState(t *testing.T) {
	originalConfig := testConfig(ModeShadow)
	original, err := New(originalConfig)
	if err != nil {
		t.Fatal(err)
	}
	next := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	period := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	protocol := map[uint64]string{1: "IDM", 2: "R900", 3: "R900"}
	advanceScheduler(t, original, 0, 2*time.Hour, next, period, protocol)
	state, err := original.ExportState()
	if err != nil {
		t.Fatal(err)
	}

	newConfig := originalConfig
	newConfig.CaptureTarget = 0.95
	restored, err := New(newConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreCaptureTargetState(state, originalConfig.CaptureTarget); err != nil {
		t.Fatal(err)
	}
	after, err := restored.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	if after.ConfigSHA256 == state.ConfigSHA256 {
		t.Fatal("capture-target restore retained the old configuration fingerprint")
	}
	if !reflect.DeepEqual(after.Senders, state.Senders) || !reflect.DeepEqual(after.Protocols, state.Protocols) {
		t.Fatal("capture-target restore changed sender or protocol state")
	}
	if after.LastEndNS != state.LastEndNS || after.Started != state.Started || after.RecoveryUntilNS != state.RecoveryUntilNS || after.DeadlineDeficits != state.DeadlineDeficits || after.CountDeficits != state.CountDeficits || after.Discontinuities != state.Discontinuities || after.RefreshUntilNS != state.RefreshUntilNS || after.NextRefreshNS != state.NextRefreshNS || after.Refreshes != state.Refreshes || after.RNG != state.RNG {
		t.Fatal("capture-target restore changed target-independent controller state")
	}
	for idx, got := range after.Candidates {
		want := state.Candidates[idx]
		if !reflect.DeepEqual(got.Senders, want.Senders) || got.Name != want.Name || got.LastEligible != want.LastEligible || got.LastAwake != want.LastAwake || got.TotalDurationNS != want.TotalDurationNS || got.TotalAwakeNS != want.TotalAwakeNS || got.EligibleDurationNS != want.EligibleDurationNS || got.AwakeDurationNS != want.AwakeDurationNS || got.Invalid != want.Invalid || got.Epoch != want.Epoch || got.WakeScale != want.WakeScale || got.RecoveryUntilNS != want.RecoveryUntilNS {
			t.Fatalf("candidate %s lost target-independent state", got.Name)
		}
		if got.Qualified || got.PromotionReadyNS != state.LastEndNS+int64(newConfig.PromotionStability) {
			t.Fatalf("candidate %s did not restart promotion safely: qualified=%t promotion=%d", got.Name, got.Qualified, got.PromotionReadyNS)
		}
	}
	unchanged, err := original.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, unchanged) {
		t.Fatal("capture-target restore mutated the source scheduler")
	}
}

func TestCaptureTargetRestoreRejectsAnyAdditionalPolicyChange(t *testing.T) {
	originalConfig := testConfig(ModeShadow)
	original, err := New(originalConfig)
	if err != nil {
		t.Fatal(err)
	}
	state, err := original.ExportState()
	if err != nil {
		t.Fatal(err)
	}

	changedConfig := originalConfig
	changedConfig.CaptureTarget = 0.995
	changedConfig.RefreshInterval = 7 * time.Hour
	restored, err := New(changedConfig)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := restored.ExportState()
	if err := restored.RestoreCaptureTargetState(state, originalConfig.CaptureTarget); !errors.Is(err, ErrStateConfigurationMismatch) {
		t.Fatalf("additional policy change error=%v, want configuration mismatch", err)
	}
	after, _ := restored.ExportState()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed target-only restore mutated the receiver")
	}
}

func TestPrepareResumeReanchorsWithoutResettingEvidence(t *testing.T) {
	original, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	next := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	periods := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	protocols := map[uint64]string{1: "IDM", 2: "R900", 3: "R900"}
	advanceScheduler(t, original, 0, 2*time.Hour, next, periods, protocols)
	checkpoint, err := original.ExportState()
	if err != nil {
		t.Fatal(err)
	}

	restored, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreState(checkpoint); err != nil {
		t.Fatal(err)
	}
	restored.PrepareResume()
	if len(restored.reanchorPending) != 3 {
		t.Fatalf("pending reanchors=%d want=3", len(restored.reanchorPending))
	}
	reanchorAt := time.Duration(checkpoint.LastEndNS) + 13*time.Second
	decision := restored.Advance(time.Duration(checkpoint.LastEndNS), reanchorAt)
	if !decision.Decode || decision.Selected != "" {
		t.Fatalf("resume was not fail-open before reanchor: %+v", decision)
	}
	for id := uint64(1); id <= 3; id++ {
		restored.Observe(id, protocols[id], reanchorAt)
	}
	if len(restored.reanchorPending) != 0 {
		t.Fatalf("pending reanchors=%d want=0", len(restored.reanchorPending))
	}
	reanchored, err := restored.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	for idx, candidate := range reanchored.Candidates {
		before := checkpoint.Candidates[idx]
		for senderIdx, model := range candidate.Senders {
			old := before.Senders[senderIdx]
			if model.Events != old.Events || model.Misses != old.Misses || model.ChangePoints != old.ChangePoints || !reflect.DeepEqual(model.HistoryNS, old.HistoryNS) {
				t.Fatalf("candidate %s sender %d changed evidence or history during reanchor", candidate.Name, model.ID)
			}
			if model.LastNS != int64(reanchorAt) || model.AnchorNS != int64(reanchorAt) || !model.Seen {
				t.Fatalf("candidate %s sender %d was not reanchored", candidate.Name, model.ID)
			}
		}
	}
	for idx, sender := range reanchored.Senders {
		if !reflect.DeepEqual(sender.GapsNS, checkpoint.Senders[idx].GapsNS) {
			t.Fatalf("sender %d added process downtime to watchdog gaps", sender.ID)
		}
	}

	r900At := reanchorAt + periods[2]
	restored.Advance(reanchorAt, r900At)
	restored.Observe(2, protocols[2], r900At)
	restored.Observe(3, protocols[3], r900At)
	idmAt := reanchorAt + periods[1]
	restored.Advance(r900At, idmAt)
	restored.Observe(1, protocols[1], idmAt)
	continued, err := restored.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	for idx, candidate := range continued.Candidates {
		before := checkpoint.Candidates[idx]
		for senderIdx, model := range candidate.Senders {
			old := before.Senders[senderIdx]
			if model.Events != old.Events+1 || model.Misses != old.Misses || model.ChangePoints != old.ChangePoints {
				t.Fatalf("candidate %s sender %d did not continue cleanly after reanchor", candidate.Name, model.ID)
			}
		}
	}
}

func TestRestoreStateRejectsCorruptionWithoutMutation(t *testing.T) {
	scheduler, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	before, err := scheduler.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	corrupt := before
	corrupt.ConfigSHA256 = "00"
	if err := scheduler.RestoreState(corrupt); err == nil {
		t.Fatal("configuration mismatch was accepted")
	}
	after, _ := scheduler.ExportState()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed restore mutated scheduler")
	}

	corrupt = before
	corrupt.Senders = append(corrupt.Senders, corrupt.Senders[0])
	if err := scheduler.RestoreState(corrupt); err == nil {
		t.Fatal("duplicate sender state was accepted")
	}
}

func TestLegacySnapshotRetainsEvidenceButRelearnsCadence(t *testing.T) {
	original, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	next := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	period := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	protocol := map[uint64]string{1: "IDM", 2: "R900", 3: "R900"}
	advanceScheduler(t, original, 0, 2*time.Hour, next, period, protocol)
	legacy := original.Snapshot()

	restored, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreLegacySnapshot(legacy); err != nil {
		t.Fatal(err)
	}
	snapshot := restored.Snapshot()
	if snapshot.Discontinuities != legacy.Discontinuities+1 {
		t.Fatalf("discontinuities=%d want=%d", snapshot.Discontinuities, legacy.Discontinuities+1)
	}
	for idx, candidate := range snapshot.Candidates {
		if candidate.Eligible {
			t.Fatalf("legacy candidate %s did not return to safe relearning", candidate.Name)
		}
		for id, sender := range candidate.Senders {
			old := legacy.Candidates[idx].Senders[id]
			if sender.EligibleEvents != old.EligibleEvents || sender.WouldMiss != old.WouldMiss {
				t.Fatalf("candidate %s sender %d evidence changed: got=%+v old=%+v", candidate.Name, id, sender, old)
			}
		}
	}
	beforeEvents := snapshot.Candidates[0].Senders[1].EligibleEvents
	clock := time.Duration(legacy.SampleTimeNS)
	for observation := 0; observation < 6; observation++ {
		end := clock + time.Minute
		restored.Advance(clock, end)
		restored.Observe(1, "IDM", end)
		restored.Observe(2, "R900", end)
		restored.Observe(3, "R900", end)
		clock = end
	}
	after := restored.Snapshot()
	if !after.Candidates[0].Eligible || after.Candidates[0].Senders[1].EligibleEvents != beforeEvents+1 {
		t.Fatalf("legacy restore did not relearn and continue evidence: before=%d after=%+v", beforeEvents, after.Candidates[0])
	}
}

func advanceScheduler(t *testing.T, scheduler *Scheduler, start, end time.Duration, next map[uint64]time.Duration, periods map[uint64]time.Duration, protocols map[uint64]string) {
	t.Helper()
	for second := int64(start / time.Second); second < int64(end/time.Second); second++ {
		blockStart := time.Duration(second) * time.Second
		blockEnd := blockStart + time.Second
		decision := scheduler.Advance(blockStart, blockEnd)
		if !decision.Decode {
			t.Fatalf("shadow scheduler suppressed decode at %s", blockStart)
		}
		for id := uint64(1); id <= 3; id++ {
			if next[id] == blockEnd {
				scheduler.Observe(id, protocols[id], blockEnd)
				next[id] += periods[id]
			}
		}
	}
}
