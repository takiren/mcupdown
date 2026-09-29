package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ErrPodmanAPI は podman の互換 API がエラーを返したときのエラー。
var ErrPodmanAPI = errors.New("podman API error")

// DefaultPodmanSocket は rootless podman の API ソケットの既定のパス（podman.socket ユーザーユニット）。
func DefaultPodmanSocket() string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "podman", "podman.sock")
}

// PodmanAPI は Podman の実装。podman の Docker 互換 API を Unix ソケット越しに叩く。
// Docker SDK は重いので、使う数個のエンドポイントだけを手書きしている。
type PodmanAPI struct {
	hc *http.Client
}

var _ Podman = (*PodmanAPI)(nil)

// NewPodmanAPI は socketPath の API に接続するクライアントを返す。
func NewPodmanAPI(socketPath string) *PodmanAPI {
	return &PodmanAPI{hc: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

// refPattern は URL のパスにそのまま埋め込めるイメージやコンテナの参照。
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)

func (p *PodmanAPI) do(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	u := "http://podman" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	return p.hc.Do(req)
}

func (p *PodmanAPI) ImageExists(ctx context.Context, ref string) (bool, error) {
	if !refPattern.MatchString(ref) {
		return false, fmt.Errorf("invalid image reference %q", ref)
	}
	res, err := p.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, apiError(res)
	}
}

func (p *PodmanAPI) PullImage(ctx context.Context, ref string) error {
	repo, tag, err := splitImageRef(ref)
	if err != nil {
		return err
	}
	res, err := p.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {repo}, "tag": {tag}})
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return apiError(res)
	}
	// 200 のまま、ストリームの途中でエラーが返ることもある。最後まで読んで完了を待つ。
	if err := checkPullStream(res.Body); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}

func (p *PodmanAPI) ContainerHealth(ctx context.Context, container string) (Health, error) {
	if !refPattern.MatchString(container) {
		return "", fmt.Errorf("invalid container name %q", container)
	}
	res, err := p.do(ctx, http.MethodGet, "/containers/"+container+"/json", nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusOK:
		return healthFromInspect(res.Body)
	case http.StatusNotFound:
		return HealthNone, nil
	default:
		return "", apiError(res)
	}
}

func (p *PodmanAPI) Rootless(ctx context.Context) (bool, error) {
	res, err := p.do(ctx, http.MethodGet, "/info", nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return false, apiError(res)
	}
	return rootlessFromInfo(res.Body)
}

// splitImageRef は "docker.io/itzg/minecraft-server:java21" をリポジトリとタグに分ける。
func splitImageRef(ref string) (repo, tag string, err error) {
	i := strings.LastIndex(ref, ":")
	if !refPattern.MatchString(ref) || i <= 0 || strings.Contains(ref[i:], "/") || i == len(ref)-1 {
		return "", "", fmt.Errorf("invalid image reference %q (want repository:tag)", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// checkPullStream は /images/create の進捗のストリーム（JSON の連続）を最後まで読み、エラーがあれば返す。
func checkPullStream(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read pull progress: %w", err)
		}
		if msg.ErrorDetail.Message != "" {
			return fmt.Errorf("%w: %s", ErrPodmanAPI, msg.ErrorDetail.Message)
		}
		if msg.Error != "" {
			return fmt.Errorf("%w: %s", ErrPodmanAPI, msg.Error)
		}
	}
}

// healthFromInspect は GET /containers/{name}/json の応答から health を取り出す。
// healthcheck がないコンテナは HealthNone。
func healthFromInspect(r io.Reader) (Health, error) {
	var body struct {
		State struct {
			Health *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return "", fmt.Errorf("decode container inspect: %w", err)
	}
	if body.State.Health == nil {
		return HealthNone, nil
	}
	switch h := Health(body.State.Health.Status); h {
	case HealthStarting, HealthHealthy, HealthUnhealthy:
		return h, nil
	default:
		return HealthNone, nil
	}
}

// rootlessFromInfo は GET /info の SecurityOptions に name=rootless があるかを返す（Docker と同じ判定方法）。
func rootlessFromInfo(r io.Reader) (bool, error) {
	var body struct {
		SecurityOptions []string `json:"SecurityOptions"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return false, fmt.Errorf("decode info: %w", err)
	}
	return slices.Contains(body.SecurityOptions, "name=rootless"), nil
}

// apiError は podman のエラーの応答（{"cause", "message", "response"}）をエラーにする。
func apiError(res *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var body struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &body) == nil && body.Message != "" {
		msg = body.Message
	}
	return fmt.Errorf("%w: %s %s: %d %s", ErrPodmanAPI, res.Request.Method, res.Request.URL.Path, res.StatusCode, msg)
}
