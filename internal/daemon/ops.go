package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"

	"github.com/takiren/mcupdown/internal/engine"
	"github.com/takiren/mcupdown/internal/store"
)

// CreateRequest は create の入力。空のフィールドは既定値になる。
type CreateRequest struct {
	Name       string
	Port       int
	AcceptEULA bool
	Version    string
	Type       string
	Memory     string
	ImageTag   string
	Env        map[string]string
}

// Server はサーバーの定義と、実際の状態をまとめたもの。
type Server struct {
	// Spec は Store に保存されている定義（求める状態を含む）。
	Spec store.Server
	// State は systemd の unit の状態を読み替えたもの。取得できなければ unknown。
	State engine.State
	// Health はコンテナの healthcheck の状態。コンテナがない、または取得できなければ none。
	Health engine.Health
	// Restarts は systemd が自動で再起動した回数。
	Restarts uint32
	// LastOp は最後の操作。デーモンが起動してから操作していなければ nil。
	LastOp *Operation
}

// Create はサーバーを登録し、データディレクトリを作る。サーバーは stopped で登録される。
func (d *Daemon) Create(ctx context.Context, req CreateRequest) (Server, error) {
	if !engine.ValidName(req.Name) {
		return Server{}, fmt.Errorf("%w: name %q must match ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$", ErrInvalidArgument, req.Name)
	}
	if !req.AcceptEULA {
		return Server{}, fmt.Errorf("%w: the Minecraft EULA must be accepted", ErrInvalidArgument)
	}
	s := store.Server{
		Name:         req.Name,
		Port:         req.Port,
		EULAAccepted: true,
		Version:      orDefault(req.Version, DefaultVersion),
		Type:         orDefault(req.Type, DefaultType),
		Memory:       orDefault(req.Memory, DefaultMemory),
		ImageTag:     orDefault(req.ImageTag, DefaultImageTag),
		Env:          maps.Clone(req.Env),
		DesiredState: store.DesiredStateStopped,
	}
	// .container ファイルに書けない値は、up のときではなくここで弾く。
	if err := d.engineServer(s).Validate(); err != nil {
		return Server{}, fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}

	release, err := d.acquire(s.Name)
	if err != nil {
		return Server{}, err
	}
	defer release()

	d.createMu.Lock()
	defer d.createMu.Unlock()

	all, err := d.store.List(ctx)
	if err != nil {
		return Server{}, err
	}
	for _, other := range all {
		if other.Name == s.Name {
			return Server{}, fmt.Errorf("%w: %s", ErrAlreadyExists, s.Name)
		}
		if other.Port == s.Port {
			return Server{}, fmt.Errorf("%w: %d is used by %s", ErrPortInUse, s.Port, other.Name)
		}
	}

	if err := d.store.Create(ctx, s); err != nil {
		return Server{}, err
	}
	// rm（--purge なし）の後に同じ名前で作り直すと、既存のワールドがそのまま使われる。
	if err := os.MkdirAll(d.dataDir(s.Name), 0o750); err != nil {
		if derr := d.store.Delete(ctx, s.Name); derr != nil {
			slog.Error("failed to roll back create", "server", s.Name, "error", derr)
		}
		return Server{}, fmt.Errorf("create data directory: %w", err)
	}
	return d.view(ctx, s), nil
}

// Up はサーバーの求める状態を running にし、起動を非同期で始める。
// pull が true なら、イメージが手元にあっても pull する。
func (d *Daemon) Up(ctx context.Context, name string, pull bool) (Server, error) {
	release, err := d.acquire(name)
	if err != nil {
		return Server{}, err
	}
	s, err := d.setDesired(ctx, name, store.DesiredStateRunning)
	if err != nil {
		release()
		return Server{}, err
	}

	d.runAsync(name, OperationUp, release, func(ctx context.Context) error {
		ref := engine.ImageRef(s.ImageTag)
		if !pull {
			exists, err := d.eng.Podman.ImageExists(ctx, ref)
			if err != nil {
				return fmt.Errorf("check image: %w", err)
			}
			pull = !exists
		}
		if pull {
			if err := d.eng.Podman.PullImage(ctx, ref); err != nil {
				return fmt.Errorf("pull %s: %w", ref, err)
			}
		}
		if err := d.writeUnit(ctx, s); err != nil {
			return err
		}
		// Start は起動ジョブを登録するだけで、healthy になるのは待たない。
		if err := d.eng.Units.Start(ctx, engine.UnitName(name)); err != nil {
			return fmt.Errorf("start unit: %w", err)
		}
		return nil
	})
	return d.view(ctx, s), nil
}

// Down はサーバーの求める状態を stopped にし、停止を非同期で始める。
// 起動処理の途中なら、force が true でない限り ErrServerStarting を返す。
func (d *Daemon) Down(ctx context.Context, name string, force bool) (Server, error) {
	release, err := d.acquire(name)
	if err != nil {
		return Server{}, err
	}
	if _, err := d.store.Get(ctx, name); err != nil {
		release()
		return Server{}, err
	}
	// 起動処理の途中の SIGTERM は無視され、60 秒後に SIGKILL される（#2）。ワールドを壊しかねないので既定では断る。
	if !force {
		st, err := d.eng.Units.Status(ctx, engine.UnitName(name))
		if err != nil {
			release()
			return Server{}, fmt.Errorf("get unit status: %w", err)
		}
		if st.State() == engine.StateStarting {
			release()
			return Server{}, ErrServerStarting
		}
	}
	s, err := d.setDesired(ctx, name, store.DesiredStateStopped)
	if err != nil {
		release()
		return Server{}, err
	}

	d.runAsync(name, OperationDown, release, func(ctx context.Context) error {
		if err := d.writeUnit(ctx, s); err != nil {
			return err
		}
		if err := d.eng.Units.Stop(ctx, engine.UnitName(name)); err != nil {
			return fmt.Errorf("stop unit: %w", err)
		}
		return nil
	})
	return d.view(ctx, s), nil
}

// Remove はサーバーを削除する。動いているサーバーは削除できない。
// purge が true なら、データディレクトリ（ワールド）と drop-in も削除する。
func (d *Daemon) Remove(ctx context.Context, name string, purge bool) error {
	release, err := d.acquire(name)
	if err != nil {
		return err
	}
	defer release()

	s, err := d.store.Get(ctx, name)
	if err != nil {
		return err
	}
	if s.DesiredState == store.DesiredStateRunning {
		return ErrServerRunning
	}
	st, err := d.eng.Units.Status(ctx, engine.UnitName(name))
	if err != nil {
		return fmt.Errorf("get unit status: %w", err)
	}
	if state := st.State(); state != engine.StateStopped && state != engine.StateFailed {
		return fmt.Errorf("%w: unit is %s", ErrServerRunning, state)
	}

	if err := d.eng.Files.Remove(name); err != nil {
		return fmt.Errorf("remove unit file: %w", err)
	}
	if err := d.eng.Units.Reload(ctx); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	if err := d.store.Delete(ctx, name); err != nil {
		return err
	}
	d.forgetLastOp(name)

	if purge {
		// ここで失敗しても登録は消えているので、利用者が手で消せるようにエラーを返す。
		if err := d.eng.Files.RemoveDropIns(name); err != nil {
			return fmt.Errorf("remove drop-ins: %w", err)
		}
		if err := os.RemoveAll(d.dataDir(name)); err != nil {
			return fmt.Errorf("remove data directory: %w", err)
		}
	}
	return nil
}

// Get はサーバーの定義と実際の状態を返す。
func (d *Daemon) Get(ctx context.Context, name string) (Server, error) {
	s, err := d.store.Get(ctx, name)
	if err != nil {
		return Server{}, err
	}
	return d.view(ctx, s), nil
}

// List はすべてのサーバーを名前順で返す。
func (d *Daemon) List(ctx context.Context) ([]Server, error) {
	all, err := d.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(all))
	for _, s := range all {
		out = append(out, d.view(ctx, s))
	}
	return out, nil
}

// Logs はサーバーのログの末尾 tail 件を古い順に返す。tail が 0 なら DefaultLogTail 件。
func (d *Daemon) Logs(ctx context.Context, name string, tail int) ([]engine.LogEntry, error) {
	if tail == 0 {
		tail = DefaultLogTail
	}
	if tail < 1 || tail > MaxLogTail {
		return nil, fmt.Errorf("%w: tail must be between 1 and %d", ErrInvalidArgument, MaxLogTail)
	}
	if _, err := d.store.Get(ctx, name); err != nil {
		return nil, err
	}
	return d.eng.Logs.Tail(ctx, engine.UnitName(name), tail)
}

// setDesired は求める状態を書き換えて、書き換えた後の定義を返す。
func (d *Daemon) setDesired(ctx context.Context, name string, state store.DesiredState) (store.Server, error) {
	var out store.Server
	err := d.store.Update(ctx, name, func(s *store.Server) error {
		s.DesiredState = state
		out = s.Clone()
		return nil
	})
	return out, err
}

// writeUnit は .container ファイルを生成し、内容が変わっていれば systemd に読み直させる。
func (d *Daemon) writeUnit(ctx context.Context, s store.Server) error {
	changed, err := d.eng.Files.Write(d.engineServer(s))
	if err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	if changed {
		if err := d.eng.Units.Reload(ctx); err != nil {
			return fmt.Errorf("reload systemd: %w", err)
		}
	}
	return nil
}

// view は実際の状態を問い合わせて Server を組み立てる。
// 問い合わせに失敗しても一覧全体を失敗させないよう、unknown / none にしてログに残す。
func (d *Daemon) view(ctx context.Context, s store.Server) Server {
	v := Server{Spec: s.Clone(), State: engine.StateUnknown, Health: engine.HealthNone}
	if st, err := d.eng.Units.Status(ctx, engine.UnitName(s.Name)); err != nil {
		slog.Warn("failed to get unit status", "server", s.Name, "error", err)
	} else {
		v.State, v.Restarts = st.State(), st.NRestarts
	}
	if h, err := d.eng.Podman.ContainerHealth(ctx, engine.ContainerName(s.Name)); err != nil {
		slog.Warn("failed to get container health", "server", s.Name, "error", err)
	} else {
		v.Health = h
	}
	if op, ok := d.lastOp(s.Name); ok {
		v.LastOp = &op
	}
	return v
}

// engineServer は Store の定義を .container ファイルの入力に変換する。
func (d *Daemon) engineServer(s store.Server) engine.Server {
	return engine.Server{
		Name:      s.Name,
		Port:      s.Port,
		Version:   s.Version,
		Type:      s.Type,
		Memory:    s.Memory,
		ImageTag:  s.ImageTag,
		Env:       maps.Clone(s.Env),
		DataDir:   d.dataDir(s.Name),
		AutoStart: s.DesiredState == store.DesiredStateRunning,
	}
}

func (d *Daemon) dataDir(name string) string {
	return filepath.Join(d.dataRoot, name)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
