package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func (a *app) newListCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "サーバーの一覧を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.ListServersWithResponse(cmd.Context())
			if err != nil {
				return a.connError(err)
			}
			if err := expect(res, res.Body, http.StatusOK, ""); err != nil {
				return err
			}
			if output == outputJSON {
				return writeJSON(a.stdout, res.Body)
			}

			tw := newTabWriter(a.stdout)
			_, _ = fmt.Fprintln(tw, "NAME\tTYPE\tVERSION\tPORT\tDESIRED\tACTUAL")
			for _, s := range res.JSON200.Servers {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
					s.Spec.Name, s.Spec.Type, s.Spec.Version, s.Spec.Port, s.DesiredState, s.Status.State)
			}
			return tw.Flush()
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}
