package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// stubServer は往復の確認に使う最小の実装。未使用のメソッドは埋め込みで panic させる。
type stubServer struct {
	StrictServerInterface
}

func (stubServer) GetServer(_ context.Context, req GetServerRequestObject) (GetServerResponseObject, error) {
	if req.Name != "survival" {
		return GetServer404JSONResponse{ErrorJSONResponse{Error: ErrorBody{Code: ErrorCodeNotFound, Message: "server not found"}}}, nil
	}
	return GetServer200JSONResponse{
		Spec:         ServerSpec{Name: "survival", Port: 25565, Version: "1.21.4", Type: "PAPER", Memory: "2G", ImageTag: "latest", Env: map[string]string{}},
		DesiredState: DesiredStateRunning,
		Status:       Status{State: ActualStateRunning, Health: HealthHealthy, Restarts: 1},
	}, nil
}

func (stubServer) DownServer(_ context.Context, req DownServerRequestObject) (DownServerResponseObject, error) {
	if req.Params.Force == nil || !*req.Params.Force {
		return DownServer409JSONResponse{Error: ErrorBody{Code: ErrorCodeServerStarting, Message: "server is starting"}}, nil
	}
	return DownServer202JSONResponse{DesiredState: DesiredStateStopped}, nil
}

func startUnixServer(t *testing.T, h http.Handler) string {
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
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestUnixSocketRoundTrip(t *testing.T) {
	sock := startUnixServer(t, NewHandler(stubServer{}))
	c, err := NewUnixSocketClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	t.Run("get", func(t *testing.T) {
		res, err := c.GetServerWithResponse(ctx, "survival")
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode() != http.StatusOK || res.JSON200 == nil {
			t.Fatalf("status = %d, body = %s", res.StatusCode(), res.Body)
		}
		if got := res.JSON200.Status.State; got != ActualStateRunning {
			t.Errorf("state = %q, want %q", got, ActualStateRunning)
		}
	})

	t.Run("not found", func(t *testing.T) {
		res, err := c.GetServerWithResponse(ctx, "nope")
		if err != nil {
			t.Fatal(err)
		}
		if res.JSON404 == nil || res.JSON404.Error.Code != ErrorCodeNotFound {
			t.Fatalf("status = %d, body = %s", res.StatusCode(), res.Body)
		}
	})

	t.Run("down conflict without force", func(t *testing.T) {
		res, err := c.DownServerWithResponse(ctx, "survival", &DownServerParams{})
		if err != nil {
			t.Fatal(err)
		}
		if res.JSON409 == nil || res.JSON409.Error.Code != ErrorCodeServerStarting {
			t.Fatalf("status = %d, body = %s", res.StatusCode(), res.Body)
		}
	})

	t.Run("down with force", func(t *testing.T) {
		force := true
		res, err := c.DownServerWithResponse(ctx, "survival", &DownServerParams{Force: &force})
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode() != http.StatusAccepted || res.JSON202 == nil {
			t.Fatalf("status = %d, body = %s", res.StatusCode(), res.Body)
		}
	})

	t.Run("path without base is not routed", func(t *testing.T) {
		hc := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mcctld/servers/survival", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", res.StatusCode)
		}
	})
}
