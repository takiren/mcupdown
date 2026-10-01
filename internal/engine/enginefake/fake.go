// Package enginefake は engine のインターフェースをすべて満たす、テスト用のフェイク。
//
// 状態はメモリに持ち、goroutine から同時に呼んでも安全。
// 呼び出しは Calls で順番どおりに取り出せる（例: "Write e2e", "Reload", "Start mcctl-e2e.service"）。
package enginefake

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/takiren/mcupdown/internal/engine"
)

// Fake は engine.UnitFiles、engine.Units、engine.Podman、engine.Logs を実装する。
type Fake struct {
	mu          sync.Mutex
	files       map[string][]byte // サーバー名 → 生成した .container の内容
	servers     map[string]engine.Server
	dropIns     map[string]bool
	reloads     int
	units       map[string]engine.UnitStatus
	startStatus engine.UnitStatus
	images      map[string]bool
	pulled      []string
	health      map[string]engine.Health
	rootless    bool
	logs        map[string][]engine.LogEntry
	errs        map[string]error
	calls       []string
}

var (
	_ engine.UnitFiles = (*Fake)(nil)
	_ engine.Units     = (*Fake)(nil)
	_ engine.Podman    = (*Fake)(nil)
	_ engine.Logs      = (*Fake)(nil)
)

// New は空のフェイクを返す。rootless で、Start した unit は active/running になる。
func New() *Fake {
	return &Fake{
		files:       map[string][]byte{},
		servers:     map[string]engine.Server{},
		dropIns:     map[string]bool{},
		units:       map[string]engine.UnitStatus{},
		startStatus: engine.UnitStatus{LoadState: "loaded", ActiveState: "active", SubState: "running", Result: "success"},
		images:      map[string]bool{},
		health:      map[string]engine.Health{},
		rootless:    true,
		logs:        map[string][]engine.LogEntry{},
		errs:        map[string]error{},
	}
}

// SetError は method（"Write"、"Start" など、メソッド名そのもの）が返すエラーを設定する。nil で解除する。
func (f *Fake) SetError(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, method)
		return
	}
	f.errs[method] = err
}

// SetUnitStatus は unit の状態を設定する。
func (f *Fake) SetUnitStatus(unit string, s engine.UnitStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.units[unit] = s
}

// SetStartStatus は Start した後の unit の状態を設定する（既定は active/running）。
func (f *Fake) SetStartStatus(s engine.UnitStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startStatus = s
}

// SetImage はイメージが手元にあるかを設定する。
func (f *Fake) SetImage(ref string, exists bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = exists
}

// SetHealth はコンテナの healthcheck の状態を設定する。
func (f *Fake) SetHealth(container string, h engine.Health) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health[container] = h
}

// SetRootless は Rootless の戻り値を設定する。
func (f *Fake) SetRootless(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rootless = v
}

// SetLogs は unit のログを設定する。
func (f *Fake) SetLogs(unit string, entries []engine.LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs[unit] = slices.Clone(entries)
}

// SetDropIns はサーバーに drop-in があるかを設定する。
func (f *Fake) SetDropIns(name string, exists bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropIns[name] = exists
}

// Calls はこれまでの呼び出しを順番に返す。
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// File は最後に Write された Server と、生成した内容を返す。
func (f *Fake) File(name string) (engine.Server, []byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.servers[name]
	return s, slices.Clone(f.files[name]), ok
}

// HasDropIns はサーバーの drop-in が残っているかを返す。
func (f *Fake) HasDropIns(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropIns[name]
}

// Reloads は Reload が呼ばれた回数を返す。
func (f *Fake) Reloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads
}

// Pulled は PullImage されたイメージを順番に返す。
func (f *Fake) Pulled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pulled)
}

// record は呼び出しを記録し、設定されたエラーを返す。f.mu を持った状態で呼ぶ。
func (f *Fake) record(method, arg string) error {
	if arg == "" {
		f.calls = append(f.calls, method)
	} else {
		f.calls = append(f.calls, method+" "+arg)
	}
	return f.errs[method]
}

// --- engine.UnitFiles ---

func (f *Fake) Write(s engine.Server) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Write", s.Name); err != nil {
		return false, err
	}
	content, err := engine.RenderContainerFile(s)
	if err != nil {
		return false, err
	}
	if bytes.Equal(f.files[s.Name], content) {
		return false, nil
	}
	f.files[s.Name] = content
	f.servers[s.Name] = s
	return true, nil
}

func (f *Fake) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Remove", name); err != nil {
		return err
	}
	delete(f.files, name)
	delete(f.servers, name)
	return nil
}

func (f *Fake) RemoveDropIns(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("RemoveDropIns", name); err != nil {
		return err
	}
	delete(f.dropIns, name)
	return nil
}

func (f *Fake) List() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("List", ""); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(f.files))
	for name := range f.files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// --- engine.Units ---

func (f *Fake) Reload(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Reload", ""); err != nil {
		return err
	}
	f.reloads++
	return nil
}

func (f *Fake) Start(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Start", unit); err != nil {
		return err
	}
	f.units[unit] = f.startStatus
	return nil
}

func (f *Fake) Stop(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Stop", unit); err != nil {
		return err
	}
	st := f.units[unit]
	st.ActiveState, st.SubState = "inactive", "dead"
	if st.LoadState == "" {
		st.LoadState = "loaded"
	}
	f.units[unit] = st
	return nil
}

func (f *Fake) Status(_ context.Context, unit string) (engine.UnitStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Status", unit); err != nil {
		return engine.UnitStatus{}, err
	}
	if st, ok := f.units[unit]; ok {
		return st, nil
	}
	return engine.UnitStatus{LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}, nil
}

// --- engine.Podman ---

func (f *Fake) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ImageExists", ref); err != nil {
		return false, err
	}
	return f.images[ref], nil
}

func (f *Fake) PullImage(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("PullImage", ref); err != nil {
		return err
	}
	f.images[ref] = true
	f.pulled = append(f.pulled, ref)
	return nil
}

func (f *Fake) ContainerHealth(_ context.Context, container string) (engine.Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ContainerHealth", container); err != nil {
		return "", err
	}
	if h, ok := f.health[container]; ok {
		return h, nil
	}
	return engine.HealthNone, nil
}

func (f *Fake) Rootless(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Rootless", ""); err != nil {
		return false, err
	}
	return f.rootless, nil
}

// --- engine.Logs ---

func (f *Fake) Tail(_ context.Context, unit string, n int) ([]engine.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Tail", fmt.Sprintf("%s %d", unit, n)); err != nil {
		return nil, err
	}
	entries := f.logs[unit]
	if n >= 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return slices.Clone(entries), nil
}
