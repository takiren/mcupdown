package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/takiren/mcupdown/internal/api"
)

func (a *app) newUpCommand() *cobra.Command {
	var pull bool
	cmd := &cobra.Command{
		Use:   "up <name>",
		Short: "サーバーを起動する",
		Long:  `サーバーの求める状態を running にして起動する。起動は非同期で、進み具合は status で確認する。`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			params := &api.UpServerParams{}
			if pull {
				params.Pull = &pull
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.UpServerWithResponse(cmd.Context(), name, params)
			if err != nil {
				return a.connError(err)
			}
			if err := expect(res, res.Body, http.StatusAccepted, name); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.stdout, "%s の起動を受け付けました。進み具合は mcctl status %s で確認できます\n", name, name)
			return err
		},
	}
	cmd.Flags().BoolVar(&pull, "pull", false, "イメージが手元にあっても pull する")
	return cmd
}
