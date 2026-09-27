package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down <name>",
	Short: "サーバーを停止する",
	Long:  `サーバーの求める状態を stopped にして停止し、コンテナを削除する。ワールドデータは残る。停止は非同期で、進み具合は status で確認する。`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return errors.New("not implemented")
	},
}

func init() {
	rootCmd.AddCommand(downCmd)
}
