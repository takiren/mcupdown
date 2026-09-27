package store

import (
	"context"
	"errors"
	"time"
)

type MinecraftContainer struct {
	// マイクラのバージョン
	version string
	//コンテナのイメージタグ
	imageTag string
	// 起動した時の時間
	startedAt time.Time
	//　ポート番号
	port uint
}

var (
	ErrNotFound      = errors.New("Failed to read")
	ErrUnexpected    = errors.New("Unexpected error")
	ErrCouldNotWrite = errors.New("Failed to write")
)

type ContainerInfo struct {
	// マイクラのコンテナ
	mcContainers []MinecraftContainer
}

// コンテナがどのような状態かを保存したり読んだりするインターフェース
// ファイル直接書き込みかDBを想定
// 別に中身がAPIでもいい
type Store interface {
	Read(ctx context.Context) (*MinecraftContainer, error)
	Write(ctx context.Context, _ MinecraftContainer) error
}
