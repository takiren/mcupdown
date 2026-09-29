package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/takiren/mcupdown/internal/api"
)

func (a *app) newRmCommand() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "サーバーの登録を削除する",
		Long: `サーバーの登録を削除する。ワールドなどのデータは既定では残る。
--purge を付けると、データディレクトリと drop-in も削除する。動いているサーバーは削除できない。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			params := &api.DeleteServerParams{}
			if purge {
				params.Purge = &purge
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.DeleteServerWithResponse(cmd.Context(), name, params)
			if err != nil {
				return a.connError(err)
			}
			if err := expect(res, res.Body, http.StatusNoContent, name); err != nil {
				return err
			}
			msg := fmt.Sprintf("%s を削除しました（データは残っています）\n", name)
			if purge {
				msg = fmt.Sprintf("%s をデータごと削除しました\n", name)
			}
			_, err = fmt.Fprint(a.stdout, msg)
			return err
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "データディレクトリ（ワールドを含む）と drop-in も削除する")
	return cmd
}
