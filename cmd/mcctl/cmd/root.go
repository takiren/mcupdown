// Package cmd implements the mcctl subcommands.
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "mcctl",
	Short: "マイクラサーバーを管理する",
	Long: `mcctl は mcctld の API を呼び出して、コンテナで動くマイクラサーバーを管理する。
ロジックはすべて mcctld 側にあり、mcctl は API を叩くだけのクライアント。`,
	SilenceUsage: true,
}

// Execute runs the root command and exits non-zero on error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
