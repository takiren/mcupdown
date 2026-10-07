// Command mcctld is the daemon that manages Minecraft server containers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/takiren/mcupdown/internal/apiserver"
	"github.com/takiren/mcupdown/internal/daemon"
	"github.com/takiren/mcupdown/internal/engine"
	"github.com/takiren/mcupdown/internal/store"
)

// shutdownTimeout は終了時に、実行中の操作（Stop の完了待ちなど）を待つ時間。
// 生成する unit の TimeoutStopSec（90 秒）に合わせる。
const shutdownTimeout = 90 * time.Second

type config struct {
	socket       string
	statePath    string
	dataRoot     string
	quadletDir   string
	podmanSocket string
	group        string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}

func run(ctx context.Context, args []string, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := serve(ctx, cfg); err != nil {
		slog.Error("mcctld failed", "error", err)
		return 1
	}
	return 0
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	home, _ := os.UserHomeDir()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}

	var cfg config
	fset := flag.NewFlagSet("mcctld", flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.StringVar(&cfg.socket, "socket", "/run/mcctl/mcctld.sock", "API を待ち受ける Unix ソケットのパス（ディレクトリは tmpfiles.d で作られている前提）")
	fset.StringVar(&cfg.statePath, "state", "/var/lib/mcctl/state.json", "Store のファイルのパス")
	fset.StringVar(&cfg.dataRoot, "data-dir", "/var/lib/mcctl/servers", "サーバーのデータディレクトリを置く場所")
	fset.StringVar(&cfg.quadletDir, "quadlet-dir", filepath.Join(home, ".config/containers/systemd"), "quadlet の .container ファイルを置くディレクトリ")
	fset.StringVar(&cfg.podmanSocket, "podman-socket", filepath.Join(runtimeDir, "podman/podman.sock"), "podman の API ソケットのパス")
	fset.StringVar(&cfg.group, "group", "mcctl", "変更系の操作を許すグループ")
	if err := fset.Parse(args); err != nil {
		return config{}, err
	}
	if fset.NArg() > 0 {
		err := fmt.Errorf("unexpected arguments: %v", fset.Args())
		_, _ = fmt.Fprintln(stderr, err)
		return config{}, err
	}
	return cfg, nil
}

func serve(ctx context.Context, cfg config) error {
	for _, dir := range []struct {
		path string
		mode fs.FileMode
	}{
		{filepath.Dir(cfg.statePath), 0o750},
		{cfg.dataRoot, 0o750},
		{cfg.quadletDir, 0o755},
	} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}
	// ソケットのディレクトリは root が tmpfiles.d で作る（/run は mcctl ユーザーには書けない）。
	if _, err := os.Stat(filepath.Dir(cfg.socket)); err != nil {
		return fmt.Errorf("socket directory (created by tmpfiles.d): %w", err)
	}

	units, err := engine.NewSystemdUser(ctx)
	if err != nil {
		return err
	}
	defer units.Close()

	d, err := daemon.New(store.NewFileStore(cfg.statePath), daemon.Engine{
		Files:  &engine.QuadletDir{Dir: cfg.quadletDir},
		Units:  units,
		Podman: engine.NewPodmanAPI(cfg.podmanSocket),
		Logs:   engine.NewJournal(),
	}, daemon.Config{DataRoot: cfg.dataRoot})
	if err != nil {
		return err
	}

	if err := d.Sync(ctx); err != nil {
		var partial *daemon.PartialSyncError
		if !errors.As(err, &partial) {
			return fmt.Errorf("sync: %w", err)
		}
		// 一部のサーバーの失敗では止まらない。status で確認できる。
		slog.Warn("some servers failed to sync", "error", err)
	}

	ln, err := listen(cfg.socket)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(cfg.socket) }()

	srv := &http.Server{
		Handler:           apiserver.NewHandler(d, &apiserver.Authorizer{SelfUID: uint32(os.Getuid()), Group: cfg.group}),
		ConnContext:       apiserver.ConnContext,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	slog.Info("mcctld started", "socket", cfg.socket, "group", cfg.group)

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	slog.Info("mcctld stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http server shutdown", "error", err)
	}
	if err := d.Shutdown(shutdownCtx); err != nil {
		slog.Warn("some operations did not finish before shutdown", "error", err)
	}
	slog.Info("mcctld stopped")
	return nil
}

// listen は古いソケットファイルを消してから待ち受け、誰でも接続できるようにする（認可は接続元の UID で行う）。
func listen(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// 参照は誰でもできる仕様。変更系は接続元の UID で認可する。
	if err := os.Chmod(path, 0o666); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return ln, nil
}
