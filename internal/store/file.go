package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// fileVersion は保存ファイルの形式のバージョン。形式を変えたら上げる。
const fileVersion = 1

// fileFormat は保存ファイルの中身。
type fileFormat struct {
	Version int      `json:"version"`
	Servers []Server `json:"servers"`
}

// FileStore は JSON ファイルに保存する Store。
//
// 書き込むのは mcctld だけという前提で、プロセス内のミューテックスで排他する。
// 操作のたびにファイルを読み直す（サーバーは少数なのでコストは問題にならない）。
// 書き込みは同じディレクトリの一時ファイルに書いて fsync してから rename するので、
// 途中で落ちても古い内容か新しい内容のどちらかが残る。
type FileStore struct {
	path string
	mu   sync.Mutex
}

var _ Store = (*FileStore)(nil)

// NewFileStore は path に保存する FileStore を返す。ファイルがなければ空として扱い、
// 最初の書き込みで作る。親ディレクトリはあらかじめ作っておくこと。
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (f *FileStore) Get(ctx context.Context, name string) (Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	servers, err := f.load(ctx)
	if err != nil {
		return Server{}, err
	}
	i := indexOf(servers, name)
	if i < 0 {
		return Server{}, fmt.Errorf("%q: %w", name, ErrNotFound)
	}
	return servers[i].Clone(), nil
}

func (f *FileStore) List(ctx context.Context) ([]Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	servers, err := f.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Server, len(servers))
	for i, s := range servers {
		out[i] = s.Clone()
	}
	return out, nil
}

func (f *FileStore) Create(ctx context.Context, s Server) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	servers, err := f.load(ctx)
	if err != nil {
		return err
	}
	if indexOf(servers, s.Name) >= 0 {
		return fmt.Errorf("%q: %w", s.Name, ErrAlreadyExists)
	}
	return f.save(append(servers, s.Clone()))
}

func (f *FileStore) Update(ctx context.Context, name string, fn func(*Server) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	servers, err := f.load(ctx)
	if err != nil {
		return err
	}
	i := indexOf(servers, name)
	if i < 0 {
		return fmt.Errorf("%q: %w", name, ErrNotFound)
	}
	s := servers[i].Clone()
	if err := fn(&s); err != nil {
		return err
	}
	if s.Name != name {
		return fmt.Errorf("%q -> %q: %w", name, s.Name, ErrRename)
	}
	servers[i] = s.Clone()
	return f.save(servers)
}

func (f *FileStore) Delete(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	servers, err := f.load(ctx)
	if err != nil {
		return err
	}
	i := indexOf(servers, name)
	if i < 0 {
		return fmt.Errorf("%q: %w", name, ErrNotFound)
	}
	return f.save(slices.Delete(servers, i, i+1))
}

// load はファイルを読む。呼び出し側で f.mu を取っていること。
func (f *FileStore) load(ctx context.Context) ([]Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var ff fileFormat
	if err := json.Unmarshal(b, &ff); err != nil {
		return nil, fmt.Errorf("parse state file %s: %w", f.path, err)
	}
	if ff.Version != fileVersion {
		return nil, fmt.Errorf("%s: version %d (want %d): %w", f.path, ff.Version, fileVersion, ErrUnsupportedVersion)
	}
	return ff.Servers, nil
}

// save はファイルを原子的に置き換える。呼び出し側で f.mu を取っていること。
func (f *FileStore) save(servers []Server) error {
	slices.SortFunc(servers, func(a, b Server) int { return strings.Compare(a.Name, b.Name) })
	if servers == nil {
		servers = []Server{}
	}
	b, err := json.MarshalIndent(fileFormat{Version: fileVersion, Servers: servers}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(f.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	// rename に成功した後は tmp.Name() は存在しないので、Remove の失敗は無視してよい。
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	// rename 自体を永続化するため、ディレクトリも fsync する。
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open state dir: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync state dir: %w", err)
	}
	return nil
}

func indexOf(servers []Server, name string) int {
	return slices.IndexFunc(servers, func(s Server) bool { return s.Name == name })
}
