package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrJournalPermission は journal を読む権限がないときのエラー。
// mcctl はシステムユーザーなので専用の journal がなく、systemd-journal グループに入っている必要がある。
var ErrJournalPermission = errors.New("no permission to read the journal (is the user in the systemd-journal group?)")

// Journal は Logs の実装。journalctl --user -o json を実行して読む。
// sdjournal（cgo と libsystemd が必要）は使わない。
type Journal struct {
	// run は journalctl を実行して標準出力と標準エラー出力を返す。テストで差し替える。
	run func(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

var _ Logs = (*Journal)(nil)

// NewJournal は PATH の journalctl を使う Journal を返す。
func NewJournal() *Journal {
	return &Journal{run: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		var out, errOut bytes.Buffer
		cmd := exec.CommandContext(ctx, "journalctl", args...)
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		return out.Bytes(), errOut.Bytes(), err
	}}
}

// Tail は unit のログの末尾 n 件を古い順に返す。
//
// 読むのは「コンテナの出力（CONTAINER_NAME が一致）」と「systemd がその unit について記録したもの（USER_UNIT が一致）」だけ。
// -u で絞ると podman のイベントや `podman run -d` が出力するコンテナ ID も混ざり、-n の件数を食うので使わない。
// コンテナ名は unit 名から .service を外したもの（ContainerName と UnitName の命名規則）。
func (j *Journal) Tail(ctx context.Context, unit string, n int) ([]LogEntry, error) {
	container, ok := strings.CutSuffix(unit, ".service")
	if !ok || !strings.HasPrefix(container, "mcctl-") || !ValidName(strings.TrimPrefix(container, "mcctl-")) {
		return nil, fmt.Errorf("invalid unit %q", unit)
	}
	if n < 1 {
		return nil, fmt.Errorf("invalid count %d", n)
	}
	stdout, stderr, err := j.run(ctx, journalArgs(container, unit, n)...)
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w: %s", err, bytes.TrimSpace(stderr))
	}
	if len(bytes.TrimSpace(stdout)) == 0 && bytes.Contains(stderr, []byte("insufficient permissions")) {
		return nil, ErrJournalPermission
	}
	return parseJournal(bytes.NewReader(stdout))
}

func journalArgs(container, unit string, n int) []string {
	return []string{
		"--user", "--no-pager", "--output=json", "--all",
		"--output-fields=MESSAGE,CONTAINER_NAME,SYSLOG_IDENTIFIER",
		"--lines=" + strconv.Itoa(n),
		"CONTAINER_NAME=" + container, "+", "USER_UNIT=" + unit,
	}
}

// parseJournal は journalctl -o json の出力（1行に1件の JSON）を LogEntry に変換する。
func parseJournal(r io.Reader) ([]LogEntry, error) {
	var entries []LogEntry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20) // --all なので長い行もある
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec struct {
			Realtime      string          `json:"__REALTIME_TIMESTAMP"`
			Message       json.RawMessage `json:"MESSAGE"`
			ContainerName string          `json:"CONTAINER_NAME"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("decode journal entry: %w", err)
		}
		usec, err := strconv.ParseInt(rec.Realtime, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("decode journal timestamp %q: %w", rec.Realtime, err)
		}
		msg, err := journalMessage(rec.Message)
		if err != nil {
			return nil, err
		}
		source := LogSourceSystemd
		if rec.ContainerName != "" {
			source = LogSourceContainer
		}
		entries = append(entries, LogEntry{
			Time:    time.UnixMicro(usec).UTC(),
			Source:  source,
			Message: strings.TrimRight(msg, "\r\n"),
		})
	}
	return entries, sc.Err()
}

// journalMessage は MESSAGE を文字列にする。
// journald は制御文字や不正な UTF-8 を含むメッセージを、文字列ではなくバイトの配列で出力する。
// 値が大きすぎる場合など、null になることもある。
func journalMessage(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var b []byte
	var ints []int
	if err := json.Unmarshal(raw, &ints); err != nil {
		return "", fmt.Errorf("decode journal message: %w", err)
	}
	for _, v := range ints {
		if v < 0 || v > 255 {
			return "", fmt.Errorf("decode journal message: byte out of range: %d", v)
		}
		b = append(b, byte(v))
	}
	return strings.ToValidUTF8(string(b), "�"), nil
}
