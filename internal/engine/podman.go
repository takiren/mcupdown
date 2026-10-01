package engine

import "context"

// Podman は podman の Docker 互換 API のうち、mcctld が使う操作。
// コンテナの作成や起動は quadlet と systemd に任せるので、ここには含めない。
type Podman interface {
	// ImageExists はイメージが手元にあるかを返す。
	ImageExists(ctx context.Context, ref string) (bool, error)
	// PullImage はイメージを pull し、完了まで待つ。
	PullImage(ctx context.Context, ref string) error
	// ContainerHealth はコンテナの healthcheck の状態を返す。コンテナがなければ HealthNone。
	ContainerHealth(ctx context.Context, container string) (Health, error)
	// Rootless は podman が rootless で動いているかを返す。
	Rootless(ctx context.Context) (bool, error)
}

// Health はコンテナの healthcheck の状態。
type Health string

const (
	HealthNone      Health = "none"
	HealthStarting  Health = "starting"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)
