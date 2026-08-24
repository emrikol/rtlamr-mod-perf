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
		CaptureTarget:    0.999,
		Confidence:       0.95,
		MinimumAudit:     0.10,
		RecoveryDuration: 10 * time.Minute,
		RandomSeed:       1,
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
	scheduler.refreshQualifications()
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
	scheduler.refreshQualifications()
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
