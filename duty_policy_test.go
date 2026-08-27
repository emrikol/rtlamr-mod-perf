package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bemasher/rtlamr/internal/dutyscheduler"
)

func TestLoadDutySchedulerPolicyAppliesGenericAndSenderControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	contents := []byte(`{
  "schema": "rtlamr-duty-scheduler-policy-v1",
  "minimum_audit": 0.2,
  "recovery_duration": "12m",
  "refresh_interval": "8h",
  "refresh_duration": "12m",
  "promotion_margin": 0.0001,
  "promotion_stability": "15m",
  "watchdog_history": 64,
  "watchdog_min_intervals": 12,
  "watchdog_window": "15m",
  "watchdog_quantile": 0.98,
  "watchdog_margin": 0.4,
  "senders": [{"id": 1, "overdue": "5m", "count_window": "10m", "minimum_count": 2}]
}`)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadDutySchedulerConfig(dutyscheduler.ModeShadow, []uint64{1, 2}, 0.995, path)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinimumAudit != 0.2 || config.RefreshInterval != 8*time.Hour || config.RefreshDuration != 12*time.Minute || config.RecoveryDuration != 12*time.Minute || config.PromotionStability != 15*time.Minute {
		t.Fatalf("global policy was not applied: %+v", config)
	}
	if config.Senders[0].Overdue != 5*time.Minute || config.Senders[0].MinimumCount != 2 || config.Senders[1].Overdue != 0 {
		t.Fatalf("sender policy was not applied narrowly: %+v", config.Senders)
	}
	for _, candidate := range config.Candidates {
		if candidate.AuditFraction < 0.2 {
			t.Fatalf("candidate %s violates policy audit floor", candidate.Name)
		}
	}
}

func TestLoadDutySchedulerPolicyRejectsUnknownSender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	contents := []byte(`{"schema":"rtlamr-duty-scheduler-policy-v1","senders":[{"id":99,"overdue":"5m"}]}`)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDutySchedulerConfig(dutyscheduler.ModeShadow, []uint64{1}, 0.995, path); err == nil {
		t.Fatal("unknown policy sender was accepted")
	}
}

func TestLoadDutySchedulerPolicyRejectsExposedPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"schema":"rtlamr-duty-scheduler-policy-v1"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDutySchedulerConfig(dutyscheduler.ModeShadow, []uint64{1}, 0.995, path); err == nil {
		t.Fatal("group/world-readable policy was accepted")
	}
}
