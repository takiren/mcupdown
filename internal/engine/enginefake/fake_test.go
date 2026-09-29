package enginefake

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/takiren/mcupdown/internal/engine"
)

func server() engine.Server {
	return engine.Server{
		Name: "e2e", Port: 25570, Version: "1.21.4", Type: "VANILLA",
		Memory: "1G", ImageTag: "latest", DataDir: "/var/lib/mcctl/servers/e2e",
	}
}

func TestLifecycle(t *testing.T) {
	f := New()
	ctx := t.Context()
	unit := engine.UnitName("e2e")

	if st, _ := f.Status(ctx, unit); st.State() != engine.StateStopped {
		t.Errorf("initial state = %q", st.State())
	}
	if changed, err := f.Write(server()); err != nil || !changed {
		t.Fatalf("Write = %v, %v", changed, err)
	}
	if changed, _ := f.Write(server()); changed {
		t.Error("same Write reported change")
	}
	if err := f.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.Status(ctx, unit); st.State() != engine.StateRunning {
		t.Errorf("after Start = %q", st.State())
	}
	if err := f.Stop(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.Status(ctx, unit); st.State() != engine.StateStopped {
		t.Errorf("after Stop = %q", st.State())
	}
	if names, _ := f.List(); !slices.Equal(names, []string{"e2e"}) {
		t.Errorf("List = %v", names)
	}
	if err := f.Remove("e2e"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.File("e2e"); ok {
		t.Error("file remains after Remove")
	}

	want := []string{
		"Status mcctl-e2e.service", "Write e2e", "Write e2e", "Reload", "Start mcctl-e2e.service",
		"Status mcctl-e2e.service", "Stop mcctl-e2e.service", "Status mcctl-e2e.service", "List", "Remove e2e",
	}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("Calls =\n%q\nwant\n%q", got, want)
	}
}

func TestErrorsAndPodman(t *testing.T) {
	f := New()
	ctx := t.Context()
	boom := errors.New("boom")

	f.SetError("PullImage", boom)
	if err := f.PullImage(ctx, "img"); !errors.Is(err, boom) {
		t.Errorf("PullImage err = %v", err)
	}
	f.SetError("PullImage", nil)
	if ok, _ := f.ImageExists(ctx, "img"); ok {
		t.Error("image exists after failed pull")
	}
	if err := f.PullImage(ctx, "img"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.ImageExists(ctx, "img"); !ok {
		t.Error("image missing after pull")
	}
	if h, _ := f.ContainerHealth(ctx, "mcctl-e2e"); h != engine.HealthNone {
		t.Errorf("health = %q", h)
	}
	f.SetHealth("mcctl-e2e", engine.HealthHealthy)
	if h, _ := f.ContainerHealth(ctx, "mcctl-e2e"); h != engine.HealthHealthy {
		t.Errorf("health = %q", h)
	}
	if r, _ := f.Rootless(ctx); !r {
		t.Error("rootless default = false")
	}

	s := server()
	s.Port = 0
	if _, err := f.Write(s); !errors.Is(err, engine.ErrInvalidServer) {
		t.Errorf("Write(invalid) = %v", err)
	}
}

func TestTail(t *testing.T) {
	f := New()
	f.SetLogs("u", []engine.LogEntry{{Message: "1"}, {Message: "2"}, {Message: "3"}})
	got, err := f.Tail(t.Context(), "u", 2)
	if err != nil || len(got) != 2 || got[0].Message != "2" || got[1].Message != "3" {
		t.Errorf("Tail = %v, %v", got, err)
	}
}

func TestConcurrent(t *testing.T) {
	f := New()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, _ = f.Write(server())
			_ = f.Start(t.Context(), "u")
			_, _ = f.Status(t.Context(), "u")
			_ = f.Calls()
		})
	}
	wg.Wait()
}
