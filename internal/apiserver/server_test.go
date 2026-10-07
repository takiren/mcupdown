package apiserver

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/takiren/mcupdown/internal/api"
	"github.com/takiren/mcupdown/internal/daemon"
	"github.com/takiren/mcupdown/internal/engine"
	"github.com/takiren/mcupdown/internal/engine/enginefake"
	"github.com/takiren/mcupdown/internal/store"
)

type testEnv struct {
	c    *api.ClientWithResponses
	hc   *http.Client
	fake *enginefake.Fake
	sock string
}

// blockingUnits は block が閉じられるまで Start を止める（操作中の状態を作るため）。
type blockingUnits struct {
	engine.Units
	block chan struct{}
}

func (b *blockingUnits) Start(ctx context.Context, unit string) error {
	if b.block != nil {
		<-b.block
	}
	return b.Units.Start(ctx, unit)
}

func newTestEnv(t *testing.T, units func(engine.Units) engine.Units) *testEnv {
	t.Helper()
	fake := enginefake.New()
	eng := daemon.Engine{Files: fake, Units: fake, Podman: fake, Logs: fake}
	if units != nil {
		eng.Units = units(fake)
	}
	d, err := daemon.New(store.NewFileStore(filepath.Join(t.TempDir(), "state.json")), eng, daemon.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	// macOS は Unix ソケットのパス長の上限が短いので、短い一時ディレクトリを使う。
	dir, err := os.MkdirTemp("", "mcctld")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	// 接続元はテスト自身なので、Linux でも自分の UID として許可される。
	srv := &http.Server{
		Handler:     NewHandler(d, &Authorizer{SelfUID: uint32(os.Getuid()), Group: "mcctl"}),
		ConnContext: ConnContext,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = d.Shutdown(context.Background())
		_ = os.RemoveAll(dir)
	})

	c, err := api.NewUnixSocketClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dl net.Dialer
		return dl.DialContext(ctx, "unix", sock)
	}}}
	return &testEnv{c: c, hc: hc, fake: fake, sock: sock}
}

func (e *testEnv) create(t *testing.T, name string, port int) {
	t.Helper()
	res, err := e.c.CreateServerWithResponse(t.Context(), api.CreateServerJSONRequestBody{Name: name, Port: port, AcceptEula: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.JSON201 == nil {
		t.Fatalf("create: status %d: %s", res.StatusCode(), res.Body)
	}
}

// waitOp は最後の操作が終わるまで待つ。
func (e *testEnv) waitOp(t *testing.T, name string) api.Server {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := e.c.GetServerWithResponse(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		if res.JSON200 == nil {
			t.Fatalf("get: status %d: %s", res.StatusCode(), res.Body)
		}
		if op := res.JSON200.LastOperation; op != nil && op.Result != api.OperationResultInProgress {
			return *res.JSON200
		}
		if time.Now().After(deadline) {
			t.Fatal("operation did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ptr[T any](v T) *T { return &v }

func TestLifecycle(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	cres, err := e.c.CreateServerWithResponse(ctx, api.CreateServerJSONRequestBody{
		Name: "survival", Port: 25565, AcceptEula: true,
		Type: ptr("PAPER"), Memory: ptr("4G"), Env: &map[string]string{"MOTD": "hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cres.StatusCode() != http.StatusCreated || cres.JSON201 == nil {
		t.Fatalf("create: %d %s", cres.StatusCode(), cres.Body)
	}
	got := cres.JSON201
	if got.Spec.Type != "PAPER" || got.Spec.Memory != "4G" || got.Spec.Version != daemon.DefaultVersion ||
		got.Spec.Env["MOTD"] != "hi" || got.DesiredState != api.DesiredStateStopped || got.Status.State != api.ActualStateStopped {
		t.Errorf("created = %+v", got)
	}

	ures, err := e.c.UpServerWithResponse(ctx, "survival", &api.UpServerParams{})
	if err != nil {
		t.Fatal(err)
	}
	if ures.StatusCode() != http.StatusAccepted || ures.JSON202 == nil || ures.JSON202.DesiredState != api.DesiredStateRunning {
		t.Fatalf("up: %d %s", ures.StatusCode(), ures.Body)
	}
	e.fake.SetHealth("mcctl-survival", engine.HealthHealthy)
	s := e.waitOp(t, "survival")
	if s.LastOperation.Kind != api.OperationKindUp || s.LastOperation.Result != api.OperationResultSucceeded || s.LastOperation.FinishedAt == nil {
		t.Errorf("last op = %+v", s.LastOperation)
	}
	if s.Status.State != api.ActualStateRunning || s.Status.Health != api.HealthHealthy {
		t.Errorf("status = %+v", s.Status)
	}

	lres, err := e.c.ListServersWithResponse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lres.JSON200 == nil || len(lres.JSON200.Servers) != 1 {
		t.Fatalf("list: %d %s", lres.StatusCode(), lres.Body)
	}

	dres, err := e.c.DownServerWithResponse(ctx, "survival", &api.DownServerParams{})
	if err != nil {
		t.Fatal(err)
	}
	if dres.StatusCode() != http.StatusAccepted {
		t.Fatalf("down: %d %s", dres.StatusCode(), dres.Body)
	}
	if s := e.waitOp(t, "survival"); s.LastOperation.Kind != api.OperationKindDown || s.Status.State != api.ActualStateStopped {
		t.Errorf("after down = %+v", s)
	}

	rres, err := e.c.DeleteServerWithResponse(ctx, "survival", &api.DeleteServerParams{Purge: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if rres.StatusCode() != http.StatusNoContent {
		t.Fatalf("rm: %d %s", rres.StatusCode(), rres.Body)
	}
}

func TestLogs(t *testing.T) {
	e := newTestEnv(t, nil)
	e.create(t, "survival", 25565)
	t0 := time.Date(2026, 9, 27, 13, 38, 30, 0, time.UTC)
	e.fake.SetLogs("mcctl-survival.service", []engine.LogEntry{
		{Time: t0, Source: engine.LogSourceContainer, Message: "Done"},
		{Time: t0.Add(time.Second), Source: engine.LogSourceSystemd, Message: "Stopped"},
		{Time: t0.Add(2 * time.Second), Source: engine.LogSourceContainer, Message: "last"},
	})

	res, err := e.c.GetServerLogsWithResponse(t.Context(), "survival", &api.GetServerLogsParams{Tail: ptr(2)})
	if err != nil {
		t.Fatal(err)
	}
	if res.JSON200 == nil || len(res.JSON200.Entries) != 2 {
		t.Fatalf("logs: %d %s", res.StatusCode(), res.Body)
	}
	if got := res.JSON200.Entries[0]; got.Source != api.LogEntrySourceSystemd || got.Message != "Stopped" || !got.Time.Equal(t0.Add(time.Second)) {
		t.Errorf("entry = %+v", got)
	}
}

func TestErrorMapping(t *testing.T) {
	block := make(chan struct{})
	bu := &blockingUnits{}
	e := newTestEnv(t, func(u engine.Units) engine.Units { bu.Units = u; return bu })
	ctx := t.Context()
	e.create(t, "survival", 25565)
	e.create(t, "starting", 25566)
	e.create(t, "busy", 25567)

	// survival: 動いている（rm は server_running）。
	if _, err := e.c.UpServerWithResponse(ctx, "survival", &api.UpServerParams{}); err != nil {
		t.Fatal(err)
	}
	e.waitOp(t, "survival")
	// starting: 起動処理の途中（down は server_starting）。
	e.fake.SetStartStatus(engine.UnitStatus{LoadState: "loaded", ActiveState: "activating", SubState: "start"})
	if _, err := e.c.UpServerWithResponse(ctx, "starting", &api.UpServerParams{}); err != nil {
		t.Fatal(err)
	}
	e.waitOp(t, "starting")
	// busy: up の途中で止めておく（operation_in_progress）。
	bu.block = block
	if _, err := e.c.UpServerWithResponse(ctx, "busy", &api.UpServerParams{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(block) })
	e.fake.SetError("Tail", errors.New("journalctl: secret details"))

	type result struct {
		status int
		body   *api.Error
	}
	tests := []struct {
		name       string
		call       func() (result, error)
		wantStatus int
		wantCode   api.ErrorCode
	}{
		{"invalid_argument", func() (result, error) {
			r, err := e.c.CreateServerWithResponse(ctx, api.CreateServerJSONRequestBody{Name: "x", Port: 25570})
			return result{r.StatusCode(), errBody(r.JSON400, r.JSONDefault)}, err
		}, 400, api.ErrorCodeInvalidArgument},
		{"not_found", func() (result, error) {
			r, err := e.c.GetServerWithResponse(ctx, "nope")
			return result{r.StatusCode(), errBody(r.JSON404, r.JSONDefault)}, err
		}, 404, api.ErrorCodeNotFound},
		{"already_exists", func() (result, error) {
			r, err := e.c.CreateServerWithResponse(ctx, api.CreateServerJSONRequestBody{Name: "survival", Port: 25570, AcceptEula: true})
			return result{r.StatusCode(), errBody(r.JSON409, r.JSONDefault)}, err
		}, 409, api.ErrorCodeAlreadyExists},
		{"port_in_use", func() (result, error) {
			r, err := e.c.CreateServerWithResponse(ctx, api.CreateServerJSONRequestBody{Name: "other", Port: 25565, AcceptEula: true})
			return result{r.StatusCode(), errBody(r.JSON409, r.JSONDefault)}, err
		}, 409, api.ErrorCodePortInUse},
		{"operation_in_progress", func() (result, error) {
			r, err := e.c.DownServerWithResponse(ctx, "busy", &api.DownServerParams{Force: ptr(true)})
			return result{r.StatusCode(), errBody(r.JSON409, r.JSONDefault)}, err
		}, 409, api.ErrorCodeOperationInProgress},
		{"server_running", func() (result, error) {
			r, err := e.c.DeleteServerWithResponse(ctx, "survival", &api.DeleteServerParams{})
			return result{r.StatusCode(), errBody(r.JSON409, r.JSONDefault)}, err
		}, 409, api.ErrorCodeServerRunning},
		{"server_starting", func() (result, error) {
			r, err := e.c.DownServerWithResponse(ctx, "starting", &api.DownServerParams{})
			return result{r.StatusCode(), errBody(r.JSON409, r.JSONDefault)}, err
		}, 409, api.ErrorCodeServerStarting},
		{"internal", func() (result, error) {
			r, err := e.c.GetServerLogsWithResponse(ctx, "survival", &api.GetServerLogsParams{})
			return result{r.StatusCode(), errBody(nil, r.JSONDefault)}, err
		}, 500, api.ErrorCodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := tt.call()
			if err != nil {
				t.Fatal(err)
			}
			if r.status != tt.wantStatus || r.body == nil || r.body.Error.Code != tt.wantCode {
				t.Fatalf("status = %d, body = %+v, want %d %s", r.status, r.body, tt.wantStatus, tt.wantCode)
			}
			if tt.wantCode == api.ErrorCodeInternal && bytes.Contains([]byte(r.body.Error.Message), []byte("secret")) {
				t.Errorf("internal error leaks details: %q", r.body.Error.Message)
			}
		})
	}
}

func errBody(typed, def *api.Error) *api.Error {
	if typed != nil {
		return typed
	}
	return def
}

func TestRequestErrorsAreJSON(t *testing.T) {
	e := newTestEnv(t, nil)
	e.create(t, "survival", 25565)
	for _, tt := range []struct {
		name, method, path, body string
	}{
		{"bad json", http.MethodPost, "/v1/servers", "{"},
		{"bad query", http.MethodGet, "/v1/servers/survival/logs?tail=abc", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tt.method, "http://mcctld"+tt.path, bytes.NewBufferString(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			res, err := e.hc.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusBadRequest || res.Header.Get("Content-Type") != "application/json" {
				t.Errorf("status = %d, content-type = %q", res.StatusCode, res.Header.Get("Content-Type"))
			}
		})
	}
}
