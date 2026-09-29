package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestQuadletDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "containers", "systemd") // 存在しないディレクトリから始める
	q := &QuadletDir{Dir: dir}

	names, err := q.List()
	if err != nil || len(names) != 0 {
		t.Fatalf("List on missing dir = %v, %v", names, err)
	}

	s := testServer()
	changed, err := q.Write(s)
	if err != nil || !changed {
		t.Fatalf("first Write = %v, %v; want true", changed, err)
	}
	path := filepath.Join(dir, "mcctl-survival.container")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %v, want 0644", info.Mode().Perm())
	}

	changed, err = q.Write(s)
	if err != nil || changed {
		t.Errorf("same Write = %v, %v; want false", changed, err)
	}

	s.AutoStart = true
	changed, err = q.Write(s)
	if err != nil || !changed {
		t.Errorf("changed Write = %v, %v; want true", changed, err)
	}

	// 一時ファイルが残っていないこと。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir entries = %v, want only the .container file", entries)
	}

	// 手で置いたファイルやほかのファイルは List に含めない。
	must(t, os.WriteFile(filepath.Join(dir, "mcctl-manual.container"), []byte("[Container]\nImage=x\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "other.container"), []byte(generatedHeader), 0o644))
	creative := testServer()
	creative.Name = "creative"
	creative.DataDir = "/var/lib/mcctl/servers/creative"
	if _, err := q.Write(creative); err != nil {
		t.Fatal(err)
	}
	names, err = q.List()
	if err != nil || !slices.Equal(names, []string{"creative", "survival"}) {
		t.Errorf("List = %v, %v", names, err)
	}

	// drop-in は Remove では消えず、RemoveDropIns で消える。
	dropIn := filepath.Join(dir, "mcctl-survival.container.d")
	must(t, os.MkdirAll(dropIn, 0o755))
	must(t, os.WriteFile(filepath.Join(dropIn, "10-ports.conf"), []byte("[Container]\n"), 0o644))

	must(t, q.Remove("survival"))
	must(t, q.Remove("survival")) // 2回目もエラーにしない
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".container still exists: %v", err)
	}
	if _, err := os.Stat(dropIn); err != nil {
		t.Errorf("drop-in removed by Remove: %v", err)
	}
	must(t, q.RemoveDropIns("survival"))
	if _, err := os.Stat(dropIn); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("drop-in still exists: %v", err)
	}

	if err := q.Remove("../x"); !errors.Is(err, ErrInvalidServer) {
		t.Errorf("Remove(invalid) = %v", err)
	}
	if err := q.RemoveDropIns(""); !errors.Is(err, ErrInvalidServer) {
		t.Errorf("RemoveDropIns(invalid) = %v", err)
	}
}

func TestQuadletDirWriteInvalid(t *testing.T) {
	q := &QuadletDir{Dir: t.TempDir()}
	s := testServer()
	s.Port = 0
	if _, err := q.Write(s); !errors.Is(err, ErrInvalidServer) {
		t.Errorf("Write(invalid) = %v", err)
	}
	entries, _ := os.ReadDir(q.Dir)
	if len(entries) != 0 {
		t.Errorf("files written for invalid server: %v", entries)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
