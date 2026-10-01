package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestUnitStatusFromProperties(t *testing.T) {
	unit := map[string]any{"LoadState": "loaded", "ActiveState": "activating", "SubState": "auto-restart", "Id": "x"}
	svc := map[string]any{"NRestarts": uint32(3), "Result": "exit-code"}
	got := unitStatusFromProperties(unit, svc)
	want := UnitStatus{LoadState: "loaded", ActiveState: "activating", SubState: "auto-restart", Result: "exit-code", NRestarts: 3}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.State() != StateRestarting {
		t.Errorf("State = %q", got.State())
	}

	// Service のプロパティがない（unit ファイルがない）場合や、型が想定と違う場合はゼロ値になる。
	got = unitStatusFromProperties(map[string]any{"LoadState": "not-found", "ActiveState": 1}, nil)
	if got != (UnitStatus{LoadState: "not-found"}) {
		t.Errorf("got %+v", got)
	}
}

func TestJobResultError(t *testing.T) {
	for _, ok := range []string{"done", "skipped"} {
		if err := jobResultError("stop", "u", ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"canceled", "timeout", "failed", "dependency", ""} {
		if err := jobResultError("stop", "u", bad); !errors.Is(err, ErrJobFailed) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestIsNoSuchUnit(t *testing.T) {
	noSuch := dbus.Error{Name: "org.freedesktop.systemd1.NoSuchUnit"}
	if !isNoSuchUnit(noSuch) || !isNoSuchUnit(fmt.Errorf("wrap: %w", noSuch)) {
		t.Error("NoSuchUnit not detected")
	}
	if isNoSuchUnit(dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}) || isNoSuchUnit(errors.New("x")) {
		t.Error("false positive")
	}
}
