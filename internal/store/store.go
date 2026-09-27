package store

import (
	"context"
	"errors"
	"time"
)

type MinecraftContainer struct {
	// マイクラのバージョン
	Version string
	//コンテナのイメージタグ
	ImageTag string
	// 起動した時の時間
	StartedAt time.Time
	//　ポート番号
	Port uint
}

var (
	ErrNotFound      = errors.New("failed to read")
	ErrUnexpected    = errors.New("unexpected error")
	ErrCouldNotWrite = errors.New("failed to write")
)

type ContainerInfo struct {
	// マイクラのコンテナ
	MCContainers []MinecraftContainer
}

// コンテナがどのような状態かを保存したり読んだりするインターフェース
// ファイル直接書き込みかDBを想定
// 別に中身がAPIでもいい
type Store interface {
	Read(ctx context.Context) (*MinecraftContainer, error)
	Write(ctx context.Context, _ MinecraftContainer) error
}
