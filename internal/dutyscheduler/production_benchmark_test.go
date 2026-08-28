package dutyscheduler

import (
	"testing"
	"time"
)

func BenchmarkProductionCandidateBankEstimatingBlock(b *testing.B) {
	benchmarkProductionCandidateBankEstimatingBlock(b, false)
}

func BenchmarkProductionCandidateBankEstimatingBlockFullEvaluation(b *testing.B) {
	benchmarkProductionCandidateBankEstimatingBlock(b, true)
}

func benchmarkProductionCandidateBankEstimatingBlock(b *testing.B, fullEvaluation bool) {
	scheduler, err := New(DefaultConfig(ModeShadow, []uint64{1, 2, 3}))
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
		if fullEvaluation {
			for _, candidate := range scheduler.candidates {
				candidate.invalidateSchedule()
			}
			for _, sender := range scheduler.senderList {
				sender.countStart = 0
			}
			scheduler.watchdogDirty = true
		}
		start := time.Duration(idx) * block
		scheduler.Advance(start, start+block)
	}
}
