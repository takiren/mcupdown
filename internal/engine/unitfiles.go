package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// UnitFiles は quadlet の .container ファイルを管理する。
// ファイルを書き換えた後は、Units.Reload で systemd に読み込ませる必要がある。
type UnitFiles interface {
	// Write は Server から .container ファイルを生成して書き込む。
	// 内容が変わらなければ書き込まず、changed に false を返す。
	Write(s Server) (changed bool, err error)
	// Remove はサーバーの .container ファイルを削除する。ファイルがなくてもエラーにしない。
	// drop-in のディレクトリは消さない。
	Remove(name string) error
	// RemoveDropIns はサーバーの drop-in のディレクトリを削除する（rm --purge 用）。
	RemoveDropIns(name string) error
	// List は mcctld が生成した .container ファイルのあるサーバー名を、名前順に返す。
	// 手で置かれた mcctl-*.container（先頭に生成物の印がないもの）は含めない。
	List() ([]string, error)
}

// QuadletDir は UnitFiles のディレクトリ上の実装。
// 通常は mcctl ユーザーの ~/.config/containers/systemd を指す。
type QuadletDir struct {
	Dir string
}

var _ UnitFiles = (*QuadletDir)(nil)

func (q *QuadletDir) path(name string) string {
	return filepath.Join(q.Dir, ContainerFileName(name))
}

// Write は一時ファイルに書いてから rename するので、途中の状態のファイルを systemd が読むことはない。
func (q *QuadletDir) Write(s Server) (bool, error) {
	content, err := RenderContainerFile(s)
	if err != nil {
		return false, err
	}
	dst := q.path(s.Name)
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, content) {
		return false, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(q.Dir, 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(dst, content, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func (q *QuadletDir) Remove(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: name %q", ErrInvalidServer, name)
	}
	if err := os.Remove(q.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(q.Dir)
}

func (q *QuadletDir) RemoveDropIns(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: name %q", ErrInvalidServer, name)
	}
	return os.RemoveAll(filepath.Join(q.Dir, DropInDirName(name)))
}

func (q *QuadletDir) List() ([]string, error) {
	entries, err := os.ReadDir(q.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		file := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(file, "mcctl-") || !strings.HasSuffix(file, ".container") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(file, "mcctl-"), ".container")
		if !ValidName(name) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(q.Dir, file))
		if err != nil {
			return nil, err
		}
		if IsGenerated(content) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}

// writeFileAtomic は同じディレクトリの一時ファイルに書いて fsync し、rename で置き換える。
func writeFileAtomic(dst string, data []byte, perm fs.FileMode) (err error) {
	dir := filepath.Dir(dst)
	f, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir はディレクトリのエントリの変更（rename や削除）をディスクに書き出す。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	// ディレクトリの fsync に対応しないファイルシステムもあるので、失敗は無視する。
	_ = d.Sync()
	return nil
}
