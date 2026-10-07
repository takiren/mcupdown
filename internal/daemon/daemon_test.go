package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/takiren/mcupdown/internal/engine"
	"github.com/takiren/mcupdown/internal/engine/enginefake"
	"github.com/takiren/mcupdown/internal/store"
)

type env struct {
	d        *Daemon
	fake     *enginefake.Fake
	store    store.Store
	dataRoot string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, func(e Engine) Engine { return e })
}

// newEnvWith は wrap で engine の一部を差し替えたデーモンを作る。
func newEnvWith(t *testing.T, wrap func(Engine) Engine) *env {
	t.Helper()
	fake := enginefake.New()
	st := store.NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	dataRoot := t.TempDir()
	eng := wrap(Engine{Files: fake, Units: fake, Podman: fake, Logs: fake})
	d, err := New(st, eng, Config{DataRoot: dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return &env{d: d, fake: fake, store: st, dataRoot: dataRoot}
}

func (e *env) create(t *testing.T, name string, port int) {
	t.Helper()
	if _, err := e.d.Create(t.Context(), CreateRequest{Name: name, Port: port, AcceptEULA: true}); err != nil {
		t.Fatal(err)
	}
}

// wait は非同期の操作が終わるのを待つ。
func (e *env) wait() { e.d.wg.Wait() }

func (e *env) lastOp(t *testing.T, name string) Operation {
	t.Helper()
	s, err := e.d.Get(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	if s.LastOp == nil {
		t.Fatal("no last operation")
	}
	return *s.LastOp
}

// callsSince は from 番目以降の呼び出しから、状態の問い合わせを除いたものを返す。
func (e *env) callsSince(from int) []string {
	var out []string
	for _, c := range e.fake.Calls()[from:] {
		if !slices.ContainsFunc([]string{"Status ", "ContainerHealth "}, func(p string) bool { return len(c) >= len(p) && c[:len(p)] == p }) {
			out = append(out, c)
		}
	}
	return out
}

func TestCreate(t *testing.T) {
	e := newEnv(t)
	got, err := e.d.Create(t.Context(), CreateRequest{Name: "survival", Port: 25565, AcceptEULA: true, Env: map[string]string{"MOTD": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	want := store.Server{
		Name: "survival", Port: 25565, EULAAccepted: true,
		Version: DefaultVersion, Type: DefaultType, Memory: DefaultMemory, ImageTag: DefaultImageTag,
		Env: map[string]string{"MOTD": "hi"}, DesiredState: store.DesiredStateStopped,
	}
	if !reflect.DeepEqual(got.Spec, want) {
		t.Errorf("spec = %+v, want %+v", got.Spec, want)
	}
	if got.State != engine.StateStopped || got.Health != engine.HealthNone || got.LastOp != nil {
		t.Errorf("view = %+v", got)
	}
	if fi, err := os.Stat(filepath.Join(e.dataRoot, "survival")); err != nil || !fi.IsDir() {
		t.Errorf("data directory not created: %v", err)
	}
	// create では .container ファイルを書かない（up で書く）。
	if _, _, ok := e.fake.File("survival"); ok {
		t.Error("unit file written on create")
	}
}

func TestCreateErrors(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)

	tests := []struct {
		name string
		req  CreateRequest
		want error
	}{
		{"bad name", CreateRequest{Name: "Bad_Name", Port: 25566, AcceptEULA: true}, ErrInvalidArgument},
		{"no eula", CreateRequest{Name: "a", Port: 25566}, ErrInvalidArgument},
		{"bad port", CreateRequest{Name: "a", Port: 0, AcceptEULA: true}, ErrInvalidArgument},
		{"bad memory", CreateRequest{Name: "a", Port: 25566, AcceptEULA: true, Memory: "2GB"}, ErrInvalidArgument},
		{"bad env key", CreateRequest{Name: "a", Port: 25566, AcceptEULA: true, Env: map[string]string{"A-B": "x"}}, ErrInvalidArgument},
		{"duplicate name", CreateRequest{Name: "survival", Port: 25566, AcceptEULA: true}, ErrAlreadyExists},
		{"duplicate port", CreateRequest{Name: "creative", Port: 25565, AcceptEULA: true}, ErrPortInUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := e.d.Create(t.Context(), tt.req); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if all, _ := e.store.List(t.Context()); len(all) != 1 {
		t.Errorf("store has %d servers, want 1", len(all))
	}
}

func TestCreateRollsBackWhenDataDirFails(t *testing.T) {
	e := newEnv(t)
	// データディレクトリを作るはずの場所にファイルを置いて、MkdirAll を失敗させる。
	if err := os.WriteFile(filepath.Join(e.dataRoot, "survival"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Create(t.Context(), CreateRequest{Name: "survival", Port: 25565, AcceptEULA: true}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := e.store.Get(t.Context(), "survival"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("store still has the server: %v", err)
	}
}

func TestCreateConcurrentSamePort(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		wg.Go(func() {
			_, err := e.d.Create(context.Background(), CreateRequest{Name: "s" + string(rune('a'+i)), Port: 25565, AcceptEULA: true})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrPortInUse):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d creates succeeded, want 1", ok)
	}
}

func TestUp(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	from := len(e.fake.Calls())

	got, err := e.d.Up(t.Context(), "survival", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.DesiredState != store.DesiredStateRunning || got.LastOp == nil || got.LastOp.Result != OperationInProgress {
		t.Errorf("accepted view = %+v", got)
	}
	e.wait()

	ref := engine.ImageRef(DefaultImageTag)
	want := []string{"ImageExists " + ref, "PullImage " + ref, "Write survival", "Reload", "Start mcctl-survival.service"}
	if got := e.callsSince(from); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if s, _, _ := e.fake.File("survival"); !s.AutoStart || s.DataDir != filepath.Join(e.dataRoot, "survival") {
		t.Errorf("unit file server = %+v", s)
	}
	if op := e.lastOp(t, "survival"); op.Kind != OperationUp || op.Result != OperationSucceeded || op.FinishedAt.IsZero() {
		t.Errorf("last op = %+v", op)
	}
	if s, _ := e.d.Get(t.Context(), "survival"); s.State != engine.StateRunning {
		t.Errorf("state = %s", s.State)
	}
}

func TestUpPull(t *testing.T) {
	ref := engine.ImageRef(DefaultImageTag)
	for _, tt := range []struct {
		name       string
		exists     bool
		pull       bool
		wantPulled bool
	}{
		{"image exists", true, false, false},
		{"image exists and forced", true, true, true},
		{"image missing", false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.create(t, "survival", 25565)
			e.fake.SetImage(ref, tt.exists)
			if _, err := e.d.Up(t.Context(), "survival", tt.pull); err != nil {
				t.Fatal(err)
			}
			e.wait()
			if got := len(e.fake.Pulled()) > 0; got != tt.wantPulled {
				t.Errorf("pulled = %v, want %v", got, tt.wantPulled)
			}
		})
	}
}

func TestUpFailureIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	e.fake.SetError("PullImage", errors.New("registry unreachable"))

	if _, err := e.d.Up(t.Context(), "survival", false); err != nil {
		t.Fatal(err)
	}
	e.wait()
	op := e.lastOp(t, "survival")
	if op.Result != OperationFailed || op.Error == "" {
		t.Errorf("last op = %+v", op)
	}
	// 失敗してもロックは外れている。
	e.fake.SetError("PullImage", nil)
	if _, err := e.d.Up(t.Context(), "survival", false); err != nil {
		t.Fatalf("second up: %v", err)
	}
	e.wait()
	if op := e.lastOp(t, "survival"); op.Result != OperationSucceeded {
		t.Errorf("last op = %+v", op)
	}
}

func TestUpUnchangedFileSkipsReload(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	for range 2 {
		if _, err := e.d.Up(t.Context(), "survival", false); err != nil {
			t.Fatal(err)
		}
		e.wait()
	}
	if got := e.fake.Reloads(); got != 1 {
		t.Errorf("reloads = %d, want 1", got)
	}
}

// blockingUnits は Start を unblock が閉じられるまで止める。
type blockingUnits struct {
	engine.Units
	started chan struct{}
	unblock chan struct{}
}

func (b *blockingUnits) Start(ctx context.Context, unit string) error {
	close(b.started)
	<-b.unblock
	return b.Units.Start(ctx, unit)
}

func TestOperationInProgress(t *testing.T) {
	bu := &blockingUnits{started: make(chan struct{}), unblock: make(chan struct{})}
	e := newEnvWith(t, func(eng Engine) Engine {
		bu.Units = eng.Units
		eng.Units = bu
		return eng
	})
	e.create(t, "survival", 25565)
	e.create(t, "creative", 25566)

	if _, err := e.d.Up(t.Context(), "survival", false); err != nil {
		t.Fatal(err)
	}
	<-bu.started

	ctx := t.Context()
	if _, err := e.d.Up(ctx, "survival", false); !errors.Is(err, ErrOperationInProgress) {
		t.Errorf("up: %v", err)
	}
	if _, err := e.d.Down(ctx, "survival", true); !errors.Is(err, ErrOperationInProgress) {
		t.Errorf("down: %v", err)
	}
	if err := e.d.Remove(ctx, "survival", false); !errors.Is(err, ErrOperationInProgress) {
		t.Errorf("rm: %v", err)
	}
	// 実行中でも参照はできる。
	if s, err := e.d.Get(ctx, "survival"); err != nil || s.LastOp == nil || s.LastOp.Result != OperationInProgress {
		t.Errorf("get = %+v, %v", s, err)
	}
	// 別のサーバーは操作できる。
	if _, err := e.d.Down(ctx, "creative", false); err != nil {
		t.Errorf("down other server: %v", err)
	}

	close(bu.unblock)
	e.wait()
	if _, err := e.d.Down(ctx, "survival", true); err != nil {
		t.Errorf("down after up finished: %v", err)
	}
}

func TestDown(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	if _, err := e.d.Up(t.Context(), "survival", false); err != nil {
		t.Fatal(err)
	}
	e.wait()
	from := len(e.fake.Calls())

	got, err := e.d.Down(t.Context(), "survival", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.DesiredState != store.DesiredStateStopped {
		t.Errorf("desired = %s", got.Spec.DesiredState)
	}
	e.wait()

	want := []string{"Write survival", "Reload", "Stop mcctl-survival.service"}
	if got := e.callsSince(from); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if s, _, _ := e.fake.File("survival"); s.AutoStart {
		t.Error("unit file still has [Install]")
	}
	if op := e.lastOp(t, "survival"); op.Kind != OperationDown || op.Result != OperationSucceeded {
		t.Errorf("last op = %+v", op)
	}
}

func TestDownWhileStarting(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	e.fake.SetStartStatus(engine.UnitStatus{LoadState: "loaded", ActiveState: "activating", SubState: "start"})
	if _, err := e.d.Up(t.Context(), "survival", false); err != nil {
		t.Fatal(err)
	}
	e.wait()

	if _, err := e.d.Down(t.Context(), "survival", false); !errors.Is(err, ErrServerStarting) {
		t.Fatalf("err = %v, want ErrServerStarting", err)
	}
	// 断ったときは求める状態を変えない。
	if s, _ := e.store.Get(t.Context(), "survival"); s.DesiredState != store.DesiredStateRunning {
		t.Errorf("desired = %s", s.DesiredState)
	}
	if _, err := e.d.Down(t.Context(), "survival", true); err != nil {
		t.Fatalf("forced down: %v", err)
	}
	e.wait()
	if op := e.lastOp(t, "survival"); op.Kind != OperationDown || op.Result != OperationSucceeded {
		t.Errorf("last op = %+v", op)
	}
}

func TestNotFound(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	if _, err := e.d.Up(ctx, "nope", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("up: %v", err)
	}
	if _, err := e.d.Down(ctx, "nope", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("down: %v", err)
	}
	if err := e.d.Remove(ctx, "nope", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("rm: %v", err)
	}
	if _, err := e.d.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := e.d.Logs(ctx, "nope", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("logs: %v", err)
	}
	// 見つからなかった操作はロックを残さない。
	e.create(t, "nope", 25565)
	if _, err := e.d.Up(ctx, "nope", false); err != nil {
		t.Errorf("up after create: %v", err)
	}
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	e.create(t, "survival", 25565)
	if _, err := e.d.Up(ctx, "survival", false); err != nil {
		t.Fatal(err)
	}
	e.wait()

	if err := e.d.Remove(ctx, "survival", false); !errors.Is(err, ErrServerRunning) {
		t.Fatalf("rm running: %v", err)
	}
	if _, err := e.d.Down(ctx, "survival", false); err != nil {
		t.Fatal(err)
	}
	e.wait()
	from := len(e.fake.Calls())

	if err := e.d.Remove(ctx, "survival", false); err != nil {
		t.Fatal(err)
	}
	want := []string{"Remove survival", "Reload"}
	if got := e.callsSince(from); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if _, err := e.store.Get(ctx, "survival"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("store: %v", err)
	}
	// --purge なしではワールドは残る。
	if _, err := os.Stat(filepath.Join(e.dataRoot, "survival")); err != nil {
		t.Errorf("data directory removed: %v", err)
	}
}

func TestRemoveWhileUnitActive(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	// 求める状態は stopped だが、unit はまだ動いている（停止中など）。
	e.fake.SetUnitStatus("mcctl-survival.service", engine.UnitStatus{LoadState: "loaded", ActiveState: "deactivating", SubState: "stop"})
	if err := e.d.Remove(t.Context(), "survival", false); !errors.Is(err, ErrServerRunning) {
		t.Fatalf("err = %v, want ErrServerRunning", err)
	}
	// failed は削除できる。
	e.fake.SetUnitStatus("mcctl-survival.service", engine.UnitStatus{LoadState: "loaded", ActiveState: "failed", SubState: "failed"})
	if err := e.d.Remove(t.Context(), "survival", false); err != nil {
		t.Fatalf("rm failed unit: %v", err)
	}
}

func TestRemovePurge(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	world := filepath.Join(e.dataRoot, "survival", "world")
	if err := os.MkdirAll(world, 0o750); err != nil {
		t.Fatal(err)
	}
	e.fake.SetDropIns("survival", true)

	if err := e.d.Remove(t.Context(), "survival", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dataRoot, "survival")); !os.IsNotExist(err) {
		t.Errorf("data directory still exists: %v", err)
	}
	if e.fake.HasDropIns("survival") {
		t.Error("drop-ins still exist")
	}
}

func TestRecreateKeepsWorld(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	marker := filepath.Join(e.dataRoot, "survival", "level.dat")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Remove(t.Context(), "survival", false); err != nil {
		t.Fatal(err)
	}
	e.create(t, "survival", 25565)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("world lost: %v", err)
	}
}

func TestViewToleratesQueryErrors(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	e.fake.SetError("Status", errors.New("dbus down"))
	e.fake.SetError("ContainerHealth", errors.New("podman down"))

	all, err := e.d.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].State != engine.StateUnknown || all[0].Health != engine.HealthNone {
		t.Errorf("list = %+v", all)
	}
}

func TestViewStatus(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	e.fake.SetUnitStatus("mcctl-survival.service", engine.UnitStatus{LoadState: "loaded", ActiveState: "activating", SubState: "auto-restart", NRestarts: 3})
	e.fake.SetHealth("mcctl-survival", engine.HealthUnhealthy)

	s, err := e.d.Get(t.Context(), "survival")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != engine.StateRestarting || s.Restarts != 3 || s.Health != engine.HealthUnhealthy {
		t.Errorf("view = %+v", s)
	}
}

func TestLogs(t *testing.T) {
	e := newEnv(t)
	e.create(t, "survival", 25565)
	ctx := t.Context()

	if _, err := e.d.Logs(ctx, "survival", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Logs(ctx, "survival", 50); err != nil {
		t.Fatal(err)
	}
	calls := e.fake.Calls()
	if !slices.Contains(calls, "Tail mcctl-survival.service 200") || !slices.Contains(calls, "Tail mcctl-survival.service 50") {
		t.Errorf("calls = %q", calls)
	}
	for _, n := range []int{-1, MaxLogTail + 1} {
		if _, err := e.d.Logs(ctx, "survival", n); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("tail %d: %v", n, err)
		}
	}
}

func TestNewRejectsBadDataRoot(t *testing.T) {
	fake := enginefake.New()
	eng := Engine{Files: fake, Units: fake, Podman: fake, Logs: fake}
	st := store.NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	for _, root := range []string{"", "relative/dir", "/var/lib/mc ctl", "/var/lib/a,b"} {
		if _, err := New(st, eng, Config{DataRoot: root}); err == nil {
			t.Errorf("New(%q) succeeded", root)
		}
	}
}
