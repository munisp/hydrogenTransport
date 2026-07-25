package anomaly

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestDetector(threshold int, window time.Duration) *Detector {
	d := New(threshold, window, "", zap.NewNop())
	start := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	var now = start
	d.now = func() time.Time { return now }
	return d
}

func TestNoAlertBelowThreshold(t *testing.T) {
	d := newTestDetector(3, time.Minute)
	for i := 0; i < 3; i++ {
		d.Observe("alice", "toggle.update", "feature_toggle")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, alerted := d.alerted["alice"]; alerted {
		t.Fatal("alert fired below/at threshold")
	}
}

func TestAlertAboveThresholdWithCooldown(t *testing.T) {
	d := newTestDetector(3, time.Minute)
	now := d.now()
	offset := time.Second
	d.now = func() time.Time { return now.Add(offset) }

	for i := 0; i < 4; i++ {
		d.Observe("alice", "user.create", "user")
	}
	d.mu.Lock()
	firstAlert, alerted := d.alerted["alice"]
	d.mu.Unlock()
	if !alerted {
		t.Fatal("expected alert above threshold")
	}

	// Cooldown: further bursts must not update the alert timestamp.
	offset += 30 * time.Second
	for i := 0; i < 5; i++ {
		d.Observe("alice", "user.create", "user")
	}
	d.mu.Lock()
	secondAlert := d.alerted["alice"]
	d.mu.Unlock()
	if !secondAlert.Equal(firstAlert) {
		t.Fatal("cooldown violated: alert refired inside 5m")
	}

	// After cooldown expiry a fresh burst alerts again (timestamp advances).
	offset += 6 * time.Minute
	for i := 0; i < 5; i++ {
		d.Observe("alice", "user.create", "user")
	}
	d.mu.Lock()
	thirdAlert := d.alerted["alice"]
	d.mu.Unlock()
	if !thirdAlert.After(firstAlert) {
		t.Fatal("expected re-alert after cooldown expired")
	}
}

func TestWindowPruning(t *testing.T) {
	d := newTestDetector(2, time.Minute)
	now := d.now()
	offset := time.Duration(0)
	d.now = func() time.Time { return now.Add(offset) }

	d.Observe("bob", "user.disable", "user")
	offset = 90 * time.Second // outside the 1m window
	d.Observe("bob", "user.disable", "user")
	d.mu.Lock()
	count := len(d.seen["bob"])
	_, alerted := d.alerted["bob"]
	d.mu.Unlock()
	if count != 1 {
		t.Fatalf("stale entries not pruned: count=%d want 1", count)
	}
	if alerted {
		t.Fatal("alert fired despite window pruning")
	}
}

func TestEmptyActorIgnored(t *testing.T) {
	d := newTestDetector(0, time.Minute)
	d.Observe("", "x", "y")
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.seen) != 0 {
		t.Fatal("empty actor recorded")
	}
}
