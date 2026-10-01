//go:build integration

package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEngineLifecycleIntegration は4つの実装を組み合わせ、実際のマイクラのサーバーで
// 書き込み → reload → 起動 → healthy → クラッシュからの自動復帰 → ログ → 停止 を確かめる。
// mcctl ユーザー（linger と systemd-journal グループあり）で、XDG_RUNTIME_DIR と
// DBUS_SESSION_BUS_ADDRESS を設定して実行する。数分かかる。
func TestEngineLifecycleIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	const name = "e2e-engine"
	unit, container := UnitName(name), ContainerName(name)
	files := &QuadletDir{Dir: filepath.Join(home, ".config", "containers", "systemd")}
	podman := NewPodmanAPI(DefaultPodmanSocket())
	journal := NewJournal()
	units, err := NewSystemdUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer units.Close()

	dataDir := filepath.Join(home, "servers", name)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_ = units.Stop(c, unit)
		_ = files.Remove(name)
		_ = units.Reload(c)
		_ = os.RemoveAll(dataDir)
	})

	s := Server{
		Name: name, Port: 25570, Version: "1.21.4", Type: "VANILLA", Memory: "1G",
		ImageTag: "latest", DataDir: dataDir, AutoStart: true,
		Env: map[string]string{"MOTD": "mcctl e2e 100% $HOME"},
	}
	ref := ImageRef(s.ImageTag)
	if ok, err := podman.ImageExists(ctx, ref); err != nil {
		t.Fatal(err)
	} else if !ok {
		if err := podman.PullImage(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}

	if changed, err := files.Write(s); err != nil || !changed {
		t.Fatalf("Write = %v, %v", changed, err)
	}
	if err := units.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := units.Start(ctx, unit); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(ctx, t, units, unit, func(st UnitStatus) bool { return st.State() == StateRunning })
	if !slices.Contains(seen, StateStarting) {
		t.Errorf("never observed starting: %v", seen)
	}
	if h, err := podman.ContainerHealth(ctx, container); err != nil || h != HealthHealthy {
		t.Errorf("health after running = %q, %v", h, err)
	}

	// %、$、空白を含む env がそのまま届いている。
	out, err := exec.CommandContext(ctx, "podman", "exec", container, "printenv", "MOTD").Output()
	if err != nil || strings.TrimSpace(string(out)) != "mcctl e2e 100% $HOME" {
		t.Errorf("MOTD in container = %q, %v", out, err)
	}

	// java を kill してクラッシュさせると、systemd が再起動する。
	if err := exec.CommandContext(ctx, "podman", "exec", container, "pkill", "-9", "java").Run(); err != nil {
		t.Fatalf("kill java: %v", err)
	}
	seen = waitFor(ctx, t, units, unit, func(st UnitStatus) bool { return st.NRestarts >= 1 && st.State() == StateRunning })
	if !slices.Contains(seen, StateRestarting) {
		t.Errorf("never observed restarting: %v", seen)
	}

	entries, err := journal.Tail(ctx, unit, 500)
	if err != nil {
		t.Fatal(err)
	}
	var fromContainer, started, restartScheduled bool
	for _, e := range entries {
		switch {
		case e.Source == LogSourceContainer && strings.Contains(e.Message, "Done ("):
			fromContainer = true
		case e.Source == LogSourceSystemd && strings.HasPrefix(e.Message, "Started "):
			started = true
		case e.Source == LogSourceSystemd && strings.Contains(e.Message, "Scheduled restart job"):
			restartScheduled = true
		}
	}
	if !fromContainer || !started || !restartScheduled {
		t.Errorf("journal: container=%v started=%v restart=%v (%d entries)", fromContainer, started, restartScheduled, len(entries))
	}
	if last, err := journal.Tail(ctx, unit, 3); err != nil || len(last) != 3 {
		t.Errorf("Tail(3) = %d entries, %v", len(last), err)
	}

	if err := units.Stop(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if st, err := units.Status(ctx, unit); err != nil || st.State() != StateStopped {
		t.Errorf("after stop = %+v, %v", st, err)
	}
	if h, err := podman.ContainerHealth(ctx, container); err != nil || h != HealthNone {
		t.Errorf("health after stop = %q, %v（--rm でコンテナは消えるはず）", h, err)
	}
	if names, err := files.List(); err != nil || !slices.Contains(names, name) {
		t.Errorf("List = %v, %v", names, err)
	}
}

// waitFor は done が true になるまで unit の状態を見張り、途中で見えた状態を返す。
func waitFor(ctx context.Context, t *testing.T, u *SystemdUser, unit string, done func(UnitStatus) bool) []State {
	t.Helper()
	var seen []State
	for {
		st, err := u.Status(ctx, unit)
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) == 0 || seen[len(seen)-1] != st.State() {
			seen = append(seen, st.State())
		}
		if done(st) {
			return seen
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout: states %v, last %+v", seen, st)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
