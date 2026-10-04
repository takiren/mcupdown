package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/takiren/mcupdown/internal/api"
)

// stub は API のスタブ。受け取ったリクエストを記録し、err が設定されていればそれを返す。
type stub struct {
	api.StrictServerInterface

	mu   sync.Mutex
	last any
	err  *api.ErrorBody
}

func (s *stub) record(req any) *api.ErrorBody {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = req
	return s.err
}

func (s *stub) lastRequest() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

var testServer = api.Server{
	Spec: api.ServerSpec{
		Name: "survival", Port: 25565, Version: "1.21.4", Type: "PAPER", Memory: "4G", ImageTag: "java21",
		Env: map[string]string{"MOTD": "hello", "DIFFICULTY": "hard"},
	},
	DesiredState: api.DesiredStateRunning,
	Status:       api.Status{State: api.ActualStateStarting, Health: api.HealthStarting, Restarts: 2},
	LastOperation: &api.Operation{
		Kind: api.OperationKindUp, Result: api.OperationResultSucceeded,
		StartedAt: time.Date(2026, 9, 27, 14, 2, 3, 0, time.UTC),
	},
}

func (s *stub) ListServers(context.Context, api.ListServersRequestObject) (api.ListServersResponseObject, error) {
	other := testServer
	other.Spec = api.ServerSpec{Name: "creative", Port: 25566, Version: "1.21.4", Type: "VANILLA", Memory: "2G", ImageTag: "latest", Env: map[string]string{}}
	other.DesiredState = api.DesiredStateStopped
	other.Status = api.Status{State: api.ActualStateStopped, Health: api.HealthNone}
	return api.ListServers200JSONResponse{Servers: []api.Server{testServer, other}}, nil
}

func (s *stub) CreateServer(_ context.Context, req api.CreateServerRequestObject) (api.CreateServerResponseObject, error) {
	if e := s.record(*req.Body); e != nil {
		return api.CreateServer409JSONResponse{Error: *e}, nil
	}
	return api.CreateServer201JSONResponse(testServer), nil
}

func (s *stub) GetServer(_ context.Context, req api.GetServerRequestObject) (api.GetServerResponseObject, error) {
	if e := s.record(req.Name); e != nil {
		return api.GetServer404JSONResponse{ErrorJSONResponse: api.ErrorJSONResponse{Error: *e}}, nil
	}
	return api.GetServer200JSONResponse(testServer), nil
}

func (s *stub) UpServer(_ context.Context, req api.UpServerRequestObject) (api.UpServerResponseObject, error) {
	if e := s.record(req.Params); e != nil {
		return api.UpServer403JSONResponse{ErrorJSONResponse: api.ErrorJSONResponse{Error: *e}}, nil
	}
	return api.UpServer202JSONResponse(testServer), nil
}

func (s *stub) DownServer(_ context.Context, req api.DownServerRequestObject) (api.DownServerResponseObject, error) {
	if e := s.record(req.Params); e != nil {
		return api.DownServer409JSONResponse{Error: *e}, nil
	}
	return api.DownServer202JSONResponse(testServer), nil
}

func (s *stub) DeleteServer(_ context.Context, req api.DeleteServerRequestObject) (api.DeleteServerResponseObject, error) {
	if e := s.record(req.Params); e != nil {
		return api.DeleteServer409JSONResponse{Error: *e}, nil
	}
	return api.DeleteServer204Response{}, nil
}

func (s *stub) GetServerLogs(_ context.Context, req api.GetServerLogsRequestObject) (api.GetServerLogsResponseObject, error) {
	s.record(req.Params)
	t := time.Date(2026, 9, 27, 13, 38, 30, 0, time.UTC)
	return api.GetServerLogs200JSONResponse{Entries: []api.LogEntry{
		{Time: t, Source: api.LogEntrySourceContainer, Message: "Done (10.4s)!"},
		{Time: t.Add(5 * time.Second), Source: api.LogEntrySourceSystemd, Message: "Scheduled restart job, restart counter is at 1."},
	}}, nil
}

func startServer(t *testing.T, s *stub) string {
	t.Helper()
	// macOS は Unix ソケットのパス長の上限が短いので、短い一時ディレクトリを使う。
	dir, err := os.MkdirTemp("", "mcctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: api.NewHandler(s)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

type result struct {
	code           int
	stdout, stderr string
}

func run(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, loc: time.UTC, getenv: func(k string) string { return env[k] }}
	code := a.run(args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func runWith(t *testing.T, s *stub, args ...string) result {
	t.Helper()
	sock := startServer(t, s)
	return run(t, nil, append([]string{"--socket", sock}, args...)...)
}

func assertContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("出力に %q が含まれていない:\n%s", w, got)
		}
	}
}

func TestList(t *testing.T) {
	r := runWith(t, &stub{}, "list")
	if r.code != 0 {
		t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
	}
	want := "NAME      TYPE     VERSION  PORT   DESIRED  ACTUAL\n" +
		"survival  PAPER    1.21.4   25565  running  starting\n" +
		"creative  VANILLA  1.21.4   25566  stopped  stopped\n"
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
}

func TestListJSON(t *testing.T) {
	r := runWith(t, &stub{}, "list", "-o", "json")
	if r.code != 0 {
		t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
	}
	var got api.ServerList
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("JSON として読めない: %v\n%s", err, r.stdout)
	}
	if len(got.Servers) != 2 || got.Servers[0].Spec.Name != "survival" {
		t.Errorf("servers = %+v", got.Servers)
	}
	if !strings.Contains(r.stdout, "\n  ") {
		t.Errorf("整形されていない:\n%s", r.stdout)
	}
}

func TestInvalidOutput(t *testing.T) {
	for _, args := range [][]string{{"list", "-o", "yaml"}, {"status", "survival", "-o", "yaml"}} {
		r := runWith(t, &stub{}, args...)
		if r.code != 1 {
			t.Errorf("%v: code = %d", args, r.code)
		}
		assertContains(t, r.stderr, "不正な出力形式")
	}
}

func TestStatus(t *testing.T) {
	r := runWith(t, &stub{}, "status", "survival")
	if r.code != 0 {
		t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
	}
	assertContains(t, r.stdout,
		"Name:      survival\n",
		"Desired:   running\n",
		"Actual:    starting (health: starting)\n",
		"Restarts:  2\n",
		"Image:     itzg/minecraft-server:java21\n",
		"Env:       DIFFICULTY=hard MOTD=hello\n",
		"Last op:   up  2026-09-27 14:02:03  ok\n",
	)
}

func TestStatusJSON(t *testing.T) {
	r := runWith(t, &stub{}, "status", "survival", "-o", "json")
	var got api.Server
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("JSON として読めない: %v\n%s", err, r.stdout)
	}
	if got.Status.Restarts != 2 {
		t.Errorf("restarts = %d", got.Status.Restarts)
	}
}

func TestStatusNotFound(t *testing.T) {
	s := &stub{err: &api.ErrorBody{Code: api.ErrorCodeNotFound, Message: "server nope not found"}}
	r := runWith(t, s, "status", "nope")
	if r.code != 1 {
		t.Fatalf("code = %d", r.code)
	}
	assertContains(t, r.stderr, "エラー: server nope not found", "mcctl list")
}

func TestFormatOperation(t *testing.T) {
	a := &app{loc: time.UTC}
	msg := "pull failed"
	op := &api.Operation{Kind: api.OperationKindUp, Result: api.OperationResultFailed, StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Error: &msg}
	if got, want := a.formatOperation(op), "up  2026-01-02 03:04:05  failed: pull failed"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := a.formatOperation(nil); got != "-" {
		t.Errorf("nil: got %q", got)
	}
}

func TestCreate(t *testing.T) {
	t.Run("フラグをリクエストに渡す", func(t *testing.T) {
		s := &stub{}
		r := runWith(t, s, "create", "survival", "--port", "25565", "--accept-eula",
			"--version", "1.21.4", "--memory", "4G", "--env", "MOTD=a=b", "--env", "PVP=false")
		if r.code != 0 {
			t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
		}
		req := s.lastRequest().(api.CreateServerRequest)
		if req.Name != "survival" || req.Port != 25565 || !req.AcceptEula {
			t.Errorf("req = %+v", req)
		}
		if req.Version == nil || *req.Version != "1.21.4" || req.Memory == nil || *req.Memory != "4G" {
			t.Errorf("version / memory = %v / %v", req.Version, req.Memory)
		}
		// 指定しなかったフラグは送らない（mcctld の既定値を使う）
		if req.Type != nil || req.ImageTag != nil {
			t.Errorf("type / imageTag が送られている: %v / %v", req.Type, req.ImageTag)
		}
		if req.Env == nil || (*req.Env)["MOTD"] != "a=b" || (*req.Env)["PVP"] != "false" {
			t.Errorf("env = %v", req.Env)
		}
		assertContains(t, r.stdout, "survival を登録しました", "mcctl up survival")
	})

	t.Run("EULA に同意していなければ送らない", func(t *testing.T) {
		s := &stub{}
		r := runWith(t, s, "create", "survival", "--port", "25565")
		if r.code != 1 {
			t.Fatalf("code = %d", r.code)
		}
		assertContains(t, r.stderr, "--accept-eula")
		if s.lastRequest() != nil {
			t.Error("リクエストが送られた")
		}
	})

	t.Run("不正な env", func(t *testing.T) {
		r := runWith(t, &stub{}, "create", "survival", "--port", "1", "--accept-eula", "--env", "NOVALUE")
		if r.code != 1 {
			t.Fatalf("code = %d", r.code)
		}
		assertContains(t, r.stderr, "KEY=VAL")
	})

	t.Run("port は必須", func(t *testing.T) {
		r := runWith(t, &stub{}, "create", "survival", "--accept-eula")
		if r.code != 1 {
			t.Fatalf("code = %d", r.code)
		}
		assertContains(t, r.stderr, "port")
	})

	t.Run("ポートの重複", func(t *testing.T) {
		s := &stub{err: &api.ErrorBody{Code: api.ErrorCodePortInUse, Message: "port 25565 is used by creative"}}
		r := runWith(t, s, "create", "survival", "--port", "25565", "--accept-eula")
		if r.code != 1 {
			t.Fatalf("code = %d", r.code)
		}
		assertContains(t, r.stderr, "port 25565 is used by creative", "使用中のポート")
	})
}

func TestUp(t *testing.T) {
	s := &stub{}
	r := runWith(t, s, "up", "survival", "--pull")
	if r.code != 0 {
		t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
	}
	p := s.lastRequest().(api.UpServerParams)
	if p.Pull == nil || !*p.Pull {
		t.Errorf("pull = %v", p.Pull)
	}
	assertContains(t, r.stdout, "起動を受け付けました", "mcctl status survival")

	r = runWith(t, s, "up", "survival")
	if p := s.lastRequest().(api.UpServerParams); p.Pull != nil {
		t.Errorf("--pull なしで pull = %v", *p.Pull)
	}
}

func TestUpForbidden(t *testing.T) {
	s := &stub{err: &api.ErrorBody{Code: api.ErrorCodeForbidden, Message: "permission denied"}}
	r := runWith(t, s, "up", "survival")
	if r.code != 1 {
		t.Fatalf("code = %d", r.code)
	}
	assertContains(t, r.stderr, "permission denied", "mcctl グループ")
}

func TestDown(t *testing.T) {
	t.Run("起動処理の途中なら --force を案内する", func(t *testing.T) {
		s := &stub{err: &api.ErrorBody{Code: api.ErrorCodeServerStarting, Message: "server survival is starting"}}
		r := runWith(t, s, "down", "survival")
		if r.code != 1 {
			t.Fatalf("code = %d", r.code)
		}
		assertContains(t, r.stderr, "mcctl down survival --force")
		if p := s.lastRequest().(api.DownServerParams); p.Force != nil {
			t.Errorf("force = %v", *p.Force)
		}
	})

	t.Run("--force を渡す", func(t *testing.T) {
		s := &stub{}
		r := runWith(t, s, "down", "survival", "--force")
		if r.code != 0 {
			t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
		}
		if p := s.lastRequest().(api.DownServerParams); p.Force == nil || !*p.Force {
			t.Errorf("force = %v", p.Force)
		}
		assertContains(t, r.stdout, "停止を受け付けました")
	})

	t.Run("操作中", func(t *testing.T) {
		s := &stub{err: &api.ErrorBody{Code: api.ErrorCodeOperationInProgress, Message: "operation in progress"}}
		r := runWith(t, s, "down", "survival")
		assertContains(t, r.stderr, "mcctl status survival")
	})
}

func TestRm(t *testing.T) {
	t.Run("--purge を渡す", func(t *testing.T) {
		s := &stub{}
		r := runWith(t, s, "rm", "survival", "--purge")
		if r.code != 0 {
			t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
		}
		if p := s.lastRequest().(api.DeleteServerParams); p.Purge == nil || !*p.Purge {
			t.Errorf("purge = %v", p.Purge)
		}
		assertContains(t, r.stdout, "データごと削除しました")
	})

	t.Run("動いていれば down を案内する", func(t *testing.T) {
		s := &stub{err: &api.ErrorBody{Code: api.ErrorCodeServerRunning, Message: "server survival is running"}}
		r := runWith(t, s, "rm", "survival")
		if r.code != 1 {
			t.Fatalf("code = %d", r.code)
		}
		assertContains(t, r.stderr, "mcctl down survival")
	})
}

func TestLogs(t *testing.T) {
	s := &stub{}
	r := runWith(t, s, "logs", "survival", "--tail", "50")
	if r.code != 0 {
		t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
	}
	if p := s.lastRequest().(api.GetServerLogsParams); p.Tail == nil || *p.Tail != 50 {
		t.Errorf("tail = %v", p.Tail)
	}
	want := "2026-09-27 13:38:30 [container] Done (10.4s)!\n" +
		"2026-09-27 13:38:35 [systemd] Scheduled restart job, restart counter is at 1.\n"
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}

	r = runWith(t, s, "logs", "survival")
	if p := s.lastRequest().(api.GetServerLogsParams); p.Tail != nil {
		t.Errorf("--tail なしで tail = %d", *p.Tail)
	}
}

func TestSocket(t *testing.T) {
	t.Run("接続できなければ案内する", func(t *testing.T) {
		r := run(t, nil, "--socket", filepath.Join(t.TempDir(), "none.sock"), "list")
		if r.code != 1 {
			t.Fatalf("code = %d", r.code)
		}
		assertContains(t, r.stderr, "mcctld に接続できません", "none.sock", "--socket")
	})

	t.Run("環境変数を使う", func(t *testing.T) {
		sock := startServer(t, &stub{})
		r := run(t, map[string]string{socketEnv: sock}, "list")
		if r.code != 0 {
			t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
		}
	})

	t.Run("フラグが環境変数より優先される", func(t *testing.T) {
		sock := startServer(t, &stub{})
		r := run(t, map[string]string{socketEnv: "/nonexistent/mcctld.sock"}, "--socket", sock, "list")
		if r.code != 0 {
			t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
		}
	})

	t.Run("既定のパス", func(t *testing.T) {
		r := run(t, nil, "list")
		if r.code != 1 {
			t.Skip("既定のソケットに mcctld が動いている")
		}
		assertContains(t, r.stderr, defaultSocket)
	})
}
