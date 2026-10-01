package engine

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePodman は podman の互換 API のうち、PodmanAPI が使うエンドポイントを真似る。
func fakePodman(t *testing.T) *PodmanAPI {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /images/{ref...}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("ref") {
		case "docker.io/itzg/minecraft-server:latest/json":
			_, _ = io.WriteString(w, `{"Id":"x"}`)
		case "docker.io/itzg/minecraft-server:broken/json":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"cause":"x","message":"storage is broken","response":500}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"cause":"no such image","message":"No such image","response":404}`)
		}
	})
	mux.HandleFunc("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("fromImage") != "docker.io/itzg/minecraft-server" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"denied: requested access to the resource is denied"}`)
			return
		}
		switch q.Get("tag") {
		case "latest":
			_, _ = io.WriteString(w, `{"status":"Pulling fs layer","id":"a"}`+"\n"+`{"status":"Download complete","id":"a"}`+"\n")
		case "midway":
			_, _ = io.WriteString(w, `{"status":"Pulling fs layer","id":"a"}`+"\n"+`{"error":"unexpected EOF","errorDetail":{"message":"unexpected EOF"}}`+"\n")
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"manifest unknown: manifest unknown"}`)
		}
	})
	mux.HandleFunc("GET /containers/{name}/json", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("name") {
		case "mcctl-healthy":
			_, _ = io.WriteString(w, `{"State":{"Status":"running","Health":{"Status":"healthy","FailingStreak":0}}}`)
		case "mcctl-starting":
			_, _ = io.WriteString(w, `{"State":{"Status":"running","Health":{"Status":"starting"}}}`)
		case "mcctl-nocheck":
			_, _ = io.WriteString(w, `{"State":{"Status":"running"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"cause":"no such container","message":"no container","response":404}`)
		}
	})
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"SecurityOptions":["name=seccomp,profile=default","name=rootless","name=selinux"]}`)
	})

	dir, err := os.MkdirTemp("", "podman")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "podman.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return NewPodmanAPI(sock)
}

func TestPodmanAPI(t *testing.T) {
	p := fakePodman(t)
	ctx := t.Context()

	t.Run("image exists", func(t *testing.T) {
		if ok, err := p.ImageExists(ctx, "docker.io/itzg/minecraft-server:latest"); err != nil || !ok {
			t.Errorf("= %v, %v", ok, err)
		}
		if ok, err := p.ImageExists(ctx, "docker.io/itzg/minecraft-server:java8"); err != nil || ok {
			t.Errorf("missing = %v, %v", ok, err)
		}
		_, err := p.ImageExists(ctx, "docker.io/itzg/minecraft-server:broken")
		if !errors.Is(err, ErrPodmanAPI) || !strings.Contains(err.Error(), "storage is broken") {
			t.Errorf("broken = %v", err)
		}
		if _, err := p.ImageExists(ctx, "../../info"); err == nil {
			t.Error("invalid ref accepted")
		}
	})

	t.Run("pull", func(t *testing.T) {
		if err := p.PullImage(ctx, "docker.io/itzg/minecraft-server:latest"); err != nil {
			t.Errorf("ok pull: %v", err)
		}
		err := p.PullImage(ctx, "docker.io/itzg/minecraft-server:midway")
		if !errors.Is(err, ErrPodmanAPI) || !strings.Contains(err.Error(), "unexpected EOF") {
			t.Errorf("error in stream = %v", err)
		}
		err = p.PullImage(ctx, "docker.io/itzg/minecraft-server:no-such-tag")
		if !errors.Is(err, ErrPodmanAPI) || !strings.Contains(err.Error(), "manifest unknown") {
			t.Errorf("unknown tag = %v", err)
		}
		if err := p.PullImage(ctx, "docker.io/library/nope:latest"); !errors.Is(err, ErrPodmanAPI) {
			t.Errorf("denied = %v", err)
		}
	})

	t.Run("health", func(t *testing.T) {
		for name, want := range map[string]Health{
			"mcctl-healthy":  HealthHealthy,
			"mcctl-starting": HealthStarting,
			"mcctl-nocheck":  HealthNone,
			"mcctl-missing":  HealthNone,
		} {
			if got, err := p.ContainerHealth(ctx, name); err != nil || got != want {
				t.Errorf("%s = %q, %v; want %q", name, got, err, want)
			}
		}
	})

	t.Run("rootless", func(t *testing.T) {
		if ok, err := p.Rootless(ctx); err != nil || !ok {
			t.Errorf("= %v, %v", ok, err)
		}
	})
}

func TestSplitImageRef(t *testing.T) {
	for ref, want := range map[string][2]string{
		"docker.io/itzg/minecraft-server:java21": {"docker.io/itzg/minecraft-server", "java21"},
		"localhost:5000/mc:latest":               {"localhost:5000/mc", "latest"},
	} {
		repo, tag, err := splitImageRef(ref)
		if err != nil || repo != want[0] || tag != want[1] {
			t.Errorf("%s = %q %q %v", ref, repo, tag, err)
		}
	}
	for _, bad := range []string{"", "no-tag", "localhost:5000/mc", "repo:", ":tag", "a b:c"} {
		if _, _, err := splitImageRef(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParsers(t *testing.T) {
	if err := checkPullStream(strings.NewReader("")); err != nil {
		t.Errorf("empty stream: %v", err)
	}
	if err := checkPullStream(strings.NewReader(`{"status":"ok"}{"error":"boom"}`)); !errors.Is(err, ErrPodmanAPI) {
		t.Errorf("error without detail: %v", err)
	}
	if err := checkPullStream(strings.NewReader(`{"status":`)); err == nil {
		t.Error("truncated stream accepted")
	}
	if h, err := healthFromInspect(strings.NewReader(`{"State":{"Health":{"Status":""}}}`)); err != nil || h != HealthNone {
		t.Errorf("empty health = %q, %v", h, err)
	}
	if ok, err := rootlessFromInfo(strings.NewReader(`{"SecurityOptions":["name=seccomp"]}`)); err != nil || ok {
		t.Errorf("rootful = %v, %v", ok, err)
	}
}
