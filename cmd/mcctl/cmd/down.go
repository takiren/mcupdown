package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/takiren/mcupdown/internal/api"
)

func (a *app) newDownCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "down <name>",
		Short: "サーバーを停止する",
		Long: `サーバーの求める状態を stopped にして停止する。ワールドは保存され、データは残る。
停止は非同期で、進み具合は status で確認する。起動処理の途中なら --force が必要。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			params := &api.DownServerParams{}
			if force {
				params.Force = &force
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.DownServerWithResponse(cmd.Context(), name, params)
			if err != nil {
				return a.connError(err)
			}
			if err := expect(res, res.Body, http.StatusAccepted, name); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.stdout, "%s の停止を受け付けました。進み具合は mcctl status %s で確認できます\n", name, name)
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "起動処理の途中でも停止する（最大 60 秒後に強制終了され、ワールドが壊れるおそれがある）")
	return cmd
}
