package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/takiren/mcupdown/internal/engine"
	"github.com/takiren/mcupdown/internal/store"
)

// 操作の失敗の種類。ハンドラは errors.Is で判定して API の ErrorCode に読み替える。
var (
	// ErrInvalidArgument は入力の検証エラー。
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound はサーバーがないことを表す。
	ErrNotFound = store.ErrNotFound
	// ErrAlreadyExists は同じ名前のサーバーがあることを表す。
	ErrAlreadyExists = store.ErrAlreadyExists
	// ErrPortInUse は他のサーバーとポートが重複していることを表す。
	ErrPortInUse = errors.New("port is already used by another server")
	// ErrOperationInProgress は同じサーバーへの操作を実行中であることを表す。
	ErrOperationInProgress = errors.New("another operation is in progress")
	// ErrServerRunning は動いているサーバーを削除しようとしたことを表す。
	ErrServerRunning = errors.New("server is running")
	// ErrServerStarting は起動処理の途中のサーバーを force なしで止めようとしたことを表す。
	ErrServerStarting = errors.New("server is starting")
)

// 省略されたときの値。api/openapi.yaml の default と合わせる。
const (
	DefaultVersion  = "LATEST"
	DefaultType     = "VANILLA"
	DefaultMemory   = "2G"
	DefaultImageTag = "latest"

	DefaultLogTail = 200
	MaxLogTail     = 10000
)

// Engine はデーモンが使う engine のインターフェースをまとめたもの。
type Engine struct {
	Files  engine.UnitFiles
	Units  engine.Units
	Podman engine.Podman
	Logs   engine.Logs
}

// Config はデーモンの設定。
type Config struct {
	// DataRoot はサーバーのデータディレクトリを置く場所。<DataRoot>/<name> をコンテナの /data にする。
	DataRoot string
}

// Daemon はサーバーの操作を行う。複数の goroutine から同時に呼んでも安全。
type Daemon struct {
	store    store.Store
	eng      Engine
	dataRoot string
	now      func() time.Time

	// ctx は非同期の操作に使う。リクエストの context は応答とともに終わるので使えない。
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// createMu はポートの重複チェックから登録までを1つずつ行うためのロック。
	createMu sync.Mutex

	mu      sync.Mutex
	busy    map[string]bool
	lastOps map[string]Operation
}

// New はデーモンを作る。DataRoot が .container ファイルに書けないパスならエラーを返す。
func New(st store.Store, eng Engine, cfg Config) (*Daemon, error) {
	// 設定の誤りが、up のときに利用者の入力エラーとして見えないよう、ここで確かめる。
	probe := engine.Server{
		Name: "probe", Port: 25565, Version: DefaultVersion, Type: DefaultType,
		Memory: DefaultMemory, ImageTag: DefaultImageTag, DataDir: filepath.Join(cfg.DataRoot, "probe"),
	}
	if !filepath.IsAbs(cfg.DataRoot) || probe.Validate() != nil {
		return nil, fmt.Errorf("data root %q must be an absolute path of [A-Za-z0-9/._-]", cfg.DataRoot)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Daemon{
		store:    st,
		eng:      eng,
		dataRoot: cfg.DataRoot,
		now:      time.Now,
		ctx:      ctx,
		cancel:   cancel,
		busy:     map[string]bool{},
		lastOps:  map[string]Operation{},
	}, nil
}

// Shutdown は実行中の非同期の操作が終わるのを待つ。ctx が先に終わったら操作を中断させる。
// 中断しても systemd のジョブは止まらないので、サーバーが中途半端な状態になることはない。
func (d *Daemon) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		d.cancel()
		return nil
	case <-ctx.Done():
		d.cancel()
		<-done
		return ctx.Err()
	}
}

// acquire はサーバーの操作中のフラグを立てる。すでに立っていたら ErrOperationInProgress。
func (d *Daemon) acquire(name string) (release func(), err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy[name] {
		return nil, ErrOperationInProgress
	}
	d.busy[name] = true
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		delete(d.busy, name)
	}, nil
}

// OperationKind は非同期の操作の種類。
type OperationKind string

const (
	OperationUp   OperationKind = "up"
	OperationDown OperationKind = "down"
)

// OperationResult は非同期の操作の結果。
type OperationResult string

const (
	OperationInProgress OperationResult = "in_progress"
	OperationSucceeded  OperationResult = "succeeded"
	OperationFailed     OperationResult = "failed"
)

// Operation はサーバーごとの最後の操作。メモリにだけ持ち、デーモンを再起動すると消える。
type Operation struct {
	Kind       OperationKind
	Result     OperationResult
	StartedAt  time.Time
	FinishedAt time.Time // 終わっていなければゼロ値
	Error      string    // Result が failed のときの理由
}

// runAsync は fn を非同期で実行し、最後の操作として記録する。終わったら release を呼ぶ。
func (d *Daemon) runAsync(name string, kind OperationKind, release func(), fn func(ctx context.Context) error) Operation {
	op := Operation{Kind: kind, Result: OperationInProgress, StartedAt: d.now()}
	d.setLastOp(name, op)

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer release()
		err := fn(d.ctx)
		op.FinishedAt = d.now()
		if err != nil {
			op.Result, op.Error = OperationFailed, err.Error()
		} else {
			op.Result = OperationSucceeded
		}
		d.setLastOp(name, op)
	}()
	return op
}

func (d *Daemon) setLastOp(name string, op Operation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastOps[name] = op
}

func (d *Daemon) lastOp(name string) (Operation, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	op, ok := d.lastOps[name]
	return op, ok
}

func (d *Daemon) forgetLastOp(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.lastOps, name)
}
