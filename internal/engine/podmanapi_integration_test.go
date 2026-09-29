//go:build integration

package engine

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestPodmanAPIIntegration は実際の rootless podman の互換 API で確かめる。
// mcctl ユーザーで XDG_RUNTIME_DIR を設定して実行する（podman.socket が有効であること）。
func TestPodmanAPIIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	p := NewPodmanAPI(DefaultPodmanSocket())

	if ok, err := p.Rootless(ctx); err != nil || !ok {
		t.Fatalf("Rootless = %v, %v", ok, err)
	}

	// 小さいイメージで pull と有無の判定を確かめ、最後に消す。
	const ref = "docker.io/library/busybox:latest"
	t.Cleanup(func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, "http://podman/images/"+ref, nil)
		if res, err := p.hc.Do(req); err == nil {
			_ = res.Body.Close()
		}
	})
	if err := p.PullImage(ctx, ref); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	if ok, err := p.ImageExists(ctx, ref); err != nil || !ok {
		t.Errorf("ImageExists after pull = %v, %v", ok, err)
	}
	if ok, err := p.ImageExists(ctx, "docker.io/library/busybox:no-such-tag-mcctl"); err != nil || ok {
		t.Errorf("ImageExists(missing) = %v, %v", ok, err)
	}
	if err := p.PullImage(ctx, "docker.io/library/busybox:no-such-tag-mcctl"); !errors.Is(err, ErrPodmanAPI) {
		t.Errorf("PullImage(missing tag) = %v", err)
	}
	if h, err := p.ContainerHealth(ctx, "mcctl-e2e-does-not-exist"); err != nil || h != HealthNone {
		t.Errorf("ContainerHealth(missing) = %q, %v", h, err)
	}
}
