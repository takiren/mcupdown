package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up <name>",
	Short: "サーバーを起動する",
	Long:  `サーバーの求める状態を running にして起動する。起動は非同期で、進み具合は status で確認する。`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return errors.New("not implemented")
	},
}

func init() {
	rootCmd.AddCommand(upCmd)
}
