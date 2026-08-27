package dutyscheduler

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func testCandidate(name string, history int, r900, other time.Duration) CandidateConfig {
	return CandidateConfig{
		Name:            name,
		HistoryLimit:    history,
		WarmupIntervals: 4,
		R900Floor:       r900,
		OtherFloor:      other,
		JitterQuantile:  0.95,
		JitterMargin:    500 * time.Millisecond,
		PreScale:        1,
		PostScale:       1,
		AuditFraction:   0.10,
	}
}

func testConfig(mode Mode) Config {
	return Config{
		Mode: mode,
		Senders: []SenderConfig{
			{ID: 1, Overdue: 6 * time.Minute, CountWindow: 10 * time.Minute, MinimumCount: 2},
			{ID: 2, Overdue: 3 * time.Minute, CountWindow: 10 * time.Minute, MinimumCount: 6},
			{ID: 3, Overdue: 2 * time.Minute, CountWindow: 10 * time.Minute, MinimumCount: 11},
		},
		Candidates: []CandidateConfig{
			testCandidate("n16", 16, 500*time.Millisecond, 5*time.Second),
			testCandidate("n64", 64, 500*time.Millisecond, 5*time.Second),
		},
		CaptureTarget:      0.999,
		Confidence:         0.95,
		MinimumAudit:       0.10,
		RecoveryDuration:   10 * time.Minute,
		PromotionMargin:    0.000000001,
		PromotionStability: time.Nanosecond,
		RandomSeed:         1,
	}
}

func TestSafeSeedAlwaysDecodes(t *testing.T) {
	scheduler, err := New(testConfig(ModeGated))
	if err != nil {
		t.Fatal(err)
	}
	for block := 0; block < 100; block++ {
		decision := scheduler.Advance(time.Duration(block)*time.Second, time.Duration(block+1)*time.Second)
		if !decision.Decode || decision.Selected != "" || decision.State != "SEED" {
			t.Fatalf("unsafe seed decision at block %d: %+v", block, decision)
		}
	}
}

func TestRobustPeriodTreatsLongGapsAsMultiples(t *testing.T) {
	history := []time.Duration{
		28*time.Second - 3*time.Millisecond,
		56*time.Second + 4*time.Millisecond,
		84*time.Second - 6*time.Millisecond,
		140*time.Second + 5*time.Millisecond,
		28*time.Second + time.Millisecond,
	}
	period, residuals := robustPeriod(history)
	if difference := math.Abs(float64(period - 28*time.Second)); difference > float64(3*time.Millisecond) {
		t.Fatalf("period=%s, want approximately 28s", period)
	}
	for _, residual := range residuals {
		if residual > 15*time.Millisecond {
			t.Fatalf("residual=%s exceeds injected jitter", residual)
		}
	}
}

func TestUpperMissBoundQualificationBoundary(t *testing.T) {
	if got := upperMissBound(0, 2994, 0.05); got <= 0.001 {
		t.Fatalf("2994 observations unexpectedly qualify: %.12f", got)
	}
	if got := upperMissBound(0, 2995, 0.05); got > 0.001 {
		t.Fatalf("2995 observations do not qualify: %.12f", got)
	}
}

func TestConfidenceBoundCacheInvalidatesOnlyOnEvidenceChange(t *testing.T) {
	model := &senderModel{events: 1500, misses: 1}
	alpha := 0.05
	want := upperMissBound(model.misses, model.events, alpha)
	if got := model.confidenceUpperBound(alpha); got != want || model.boundEvaluations != 1 {
		t.Fatalf("initial bound=%f evaluations=%d want=%f,1", got, model.boundEvaluations, want)
	}
	for iteration := 0; iteration < 1000; iteration++ {
		if got := model.confidenceUpperBound(alpha); got != want {
			t.Fatalf("cached bound changed at iteration %d: got=%f want=%f", iteration, got, want)
		}
	}
	if model.boundEvaluations != 1 {
		t.Fatalf("unchanged evidence caused %d evaluations, want 1", model.boundEvaluations)
	}

	model.events++
	want = upperMissBound(model.misses, model.events, alpha)
	if got := model.confidenceUpperBound(alpha); got != want || model.boundEvaluations != 2 {
		t.Fatalf("event invalidation bound=%f evaluations=%d want=%f,2", got, model.boundEvaluations, want)
	}
	model.misses++
	want = upperMissBound(model.misses, model.events, alpha)
	if got := model.confidenceUpperBound(alpha); got != want || model.boundEvaluations != 3 {
		t.Fatalf("miss invalidation bound=%f evaluations=%d want=%f,3", got, model.boundEvaluations, want)
	}
	alpha = 0.01
	want = upperMissBound(model.misses, model.events, alpha)
	if got := model.confidenceUpperBound(alpha); got != want || model.boundEvaluations != 4 {
		t.Fatalf("confidence invalidation bound=%f evaluations=%d want=%f,4", got, model.boundEvaluations, want)
	}
}

func TestRequiredObservationsMatchesExactQualificationBoundary(t *testing.T) {
	if got := RequiredObservations(0, 0.999, 0.95); got != 2995 {
		t.Fatalf("zero-miss required observations=%d want=2995", got)
	}
	for misses := uint64(0); misses < 4; misses++ {
		required := RequiredObservations(misses, 0.999, 0.95)
		if required <= misses || upperMissBound(misses, required, 0.05) > 0.001 {
			t.Fatalf("misses=%d required=%d does not qualify", misses, required)
		}
		if required > misses+1 && upperMissBound(misses, required-1, 0.05) <= 0.001 {
			t.Fatalf("misses=%d required=%d is not minimal", misses, required)
		}
	}
}

func TestDefaultCaptureTargetUsesDataDrivenOperationalContract(t *testing.T) {
	cfg := DefaultConfig(ModeShadow, []uint64{1})
	if cfg.CaptureTarget != 0.995 {
		t.Fatalf("default capture target=%f want=0.995", cfg.CaptureTarget)
	}
	if got := RequiredObservations(0, cfg.CaptureTarget, cfg.Confidence); got != 598 {
		t.Fatalf("default zero-miss required observations=%d want=598", got)
	}
}

func TestDefaultSenderConfigIsSiteIndependent(t *testing.T) {
	for _, id := range []uint64{1, 1001, 9999999999} {
		cfg := DefaultSenderConfig(id)
		if cfg.ID != id || cfg.Overdue != 0 || cfg.CountWindow != 0 || cfg.MinimumCount != 0 {
			t.Fatalf("default sender %d contains site-specific policy: %+v", id, cfg)
		}
	}
}

func TestDefaultConfigCanonicalizesSenderOrder(t *testing.T) {
	first := DefaultConfig(ModeShadow, []uint64{3, 1, 2})
	second := DefaultConfig(ModeShadow, []uint64{2, 3, 1})
	firstFingerprint, err := configSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := configSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("sender order changed config fingerprint: %s != %s", firstFingerprint, secondFingerprint)
	}
	for index, sender := range first.Senders {
		if sender.ID != uint64(index+1) {
			t.Fatalf("canonical sender %d has ID %d", index, sender.ID)
		}
	}
}

func TestUpperMissBoundMonotonicProperties(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	for iteration := 0; iteration < 1000; iteration++ {
		n := uint64(random.Intn(5000) + 2)
		misses := uint64(random.Intn(int(n)))
		bound := upperMissBound(misses, n, 0.05)
		if bound < float64(misses)/float64(n) || bound > 1 {
			t.Fatalf("invalid bound misses=%d n=%d bound=%f", misses, n, bound)
		}
		if misses+1 < n {
			withMiss := upperMissBound(misses+1, n, 0.05)
			if withMiss < bound {
				t.Fatalf("adding a miss lowered bound: before=%f after=%f", bound, withMiss)
			}
		}
		if misses == 0 {
			withMoreEvidence := upperMissBound(0, n+1, 0.05)
			if withMoreEvidence > bound {
				t.Fatalf("clean evidence raised bound: before=%f after=%f", bound, withMoreEvidence)
			}
		}
	}
}

func TestWiderWakeWindowNeverSleepsMore(t *testing.T) {
	random := rand.New(rand.NewSource(2))
	for iteration := 0; iteration < 10000; iteration++ {
		period := time.Duration(random.Int63n(int64(2*time.Minute))) + time.Second
		anchor := time.Duration(random.Int63n(int64(10 * time.Minute)))
		start := time.Duration(random.Int63n(int64(20 * time.Minute)))
		end := start + time.Duration(random.Int63n(int64(time.Second))) + time.Millisecond
		narrowWidth := time.Duration(random.Int63n(int64(period / 4)))
		wideWidth := narrowWidth + time.Duration(random.Int63n(int64(period/4)+1))
		narrow := senderModel{learned: true, period: period, anchor: anchor, preWake: narrowWidth, postWake: narrowWidth}
		wide := senderModel{learned: true, period: period, anchor: anchor, preWake: wideWidth, postWake: wideWidth}
		if narrow.awakeDuring(start, end) && !wide.awakeDuring(start, end) {
			t.Fatalf("wider window slept where narrow was awake: period=%s start=%s end=%s narrow=%s wide=%s", period, start, end, narrowWidth, wideWidth)
		}
	}
}

func TestParallelBankLearnsAndProjectsSleepWithoutChangingDecode(t *testing.T) {
	scheduler, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	next := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	protocol := map[uint64]string{1: "IDM", 2: "R900", 3: "R900"}
	period := map[uint64]time.Duration{1: 60 * time.Second, 2: 28 * time.Second, 3: 28 * time.Second}
	for second := 0; second < 8*60*60; second++ {
		start := time.Duration(second) * time.Second
		end := start + time.Second
		decision := scheduler.Advance(start, end)
		if !decision.Decode || decision.Audit || decision.Selected != "" {
			t.Fatalf("shadow altered decode at %s: %+v", start, decision)
		}
		for id := uint64(1); id <= 3; id++ {
			if next[id] == end {
				scheduler.Observe(id, protocol[id], end)
				next[id] += period[id]
			}
		}
	}
	snapshot := scheduler.Snapshot()
	if len(snapshot.Candidates) != 2 {
		t.Fatalf("candidate count=%d", len(snapshot.Candidates))
	}
	for _, candidate := range snapshot.Candidates {
		if !candidate.Eligible {
			t.Fatalf("candidate %s never became eligible", candidate.Name)
		}
		if candidate.ProjectedSleepFraction < 0.60 {
			t.Fatalf("candidate %s projected sleep=%f", candidate.Name, candidate.ProjectedSleepFraction)
		}
		for id, sender := range candidate.Senders {
			if sender.WouldMiss != 0 {
				t.Fatalf("candidate %s sender %d missed %d exact events", candidate.Name, id, sender.WouldMiss)
			}
		}
	}
}

func TestDeadlineDeficitForcesRecovery(t *testing.T) {
	cfg := testConfig(ModeGated)
	cfg.RecoveryDuration = time.Minute
	scheduler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range scheduler.candidates {
		candidate.eligibleDuration = time.Hour
		candidate.awakeDuration = 10 * time.Minute
		for _, sender := range candidate.senders {
			sender.learned = true
			sender.period = 28 * time.Second
			sender.preWake = time.Second
			sender.postWake = time.Second
			sender.events = 2995
		}
	}
	scheduler.refreshQualifications(scheduler.lastEnd)
	scheduler.Observe(1, "IDM", time.Second)
	scheduler.Observe(2, "R900", time.Second)
	scheduler.Observe(3, "R900", time.Second)
	decision := scheduler.Advance(0, 2*time.Second)
	if decision.Selected == "" {
		t.Fatal("qualified candidate was not selected")
	}
	decision = scheduler.Advance(2*time.Second, 4*time.Minute)
	if decision.State != "RECOVERY" || !decision.Decode {
		t.Fatalf("deadline did not fail open: %+v", decision)
	}
	if scheduler.Snapshot().DeadlineDeficits == 0 {
		t.Fatal("deadline deficit was not recorded")
	}
}

func TestShadowScoresWatchdogsWithoutSuppressingDecode(t *testing.T) {
	scheduler, err := New(testConfig(ModeShadow))
	if err != nil {
		t.Fatal(err)
	}
	scheduler.Advance(0, time.Second)
	scheduler.Observe(1, "IDM", time.Second)
	scheduler.Observe(2, "R900", time.Second)
	scheduler.Observe(3, "R900", time.Second)
	decision := scheduler.Advance(time.Second, 7*time.Minute)
	if !decision.Decode || decision.State == "RECOVERY" {
		t.Fatalf("shadow watchdog changed runtime decision: %+v", decision)
	}
	if scheduler.Snapshot().DeadlineDeficits != 3 {
		t.Fatalf("deadline deficits=%d, want 3", scheduler.Snapshot().DeadlineDeficits)
	}
}

func TestTimeDiscontinuityReturnsToSafeSeed(t *testing.T) {
	scheduler, err := New(testConfig(ModeGated))
	if err != nil {
		t.Fatal(err)
	}
	scheduler.Advance(0, time.Second)
	scheduler.Observe(1, "IDM", time.Second)
	decision := scheduler.Advance(2*time.Second, 3*time.Second)
	if !decision.Decode || decision.State != "RECOVERY" {
		t.Fatalf("discontinuity did not fail open: %+v", decision)
	}
	snapshot := scheduler.Snapshot()
	if snapshot.Discontinuities != 1 || snapshot.ObservedSenders[1].Protocol != "" {
		t.Fatalf("discontinuity did not reset learning: %+v", snapshot)
	}
}

func TestAuditDecisionIsStableForWholeQuietInterval(t *testing.T) {
	cfg := testConfig(ModeGated)
	for idx := range cfg.Senders {
		cfg.Senders[idx].Overdue = 0
		cfg.Senders[idx].CountWindow = 0
		cfg.Senders[idx].MinimumCount = 0
	}
	scheduler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range scheduler.candidates {
		candidate.eligibleDuration = time.Hour
		candidate.awakeDuration = 10 * time.Minute
		candidate.totalDuration = time.Hour
		candidate.totalAwake = 10 * time.Minute
		for _, sender := range candidate.senders {
			sender.learned = true
			sender.period = 28 * time.Second
			sender.anchor = 0
			sender.preWake = time.Second
			sender.postWake = time.Second
			sender.events = 2995
		}
	}
	scheduler.refreshQualifications(scheduler.lastEnd)
	scheduler.Advance(0, time.Second)
	firstQuiet := scheduler.Advance(time.Second, 2*time.Second)
	for second := 2; second < 27; second++ {
		decision := scheduler.Advance(time.Duration(second)*time.Second, time.Duration(second+1)*time.Second)
		if decision.Decode != firstQuiet.Decode || decision.Audit != firstQuiet.Audit {
			t.Fatalf("audit changed inside one quiet interval at second %d: first=%+v got=%+v", second, firstQuiet, decision)
		}
	}
	wake := scheduler.Advance(27*time.Second, 28*time.Second)
	if !wake.Decode || wake.Audit {
		t.Fatalf("predicted wake interval was not decoded normally: %+v", wake)
	}
}

func TestInvalidConfigurationFailsClosed(t *testing.T) {
	cfg := testConfig(ModeGated)
	cfg.Candidates[0].AuditFraction = 0.09
	if _, err := New(cfg); err == nil {
		t.Fatal("audit below invariant was accepted")
	}
	cfg = testConfig(ModeGated)
	cfg.Senders = append(cfg.Senders, cfg.Senders[0])
	if _, err := New(cfg); err == nil {
		t.Fatal("duplicate sender was accepted")
	}
	cfg = testConfig(ModeGated)
	cfg.WatchdogQuantile = math.NaN()
	if _, err := New(cfg); err == nil {
		t.Fatal("non-finite controller configuration was accepted")
	}
}

func primeCandidateForControllerTest(t *testing.T, scheduler *Scheduler, candidate *candidate, now time.Duration) {
	t.Helper()
	candidate.eligibleDuration = time.Hour
	candidate.awakeDuration = 10 * time.Minute
	candidate.totalDuration = time.Hour
	candidate.totalAwake = 10 * time.Minute
	required := RequiredObservations(0, scheduler.cfg.CaptureTarget+scheduler.cfg.PromotionMargin, scheduler.cfg.Confidence)
	for _, sender := range candidate.senders {
		sender.learned = true
		sender.seen = true
		sender.protocol = "R900"
		sender.period = time.Minute
		sender.anchor = 0
		sender.preWake = time.Second
		sender.postWake = time.Second
		sender.events = required
	}
	scheduler.refreshQualifications(now)
	scheduler.refreshQualifications(now + scheduler.cfg.PromotionStability)
	if !candidate.qualified {
		t.Fatal("candidate did not pass promotion hysteresis")
	}
}

func TestPeriodicRefreshForcesContinuousDSP(t *testing.T) {
	cfg := testConfig(ModeGated)
	cfg.RefreshInterval = time.Hour
	cfg.RefreshDuration = 10 * time.Minute
	scheduler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := 20 * time.Minute
	primeCandidateForControllerTest(t, scheduler, scheduler.candidates[0], now)
	scheduler.started = true
	scheduler.lastEnd = now + scheduler.cfg.PromotionStability
	scheduler.chooseCandidate()
	if scheduler.selected == nil {
		t.Fatal("qualified candidate was not selected")
	}
	scheduler.nextRefresh = scheduler.lastEnd + time.Second
	decision := scheduler.Advance(scheduler.lastEnd, scheduler.lastEnd+time.Second)
	if !decision.Decode || !decision.Refresh || decision.Audit {
		t.Fatalf("refresh did not force continuous DSP: %+v", decision)
	}
	if scheduler.Snapshot().Refreshes != 1 {
		t.Fatal("refresh was not recorded")
	}
}

func TestCleanRefreshTightensRecoveredEnvelopeAndStartsFreshEpoch(t *testing.T) {
	scheduler, err := New(testConfig(ModeGated))
	if err != nil {
		t.Fatal(err)
	}
	candidate := scheduler.candidates[0]
	primeCandidateForControllerTest(t, scheduler, candidate, 20*time.Minute)
	candidate.wakeScale = candidate.cfg.WakeScaleStep
	for _, sender := range candidate.senders {
		sender.preWake = scaleDuration(sender.preWake, candidate.wakeScale)
		sender.postWake = scaleDuration(sender.postWake, candidate.wakeScale)
	}
	beforeEpoch := candidate.epoch
	beforeWidth := candidate.senders[1].preWake
	candidate.tightenAfterRefresh(30 * time.Minute)
	if candidate.wakeScale != 1 || candidate.epoch != beforeEpoch+1 || candidate.qualified {
		t.Fatalf("clean refresh did not tighten into a fresh epoch: scale=%f epoch=%d qualified=%v", candidate.wakeScale, candidate.epoch, candidate.qualified)
	}
	if candidate.senders[1].preWake >= beforeWidth {
		t.Fatalf("clean refresh did not narrow the wake envelope: before=%s after=%s", beforeWidth, candidate.senders[1].preWake)
	}
	for _, sender := range candidate.senders {
		if sender.events != 0 || sender.misses != 0 {
			t.Fatal("clean refresh retained confidence evidence for a tightened envelope")
		}
	}
}

func TestLearnedWatchdogCreatesAndExpiresObligations(t *testing.T) {
	cfg := DefaultConfig(ModeShadow, []uint64{1})
	cfg.WatchdogHistory = 8
	cfg.WatchdogMinIntervals = 4
	cfg.WatchdogWindow = 10 * time.Minute
	scheduler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for minute := 0; minute <= 5; minute++ {
		at := time.Duration(minute)*time.Minute + time.Second
		start := scheduler.lastEnd
		if at > start {
			scheduler.Advance(start, at)
		}
		scheduler.Observe(1, "R900", at)
	}
	sender := scheduler.senders[1]
	if !sender.watchdogLearned || !sender.obligationOpen || sender.learnedOverdue <= time.Minute {
		t.Fatalf("learned watchdog was not armed: %+v", sender)
	}
	scheduler.Advance(scheduler.lastEnd, sender.obligationDue+time.Nanosecond)
	snapshot := scheduler.Snapshot()
	observed := snapshot.ObservedSenders[1]
	if observed.ObligationDeficits != 1 || observed.ObligationsOpened == 0 || snapshot.Protocols["R900"].ObligationDeficits != 1 {
		t.Fatalf("expired obligation was not recorded per sender and protocol: %+v %+v", observed, snapshot.Protocols)
	}
}

func TestProtocolTotalsCannotMaskSenderObligation(t *testing.T) {
	cfg := DefaultConfig(ModeShadow, []uint64{1, 2})
	cfg.WatchdogHistory = 8
	cfg.WatchdogMinIntervals = 2
	scheduler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for minute := 0; minute < 3; minute++ {
		at := time.Duration(minute)*time.Minute + time.Second
		scheduler.Advance(scheduler.lastEnd, at)
		scheduler.Observe(1, "R900", at)
		scheduler.Observe(2, "R900", at)
	}
	senderOneDue := scheduler.senders[1].obligationDue
	senderTwoArrival := 3*time.Minute + time.Second
	scheduler.Advance(scheduler.lastEnd, senderTwoArrival)
	scheduler.Observe(2, "R900", senderTwoArrival)
	scheduler.Advance(scheduler.lastEnd, senderOneDue+time.Nanosecond)
	snapshot := scheduler.Snapshot()
	if snapshot.ObservedSenders[1].ObligationDeficits != 1 || snapshot.ObservedSenders[2].ObligationDeficits != 0 {
		t.Fatalf("sender obligation accounting was masked: %+v", snapshot.ObservedSenders)
	}
	if snapshot.Protocols["R900"].ObligationDeficits != 1 {
		t.Fatalf("protocol telemetry did not aggregate the independent sender deficit: %+v", snapshot.Protocols["R900"])
	}
}

func TestAdaptiveHistoryFallsBackOnChangePoint(t *testing.T) {
	cfg := normalizeConfig(testConfig(ModeShadow)).Candidates[1]
	model := &senderModel{}
	for index := 0; index < 24; index++ {
		if model.observe("R900", time.Duration(index+1)*time.Minute, cfg, 1) {
			t.Fatalf("stable cadence reported a change at index %d", index)
		}
	}
	before := model.effectiveHistory
	if before < 16 {
		t.Fatalf("history never grew: %d", before)
	}
	if !model.observe("R900", 26*time.Minute+20*time.Second, cfg, 1) {
		t.Fatal("large residual did not trigger a change point")
	}
	if model.effectiveHistory > cfg.MinHistoryLimit || model.changePoints != 1 {
		t.Fatalf("change point did not shorten history: history=%d changes=%d", model.effectiveHistory, model.changePoints)
	}
}

func TestRecoveryStartsFreshEpochAndCandidateRehabilitates(t *testing.T) {
	cfg := testConfig(ModeGated)
	scheduler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	candidate := scheduler.candidates[0]
	now := 20 * time.Minute
	primeCandidateForControllerTest(t, scheduler, candidate, now)
	oldEpoch := candidate.epoch
	scheduler.recoverAtLeast(candidate.steadySleep(), now)
	if !candidate.invalid || candidate.epoch != oldEpoch+1 || candidate.wakeScale <= 1 {
		t.Fatalf("recovery did not widen and start an epoch: %+v", candidate)
	}
	for _, sender := range candidate.senders {
		if sender.events != 0 || sender.misses != 0 {
			t.Fatal("recovery retained stale confidence evidence")
		}
		sender.events = RequiredObservations(0, scheduler.cfg.CaptureTarget+scheduler.cfg.PromotionMargin, scheduler.cfg.Confidence)
	}
	ready := now + scheduler.cfg.RecoveryDuration
	scheduler.refreshQualifications(ready)
	if candidate.qualified {
		t.Fatal("candidate skipped promotion stability")
	}
	scheduler.refreshQualifications(ready + scheduler.cfg.PromotionStability)
	if !candidate.qualified || candidate.invalid {
		t.Fatal("candidate did not rehabilitate from fresh evidence")
	}
	for _, sender := range candidate.senders {
		sender.misses = sender.events
		break
	}
	scheduler.refreshQualifications(ready + scheduler.cfg.PromotionStability + time.Second)
	if candidate.qualified {
		t.Fatal("demotion was not immediate")
	}
}

func TestRecoveredSkippedArrivalForcesRecoveryAfterClockAdvanced(t *testing.T) {
	scheduler, err := New(testConfig(ModeGated))
	if err != nil {
		t.Fatal(err)
	}
	now := 20 * time.Minute
	for _, candidate := range scheduler.candidates {
		primeCandidateForControllerTest(t, scheduler, candidate, now)
	}
	for _, sender := range scheduler.senders {
		sender.protocol = "R900"
		sender.seen = true
		sender.lastSeen = now - time.Minute
	}
	scheduler.started = true
	scheduler.lastEnd = now
	scheduler.chooseCandidate()
	if scheduler.selected == nil {
		t.Fatal("qualified candidate was not selected")
	}
	oldEpoch := scheduler.selected.epoch
	scheduler.ObserveEscape(1, "R900", now-time.Second)
	if scheduler.Snapshot().State != "RECOVERY" || scheduler.selected != nil {
		t.Fatalf("recovered skipped arrival did not fail open: %+v", scheduler.Snapshot())
	}
	if scheduler.candidates[0].epoch <= oldEpoch || scheduler.candidates[0].wakeScale <= 1 {
		t.Fatal("recovered skipped arrival did not widen into fresh evidence")
	}
}

func TestRecoveryFeedbackRemainsBounded(t *testing.T) {
	scheduler, err := New(testConfig(ModeGated))
	if err != nil {
		t.Fatal(err)
	}
	candidate := scheduler.candidates[0]
	for iteration := 0; iteration < 100; iteration++ {
		candidate.beginNewEpoch(time.Duration(iteration)*time.Minute, true)
	}
	if candidate.wakeScale != candidate.cfg.MaxWakeScale {
		t.Fatalf("wake scale=%f want cap=%f", candidate.wakeScale, candidate.cfg.MaxWakeScale)
	}
	if audit := candidate.auditFraction(); audit < scheduler.cfg.MinimumAudit || audit > 1 {
		t.Fatalf("bounded audit fraction=%f", audit)
	}
	if refresh := candidate.refreshInterval(scheduler.cfg.RefreshInterval); refresh < time.Minute || refresh > scheduler.cfg.RefreshInterval {
		t.Fatalf("bounded refresh interval=%s", refresh)
	}
}

func TestShadowStatePromotesToGatedWithFailOpenRecovery(t *testing.T) {
	shadowConfig := testConfig(ModeShadow)
	shadow, err := New(shadowConfig)
	if err != nil {
		t.Fatal(err)
	}
	shadow.Advance(0, time.Second)
	for id := uint64(1); id <= 3; id++ {
		shadow.Observe(id, "R900", time.Second)
	}
	state, err := shadow.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	gatedConfig := shadowConfig
	gatedConfig.Mode = ModeGated
	gated, err := New(gatedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := gated.RestoreShadowState(state); err != nil {
		t.Fatal(err)
	}
	snapshot := gated.Snapshot()
	if snapshot.Mode != ModeGated || snapshot.State != "RECOVERY" || snapshot.ObservedSenders[1].Observations != 1 {
		t.Fatalf("shadow promotion was not exact and fail-open: %+v", snapshot)
	}
	decision := gated.Advance(time.Second, 2*time.Second)
	if !decision.Decode || decision.State != "RECOVERY" {
		t.Fatalf("promoted scheduler suppressed DSP during recovery: %+v", decision)
	}
}

func BenchmarkShadowCandidateBankBlock(b *testing.B) {
	scheduler, err := New(testConfig(ModeShadow))
	if err != nil {
		b.Fatal(err)
	}
	for _, candidate := range scheduler.candidates {
		for id, sender := range candidate.senders {
			sender.learned = true
			sender.period = 28 * time.Second
			if id == 1 {
				sender.period = 60 * time.Second
			}
			sender.anchor = 0
			sender.preWake = time.Second
			sender.postWake = time.Second
		}
	}
	const block = 3472222 * time.Nanosecond
	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		start := time.Duration(idx) * block
		scheduler.Advance(start, start+block)
	}
}

func BenchmarkShadowCandidateBankEstimatingBlock(b *testing.B) {
	scheduler, err := New(testConfig(ModeShadow))
	if err != nil {
		b.Fatal(err)
	}
	for _, candidate := range scheduler.candidates {
		candidate.eligibleDuration = time.Hour
		for id, sender := range candidate.senders {
			sender.learned = true
			sender.period = 28 * time.Second
			sender.anchor = 0
			sender.preWake = time.Second
			sender.postWake = time.Second
			sender.events = 1500
			if id == 1 {
				sender.period = 60 * time.Second
				sender.misses = 1
			}
		}
	}
	const block = 3472222 * time.Nanosecond
	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		start := time.Duration(idx) * block
		scheduler.Advance(start, start+block)
	}
}
