package cmd

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/takiren/mcupdown/internal/api"
)

func (a *app) newStatusCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "サーバーの詳細を表示する",
		Long:  `求める状態、実際の状態、health、再起動回数、最後の操作を表示する。`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutput(output); err != nil {
				return err
			}
			name := args[0]
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.GetServerWithResponse(cmd.Context(), name)
			if err != nil {
				return a.connError(err)
			}
			if err := expect(res, res.Body, http.StatusOK, name); err != nil {
				return err
			}
			if output == outputJSON {
				return writeJSON(a.stdout, res.Body)
			}
			return a.writeStatus(res.JSON200)
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

func (a *app) writeStatus(s *api.Server) error {
	tw := newTabWriter(a.stdout)
	row := func(k, v string) { _, _ = fmt.Fprintf(tw, "%s:\t%s\n", k, v) }

	row("Name", s.Spec.Name)
	row("Desired", string(s.DesiredState))
	row("Actual", fmt.Sprintf("%s (health: %s)", s.Status.State, s.Status.Health))
	row("Restarts", fmt.Sprint(s.Status.Restarts))
	row("Type", s.Spec.Type)
	row("Version", s.Spec.Version)
	row("Port", fmt.Sprint(s.Spec.Port))
	row("Memory", s.Spec.Memory)
	row("Image", "itzg/minecraft-server:"+s.Spec.ImageTag)
	if len(s.Spec.Env) > 0 {
		row("Env", formatEnv(s.Spec.Env))
	}
	row("Last op", a.formatOperation(s.LastOperation))
	return tw.Flush()
}

func (a *app) formatOperation(op *api.Operation) string {
	if op == nil {
		return "-"
	}
	var result string
	switch op.Result {
	case api.OperationResultSucceeded:
		result = "ok"
	case api.OperationResultInProgress:
		result = "in progress"
	case api.OperationResultFailed:
		result = "failed"
		if op.Error != nil && *op.Error != "" {
			result += ": " + *op.Error
		}
	default:
		result = string(op.Result)
	}
	return fmt.Sprintf("%s  %s  %s", op.Kind, a.formatTime(op.StartedAt), result)
}

// formatEnv は環境変数をキーの順に KEY=VAL で並べる。
func formatEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + env[k]
	}
	return strings.Join(pairs, " ")
}
