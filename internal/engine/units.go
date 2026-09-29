package engine

import (
	"context"
	"strings"
)

// Units は systemd --user の unit を操作する。
type Units interface {
	// Reload は systemd に unit ファイルを読み直させる（daemon-reload）。
	// quadlet の .container ファイルを書き換えた後に呼ぶ。
	Reload(ctx context.Context) error
	// Start は unit の起動ジョブを登録して、完了を待たずに戻る。
	// Notify=healthy なので、ジョブの完了は healthy になるまで（最大 TimeoutStartSec）かかる。
	Start(ctx context.Context, unit string) error
	// Stop は unit の停止ジョブを登録し、完了まで待つ。ジョブが失敗したらエラーを返す。
	Stop(ctx context.Context, unit string) error
	// Status は unit の状態を返す。unit が存在しなくてもエラーにせず、LoadState が not-found になる。
	Status(ctx context.Context, unit string) (UnitStatus, error)
}

// UnitStatus は systemd の unit のプロパティのうち、状態の判断に使うもの。
type UnitStatus struct {
	LoadState   string // loaded, not-found など
	ActiveState string // active, activating, deactivating, inactive, failed など
	SubState    string // running, start, auto-restart, dead など
	Result      string // success, exit-code, timeout など
	NRestarts   uint32 // systemd が自動で再起動した回数
}

// State はサーバーの実際の状態。unit の状態を読み替えたもの。
type State string

const (
	StateStopped    State = "stopped"
	StateStarting   State = "starting"
	StateRunning    State = "running"
	StateStopping   State = "stopping"
	StateRestarting State = "restarting"
	StateFailed     State = "failed"
	StateUnknown    State = "unknown"
)

// State は unit の状態をサーバーの状態に読み替える。
//
//	inactive                  → stopped
//	activating / auto-restart → restarting（再起動の待ち時間）
//	activating / その他        → starting（Notify=healthy なので healthy になるまで）
//	active                    → running
//	deactivating              → stopping
//	failed                    → failed
//
// unit ファイルがない（LoadState が not-found）場合は stopped とする。
func (s UnitStatus) State() State {
	if s.LoadState == "not-found" {
		return StateStopped
	}
	switch s.ActiveState {
	case "inactive":
		return StateStopped
	case "activating":
		// systemd 254 以降は、再起動のジョブが入ると auto-restart-queued になる。
		if strings.HasPrefix(s.SubState, "auto-restart") {
			return StateRestarting
		}
		return StateStarting
	case "active", "reloading", "refreshing":
		return StateRunning
	case "deactivating":
		return StateStopping
	case "failed":
		return StateFailed
	default:
		return StateUnknown
	}
}
