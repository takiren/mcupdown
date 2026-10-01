//go:build integration

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSystemdUserIntegration は実際の systemd --user で起動・停止・状態の取得を確かめる。
// mcctl ユーザー（linger あり）で XDG_RUNTIME_DIR と DBUS_SESSION_BUS_ADDRESS を設定して実行する。
func TestSystemdUserIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const unit = "mcctl-e2e-systemd-it.service"
	path := filepath.Join(dir, unit)
	content := "[Service]\nExecStart=/bin/sleep 300\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewSystemdUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Cleanup(func() {
		_ = s.Stop(context.Background(), unit)
		_ = os.Remove(path)
		_ = s.Reload(context.Background())
	})

	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Status(ctx, unit); err != nil || st.State() != StateStopped {
		t.Fatalf("before start: %+v, %v", st, err)
	}
	if err := s.Start(ctx, unit); err != nil {
		t.Fatal(err)
	}
	waitState(ctx, t, s, unit, StateRunning)

	if err := s.Stop(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Status(ctx, unit); err != nil || st.State() != StateStopped {
		t.Fatalf("after stop: %+v, %v", st, err)
	}
	// 止まっている unit を止めてもエラーにならない。
	if err := s.Stop(ctx, unit); err != nil {
		t.Errorf("stop again: %v", err)
	}

	// 存在しない unit。
	missing := "mcctl-e2e-does-not-exist.service"
	if st, err := s.Status(ctx, missing); err != nil || st.LoadState != "not-found" || st.State() != StateStopped {
		t.Errorf("missing unit: %+v, %v", st, err)
	}
	if err := s.Stop(ctx, missing); err != nil {
		t.Errorf("stop missing unit: %v", err)
	}
}

func waitState(ctx context.Context, t *testing.T, s *SystemdUser, unit string, want State) UnitStatus {
	t.Helper()
	for {
		st, err := s.Status(ctx, unit)
		if err != nil {
			t.Fatal(err)
		}
		if st.State() == want {
			return st
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for %s: last %+v", want, st)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
