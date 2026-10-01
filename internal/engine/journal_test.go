package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// 実機（podman 5.8.2 / systemd 257）の journalctl -o json の出力を元にしたもの。
// MESSAGE がバイトの配列になる行（制御文字を含む）と、null の行を含む。
const journalSample = `{"__REALTIME_TIMESTAMP":"1790516145000000","SYSLOG_IDENTIFIER":"systemd","MESSAGE":"Starting mcctl-j1.service..."}
{"__REALTIME_TIMESTAMP":"1790516145918233","SYSLOG_IDENTIFIER":"mcctl-j1","CONTAINER_NAME":"mcctl-j1","MESSAGE":"[init] Running as uid=0 gid=0\n"}
{"__REALTIME_TIMESTAMP":"1790516152035000","SYSLOG_IDENTIFIER":"mcctl-j1","CONTAINER_NAME":"mcctl-j1","MESSAGE":[27,91,51,50,109,73,78,70,79,9,100,111,110,101,10]}

{"__REALTIME_TIMESTAMP":"1790516153000000","SYSLOG_IDENTIFIER":"systemd","MESSAGE":null}
`

func TestParseJournal(t *testing.T) {
	got, err := parseJournal(strings.NewReader(journalSample))
	if err != nil {
		t.Fatal(err)
	}
	want := []LogEntry{
		{Time: time.UnixMicro(1790516145000000).UTC(), Source: LogSourceSystemd, Message: "Starting mcctl-j1.service..."},
		{Time: time.UnixMicro(1790516145918233).UTC(), Source: LogSourceContainer, Message: "[init] Running as uid=0 gid=0"},
		{Time: time.UnixMicro(1790516152035000).UTC(), Source: LogSourceContainer, Message: "\x1b[32mINFO\tdone"},
		{Time: time.UnixMicro(1790516153000000).UTC(), Source: LogSourceSystemd, Message: ""},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}

	for name, bad := range map[string]string{
		"not json":      "nope\n",
		"bad timestamp": `{"__REALTIME_TIMESTAMP":"x","MESSAGE":"a"}`,
		"bad bytes":     `{"__REALTIME_TIMESTAMP":"1","MESSAGE":[300]}`,
		"bad message":   `{"__REALTIME_TIMESTAMP":"1","MESSAGE":{"a":1}}`,
	} {
		if _, err := parseJournal(strings.NewReader(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestJournalMessageInvalidUTF8(t *testing.T) {
	got, err := journalMessage([]byte(`[97,255,98]`))
	if err != nil || got != "a�b" {
		t.Errorf("= %q, %v", got, err)
	}
}

func TestJournalTail(t *testing.T) {
	var gotArgs []string
	j := &Journal{run: func(_ context.Context, args ...string) ([]byte, []byte, error) {
		gotArgs = args
		return []byte(journalSample), nil, nil
	}}
	entries, err := j.Tail(t.Context(), "mcctl-j1.service", 200)
	if err != nil || len(entries) != 4 {
		t.Fatalf("Tail = %v, %v", entries, err)
	}
	wantArgs := []string{
		"--user", "--no-pager", "--output=json", "--all",
		"--output-fields=MESSAGE,CONTAINER_NAME,SYSLOG_IDENTIFIER", "--lines=200",
		"CONTAINER_NAME=mcctl-j1", "+", "USER_UNIT=mcctl-j1.service",
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Errorf("args =\n%q\nwant\n%q", gotArgs, wantArgs)
	}

	for _, unit := range []string{"mcctl-j1", "sshd.service", "mcctl-../x.service", "mcctl-.service"} {
		if _, err := j.Tail(t.Context(), unit, 10); err == nil {
			t.Errorf("Tail(%q) accepted", unit)
		}
	}
	if _, err := j.Tail(t.Context(), "mcctl-j1.service", 0); err == nil {
		t.Error("Tail(n=0) accepted")
	}
}

func TestJournalTailErrors(t *testing.T) {
	denied := &Journal{run: func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, []byte("No journal files were opened due to insufficient permissions.\n"), nil
	}}
	if _, err := denied.Tail(t.Context(), "mcctl-j1.service", 10); !errors.Is(err, ErrJournalPermission) {
		t.Errorf("permission = %v", err)
	}

	failed := &Journal{run: func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, []byte("Failed to add match\n"), errors.New("exit status 1")
	}}
	if _, err := failed.Tail(t.Context(), "mcctl-j1.service", 10); err == nil || !strings.Contains(err.Error(), "Failed to add match") {
		t.Errorf("failure = %v", err)
	}

	// ほかのユーザーのログが見えない、というヒントだけなら、読めた分を返す。
	hint := &Journal{run: func(context.Context, ...string) ([]byte, []byte, error) {
		return []byte(journalSample), []byte("Hint: You are currently not seeing messages from other users and the system.\n"), nil
	}}
	if entries, err := hint.Tail(t.Context(), "mcctl-j1.service", 10); err != nil || len(entries) != 4 {
		t.Errorf("hint = %v, %v", entries, err)
	}

	empty := &Journal{run: func(context.Context, ...string) ([]byte, []byte, error) { return nil, nil, nil }}
	if entries, err := empty.Tail(t.Context(), "mcctl-j1.service", 10); err != nil || len(entries) != 0 {
		t.Errorf("empty = %v, %v", entries, err)
	}
}
