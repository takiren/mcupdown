package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

const (
	outputTable = "table"
	outputJSON  = "json"

	timeLayout = "2006-01-02 15:04:05"
)

// addOutputFlag は -o / --output を登録する。
func addOutputFlag(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "output", "o", outputTable, "出力形式（table か json）")
}

func validateOutput(o string) error {
	switch o {
	case outputTable, outputJSON:
		return nil
	}
	return fmt.Errorf("不正な出力形式です: %q（table か json を指定してください）", o)
}

// writeJSON は API のレスポンスの本文を整形して書き出す。
func writeJSON(w io.Writer, body []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		return fmt.Errorf("レスポンスを JSON として整形できません: %w", err)
	}
	buf.WriteByte('\n')
	_, err := buf.WriteTo(w)
	return err
}

func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

func (a *app) formatTime(t time.Time) string {
	return t.In(a.loc).Format(timeLayout)
}
