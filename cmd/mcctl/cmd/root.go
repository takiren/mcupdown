// Package cmd implements the mcctl subcommands.
package cmd

import (
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/takiren/mcupdown/internal/api"
)

const (
	defaultSocket = "/run/mcctl/mcctld.sock"
	socketEnv     = "MCCTL_SOCKET"
)

// app はコマンドの実行に必要な状態をまとめたもの。テストでは出力先や時刻の表示に使う
// タイムゾーンを差し替える。グローバル変数に持たないので、テスト間で状態が漏れない。
type app struct {
	stdout io.Writer
	stderr io.Writer
	loc    *time.Location
	getenv func(string) string

	socket string
}

// Run は args でコマンドを実行し、終了コードを返す。
func Run(args []string, stdout, stderr io.Writer) int {
	a := &app{stdout: stdout, stderr: stderr, loc: time.Local, getenv: os.Getenv}
	return a.run(args)
}

func (a *app) run(args []string) int {
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	if err := root.Execute(); err != nil {
		_, _ = io.WriteString(a.stderr, "エラー: "+err.Error()+"\n")
		return 1
	}
	return 0
}

func (a *app) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "mcctl",
		Short: "マイクラサーバーを管理する",
		Long: `mcctl は mcctld の API を呼び出して、コンテナで動くマイクラサーバーを管理する。
ロジックはすべて mcctld 側にあり、mcctl は API を叩くだけのクライアント。`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	socket := defaultSocket
	if v := a.getenv(socketEnv); v != "" {
		socket = v
	}
	root.PersistentFlags().StringVar(&a.socket, "socket", socket, "mcctld のソケットのパス（環境変数 "+socketEnv+" でも指定できる）")

	root.AddCommand(
		a.newCreateCommand(),
		a.newUpCommand(),
		a.newDownCommand(),
		a.newRmCommand(),
		a.newListCommand(),
		a.newStatusCommand(),
		a.newLogsCommand(),
	)
	return root
}

func (a *app) client() (*api.ClientWithResponses, error) {
	return api.NewUnixSocketClient(a.socket)
}
