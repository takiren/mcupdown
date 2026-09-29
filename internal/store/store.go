package store

import (
	"context"
	"errors"
	"maps"
)

// DesiredState はサーバーを動かしたいかどうか。
type DesiredState string

const (
	DesiredStateRunning DesiredState = "running"
	DesiredStateStopped DesiredState = "stopped"
)

// Server は Store に保存するサーバーの定義。
type Server struct {
	// Name はサーバーの識別子。作成後は変更できない。
	Name string `json:"name"`
	// Port はホスト側で公開するポート。
	Port int `json:"port"`
	// EULAAccepted は create のときに EULA に同意したかどうか。
	EULAAccepted bool `json:"eulaAccepted"`
	// Version は itzg の VERSION。
	Version string `json:"version"`
	// Type は itzg の TYPE。
	Type string `json:"type"`
	// Memory は JVM のヒープ（itzg の MEMORY）。例: "2G"
	Memory string `json:"memory"`
	// ImageTag は itzg/minecraft-server のタグ。
	ImageTag string `json:"imageTag"`
	// Env は itzg に渡す任意の環境変数。
	Env map[string]string `json:"env,omitempty"`
	// DesiredState は up で running、down で stopped にする。
	DesiredState DesiredState `json:"desiredState"`
}

// Clone は Env まで含めて複製する。Store の外に渡した値を書き換えても Store に影響しないようにする。
func (s Server) Clone() Server {
	s.Env = maps.Clone(s.Env)
	return s
}

var (
	// ErrNotFound は指定した名前のサーバーがないことを表す。
	ErrNotFound = errors.New("server not found")
	// ErrAlreadyExists は同じ名前のサーバーがすでにあることを表す。
	ErrAlreadyExists = errors.New("server already exists")
	// ErrRename は Update で名前を変えようとしたことを表す。
	ErrRename = errors.New("server name cannot be changed")
	// ErrUnsupportedVersion は保存ファイルの形式が、このバージョンの mcctld では読めないことを表す。
	ErrUnsupportedVersion = errors.New("unsupported state file version")
)

// Store はサーバーの定義を名前で保存・取得する。
// 実装は複数の goroutine から同時に呼んでも安全でなければならない。
type Store interface {
	// Get は名前でサーバーを取得する。ない場合は ErrNotFound。
	Get(ctx context.Context, name string) (Server, error)
	// List はすべてのサーバーを名前順で返す。
	List(ctx context.Context) ([]Server, error)
	// Create はサーバーを登録する。同じ名前がある場合は ErrAlreadyExists。
	Create(ctx context.Context, s Server) error
	// Update は fn で定義を書き換えて保存する。読み込みから保存までを1つの操作として行う。
	// ない場合は ErrNotFound。fn がエラーを返したら保存せずにそのエラーを返す。
	// 名前を変えた場合は ErrRename。
	Update(ctx context.Context, name string, fn func(*Server) error) error
	// Delete はサーバーを削除する。ない場合は ErrNotFound。
	Delete(ctx context.Context, name string) error
}
