package dutyscheduler

import (
	"encoding/json"
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
