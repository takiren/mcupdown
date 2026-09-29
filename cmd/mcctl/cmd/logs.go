package cmd

import (
	"bufio"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/takiren/mcupdown/internal/api"
)

func (a *app) newLogsCommand() *cobra.Command {
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "サーバーのログを表示する",
		Long: `サーバーのログ（journal）の末尾を表示する。停止中のサーバーでも過去のログを読める。
[container] はマイクラの出力、[systemd] は起動・停止・再起動の記録。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			params := &api.GetServerLogsParams{}
			if cmd.Flags().Changed("tail") {
				params.Tail = &tail
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.GetServerLogsWithResponse(cmd.Context(), name, params)
			if err != nil {
				return a.connError(err)
			}
			if err := expect(res, res.Body, http.StatusOK, name); err != nil {
				return err
			}
			w := bufio.NewWriter(a.stdout)
			for _, e := range res.JSON200.Entries {
				_, _ = fmt.Fprintf(w, "%s [%s] %s\n", a.formatTime(e.Time), e.Source, e.Message)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 0, "末尾から何件表示するか（既定: 200、最大: 10000）")
	return cmd
}
