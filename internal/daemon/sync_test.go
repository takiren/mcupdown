package daemon

import (
	"errors"
	"slices"
	"testing"

	"github.com/takiren/mcupdown/internal/engine"
	"github.com/takiren/mcupdown/internal/store"
)

func TestSyncNotRootless(t *testing.T) {
	e := newEnv(t)
	e.fake.SetRootless(false)
	if err := e.d.Sync(t.Context()); !errors.Is(err, ErrNotRootless) {
		t.Fatalf("err = %v, want ErrNotRootless", err)
	}
}

func TestSyncRegeneratesAndStarts(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	e.create(t, "running", 25565)
	e.create(t, "stopped", 25566)
	e.create(t, "crashed", 25567)
	for _, name := range []string{"running", "crashed"} {
		if err := e.store.Update(ctx, name, func(s *store.Server) error {
			s.DesiredState = store.DesiredStateRunning
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// crashed は failed のまま残す。running は unit がない（ファイルを書く前に落ちた）ので起動する。
	e.fake.SetUnitStatus("mcctl-crashed.service", engine.UnitStatus{LoadState: "loaded", ActiveState: "failed", SubState: "failed"})
	from := len(e.fake.Calls())

	if err := e.d.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	calls := e.callsSince(from)
	for _, want := range []string{"Write running", "Write stopped", "Write crashed", "Reload", "Start mcctl-running.service"} {
		if !slices.Contains(calls, want) {
			t.Errorf("missing call %q in %q", want, calls)
		}
	}
	for _, unwanted := range []string{"Start mcctl-stopped.service", "Start mcctl-crashed.service"} {
		if slices.Contains(calls, unwanted) {
			t.Errorf("unexpected call %q", unwanted)
		}
	}
	if e.fake.Reloads() != 1 {
		t.Errorf("reloads = %d, want 1", e.fake.Reloads())
	}
	// [Install] の有無は求める状態に合わせる。
	if s, _, _ := e.fake.File("running"); !s.AutoStart {
		t.Error("running: AutoStart = false")
	}
	if s, _, _ := e.fake.File("stopped"); s.AutoStart {
		t.Error("stopped: AutoStart = true")
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	for range 2 {
		if err := e.d.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if e.fake.Reloads() != 1 {
		t.Errorf("reloads = %d, want 1 (the second sync changes nothing)", e.fake.Reloads())
	}
}

func TestSyncLeavesOrphansAlone(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	if _, err := e.fake.Write(engine.Server{
		Name: "orphan", Port: 25570, Version: "LATEST", Type: "VANILLA", Memory: "1G", ImageTag: "latest", DataDir: "/srv/orphan",
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.fake.File("orphan"); !ok {
		t.Error("orphan unit file was removed")
	}
	if slices.Contains(e.fake.Calls(), "Remove orphan") {
		t.Error("orphan was removed")
	}
}

func TestSyncContinuesAfterFailure(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	e.create(t, "a", 25565)
	e.create(t, "b", 25566)
	for _, name := range []string{"a", "b"} {
		if err := e.store.Update(ctx, name, func(s *store.Server) error {
			s.DesiredState = store.DesiredStateRunning
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	e.fake.SetError("Start", errors.New("boom"))

	err := e.d.Sync(ctx)
	var partial *PartialSyncError
	if !errors.As(err, &partial) || len(partial.Errs) != 2 {
		t.Fatalf("err = %v, want PartialSyncError with 2 errors", err)
	}
	calls := e.fake.Calls()
	if !slices.Contains(calls, "Start mcctl-a.service") || !slices.Contains(calls, "Start mcctl-b.service") {
		t.Errorf("both servers should be attempted: %q", calls)
	}
}

func TestSyncReloadFailureIsFatal(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	e.fake.SetError("Reload", errors.New("dbus down"))
	err := e.d.Sync(t.Context())
	var partial *PartialSyncError
	if err == nil || errors.As(err, &partial) {
		t.Fatalf("err = %v, want a fatal (non-partial) error", err)
	}
}
