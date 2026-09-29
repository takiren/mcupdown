package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sddbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

// ErrJobFailed は systemd のジョブが done 以外で終わったときのエラー。
var ErrJobFailed = errors.New("systemd job did not complete")

// SystemdUser は Units の実装。systemd --user のマネージャーと D-Bus（ユーザーバス）でやり取りする。
// 接続先は DBUS_SESSION_BUS_ADDRESS、なければ XDG_RUNTIME_DIR/bus。
// 接続が切れていたら（ユーザーマネージャーの再起動など）、次の呼び出しでつなぎ直す。
type SystemdUser struct {
	mu   sync.Mutex
	conn *sddbus.Conn
}

var _ Units = (*SystemdUser)(nil)

// NewSystemdUser はユーザーバスに接続する。
func NewSystemdUser(ctx context.Context) (*SystemdUser, error) {
	s := &SystemdUser{}
	if _, err := s.connection(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close は接続を閉じる。
func (s *SystemdUser) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}

func (s *SystemdUser) connection(ctx context.Context) (*sddbus.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && s.conn.Connected() {
		return s.conn, nil
	}
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	conn, err := sddbus.NewUserConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd user manager: %w", err)
	}
	s.conn = conn
	return conn, nil
}

func (s *SystemdUser) Reload(ctx context.Context) error {
	conn, err := s.connection(ctx)
	if err != nil {
		return err
	}
	return conn.ReloadContext(ctx)
}

func (s *SystemdUser) Start(ctx context.Context, unit string) error {
	conn, err := s.connection(ctx)
	if err != nil {
		return err
	}
	// 完了は待たない（Notify=healthy なので healthy になるまで数分かかることがある）。
	_, err = conn.StartUnitContext(ctx, unit, "replace", nil)
	return err
}

func (s *SystemdUser) Stop(ctx context.Context, unit string) error {
	conn, err := s.connection(ctx)
	if err != nil {
		return err
	}
	// go-systemd はジョブの結果をチャネルに書き終えるまで次のジョブを処理しないので、
	// ctx で先に抜けても詰まらないようにバッファを持たせる。
	done := make(chan string, 1)
	if _, err := conn.StopUnitContext(ctx, unit, "replace", done); err != nil {
		if isNoSuchUnit(err) {
			return nil // 読み込まれていない unit は止まっているのと同じ
		}
		return err
	}
	select {
	case result := <-done:
		return jobResultError("stop", unit, result)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SystemdUser) Status(ctx context.Context, unit string) (UnitStatus, error) {
	conn, err := s.connection(ctx)
	if err != nil {
		return UnitStatus{}, err
	}
	props, err := conn.GetUnitPropertiesContext(ctx, unit)
	if err != nil {
		return UnitStatus{}, err
	}
	st := unitStatusFromProperties(props, nil)
	if st.LoadState == "not-found" {
		return st, nil
	}
	svc, err := conn.GetUnitTypePropertiesContext(ctx, unit, "Service")
	if err != nil {
		return UnitStatus{}, err
	}
	return unitStatusFromProperties(props, svc), nil
}

// unitStatusFromProperties は D-Bus のプロパティ（Unit と Service のインターフェース）から UnitStatus を作る。
func unitStatusFromProperties(unit, service map[string]any) UnitStatus {
	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}
	restarts, _ := service["NRestarts"].(uint32)
	return UnitStatus{
		LoadState:   str(unit, "LoadState"),
		ActiveState: str(unit, "ActiveState"),
		SubState:    str(unit, "SubState"),
		Result:      str(service, "Result"),
		NRestarts:   restarts,
	}
}

// jobResultError はジョブの結果が done 以外ならエラーを返す。
// 結果は done, canceled, timeout, failed, dependency, skipped のどれか。
// skipped（すでに止まっているなど、状態に合わずジョブが不要だった）も成功として扱う。
func jobResultError(op, unit, result string) error {
	switch result {
	case "done", "skipped":
		return nil
	default:
		return fmt.Errorf("%w: %s %s: %s", ErrJobFailed, op, unit, result)
	}
}

func isNoSuchUnit(err error) bool {
	var e dbus.Error
	return errors.As(err, &e) && e.Name == "org.freedesktop.systemd1.NoSuchUnit"
}
