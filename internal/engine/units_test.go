package engine

import "testing"

func TestUnitStatusState(t *testing.T) {
	for _, tc := range []struct {
		load, active, sub string
		want              State
	}{
		{"loaded", "inactive", "dead", StateStopped},
		{"not-found", "inactive", "dead", StateStopped},
		{"not-found", "failed", "failed", StateStopped},
		{"loaded", "activating", "start", StateStarting},
		{"loaded", "activating", "start-pre", StateStarting},
		{"loaded", "activating", "auto-restart", StateRestarting},
		{"loaded", "activating", "auto-restart-queued", StateRestarting},
		{"loaded", "active", "running", StateRunning},
		{"loaded", "reloading", "reload", StateRunning},
		{"loaded", "deactivating", "stop-sigterm", StateStopping},
		{"loaded", "failed", "failed", StateFailed},
		{"loaded", "maintenance", "", StateUnknown},
		{"", "", "", StateUnknown},
	} {
		s := UnitStatus{LoadState: tc.load, ActiveState: tc.active, SubState: tc.sub}
		if got := s.State(); got != tc.want {
			t.Errorf("%s/%s/%s: State() = %q, want %q", tc.load, tc.active, tc.sub, got, tc.want)
		}
	}
}
