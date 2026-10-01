package engine

import (
	"context"
	"time"
)

// Logs はサーバーのログ（journal）を読む。
type Logs interface {
	// Tail は unit のログの末尾 n 件を古い順に返す。
	// マイクラ（コンテナ）の出力と、systemd の起動・停止・再起動の記録の両方を含む。
	Tail(ctx context.Context, unit string, n int) ([]LogEntry, error)
}

// LogEntry はログの1件。
type LogEntry struct {
	Time    time.Time
	Source  LogSource
	Message string
}

// LogSource はログの出どころ。
type LogSource string

const (
	// LogSourceContainer はマイクラ（コンテナ）の出力。
	LogSourceContainer LogSource = "container"
	// LogSourceSystemd は systemd の記録（起動、停止、終了理由、再起動）。
	LogSourceSystemd LogSource = "systemd"
)
