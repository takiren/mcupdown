package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) (*FileStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	return NewFileStore(path), path
}

func testServer(name string, port int) Server {
	return Server{
		Name:         name,
		Port:         port,
		EULAAccepted: true,
		Version:      "1.21.4",
		Type:         "PAPER",
		Memory:       "2G",
		ImageTag:     "latest",
		Env:          map[string]string{"DIFFICULTY": "hard"},
		DesiredState: DesiredStateStopped,
	}
}

func TestEmptyWhenFileMissing(t *testing.T) {
	s, path := newTestStore(t)
	ctx := t.Context()

	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("List = %v, want empty", got)
	}
	if _, err := s.Get(ctx, "survival"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading must not create the file: %v", err)
	}
}

func TestCRUD(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()

	want := testServer("survival", 25565)
	if err := s.Create(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testServer("creative", 25566)); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, "survival")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "creative" || list[1].Name != "survival" {
		t.Fatalf("List must be sorted by name: %+v", list)
	}

	if err := s.Update(ctx, "survival", func(sv *Server) error {
		sv.DesiredState = DesiredStateRunning
		sv.Env["MOTD"] = "hello"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, "survival")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredStateRunning || got.Env["MOTD"] != "hello" {
		t.Fatalf("Update not applied: %+v", got)
	}

	if err := s.Delete(ctx, "survival"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "survival"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
	list, err = s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "creative" {
		t.Fatalf("List after Delete = %+v", list)
	}
}

func TestErrors(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, testServer("survival", 25565)); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		op   func() error
		want error
	}{
		{"create duplicate", func() error { return s.Create(ctx, testServer("survival", 25570)) }, ErrAlreadyExists},
		{"update missing", func() error { return s.Update(ctx, "nope", func(*Server) error { return nil }) }, ErrNotFound},
		{"delete missing", func() error { return s.Delete(ctx, "nope") }, ErrNotFound},
		{"rename", func() error {
			return s.Update(ctx, "survival", func(sv *Server) error { sv.Name = "other"; return nil })
		}, ErrRename},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.op(); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}

	// 失敗した操作で内容が変わっていないこと。
	got, err := s.Get(ctx, "survival")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, testServer("survival", 25565)) {
		t.Fatalf("store changed by failed operations: %+v", got)
	}
}

func TestUpdateCallbackErrorDoesNotSave(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, testServer("survival", 25565)); err != nil {
		t.Fatal(err)
	}

	errBoom := errors.New("boom")
	err := s.Update(ctx, "survival", func(sv *Server) error {
		sv.Port = 1
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	got, err := s.Get(ctx, "survival")
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 25565 {
		t.Fatalf("Port = %d, want unchanged 25565", got.Port)
	}
}

func TestReturnedValuesAreCopies(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()

	in := testServer("survival", 25565)
	if err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Env["DIFFICULTY"] = "peaceful" // Create に渡した値を後から書き換えても影響しない

	got, err := s.Get(ctx, "survival")
	if err != nil {
		t.Fatal(err)
	}
	got.Env["DIFFICULTY"] = "easy" // 受け取った値を書き換えても影響しない

	again, err := s.Get(ctx, "survival")
	if err != nil {
		t.Fatal(err)
	}
	if again.Env["DIFFICULTY"] != "hard" {
		t.Fatalf("Env = %v, want DIFFICULTY=hard", again.Env)
	}
}

func TestPersistsAcrossInstances(t *testing.T) {
	s, path := newTestStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, testServer("survival", 25565)); err != nil {
		t.Fatal(err)
	}

	got, err := NewFileStore(path).Get(ctx, "survival")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, testServer("survival", 25565)) {
		t.Fatalf("Get from new instance = %+v", got)
	}
}

func TestNoTempFilesLeft(t *testing.T) {
	s, path := newTestStore(t)
	ctx := t.Context()
	for i := range 3 {
		if err := s.Create(ctx, testServer(fmt.Sprintf("s%d", i), 25565+i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("dir = %v, want only state.json", names)
	}
}

func TestFileFormat(t *testing.T) {
	t.Run("empty after deleting all", func(t *testing.T) {
		s, path := newTestStore(t)
		ctx := t.Context()
		if err := s.Create(ctx, testServer("survival", 25565)); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, "survival"); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "{\n  \"version\": 1,\n  \"servers\": []\n}\n"; string(b) != want {
			t.Fatalf("file = %q, want %q", b, want)
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		s, path := newTestStore(t)
		if err := os.WriteFile(path, []byte(`{"version": 2, "servers": []}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(t.Context()); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
		}
	})

	t.Run("broken json", func(t *testing.T) {
		s, path := newTestStore(t)
		if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(t.Context()); err == nil {
			t.Fatal("want parse error")
		}
	})
}

func TestCanceledContext(t *testing.T) {
	s, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Create(ctx, testServer("survival", 25565)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s, path := newTestStore(t)
	ctx := t.Context()
	const n = 20

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			name := fmt.Sprintf("s%02d", i)
			if err := s.Create(ctx, testServer(name, 25565+i)); err != nil {
				t.Error(err)
				return
			}
			if err := s.Update(ctx, name, func(sv *Server) error {
				sv.DesiredState = DesiredStateRunning
				return nil
			}); err != nil {
				t.Error(err)
			}
			if _, err := s.List(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	list, err := NewFileStore(path).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != n {
		t.Fatalf("len = %d, want %d", len(list), n)
	}
	for _, sv := range list {
		if sv.DesiredState != DesiredStateRunning {
			t.Errorf("%s: DesiredState = %s, want running", sv.Name, sv.DesiredState)
		}
	}
}
