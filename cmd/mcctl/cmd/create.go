package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/takiren/mcupdown/internal/api"
)

func (a *app) newCreateCommand() *cobra.Command {
	var (
		port       int
		acceptEULA bool
		version    string
		typ        string
		memory     string
		imageTag   string
		envs       []string
	)
	cmd := &cobra.Command{
		Use:   "create <name> --port N --accept-eula",
		Short: "サーバーを登録する",
		Long: `サーバーを登録し、データディレクトリを作る。起動はしない（mcctl up で起動する）。
省略したフラグは mcctld の既定値になる（version: LATEST, type: VANILLA, image-tag: latest）。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !acceptEULA {
				return errors.New("サーバーを動かすには Minecraft の EULA（https://aka.ms/MinecraftEULA）への同意が必要です。同意する場合は --accept-eula を付けてください")
			}
			env, err := parseEnv(envs)
			if err != nil {
				return err
			}

			req := api.CreateServerRequest{Name: name, Port: port, AcceptEula: true}
			f := cmd.Flags()
			if f.Changed("version") {
				req.Version = &version
			}
			if f.Changed("type") {
				req.Type = &typ
			}
			if f.Changed("memory") {
				req.Memory = &memory
			}
			if f.Changed("image-tag") {
				req.ImageTag = &imageTag
			}
			if len(env) > 0 {
				req.Env = &env
			}

			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.CreateServerWithResponse(cmd.Context(), req)
			if err != nil {
				return a.connError(err)
			}
			if err := expect(res, res.Body, http.StatusCreated, name); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.stdout, "%s を登録しました（ポート %d）。mcctl up %s で起動できます\n", name, port, name)
			return err
		},
	}
	f := cmd.Flags()
	f.IntVar(&port, "port", 0, "公開するポート（必須）")
	f.BoolVar(&acceptEULA, "accept-eula", false, "Minecraft の EULA に同意する（必須）")
	f.StringVar(&version, "version", "", "マイクラのバージョン（itzg の VERSION。既定: LATEST）")
	f.StringVar(&typ, "type", "", "サーバーの種類（itzg の TYPE。既定: VANILLA）")
	f.StringVar(&memory, "memory", "", "JVM のヒープ（例: 4G）")
	f.StringVar(&imageTag, "image-tag", "", "itzg/minecraft-server のタグ。Java のバージョン選択に使う（既定: latest）")
	f.StringArrayVar(&envs, "env", nil, "itzg に渡す環境変数（KEY=VAL。複数指定できる）")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}

// parseEnv は KEY=VAL の並びを map にする。同じキーは後のものが優先される。
func parseEnv(envs []string) (map[string]string, error) {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--env は KEY=VAL の形式で指定してください: %q", e)
		}
		m[k] = v
	}
	return m, nil
}
