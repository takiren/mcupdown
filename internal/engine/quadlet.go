package engine

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// generatedHeader は生成したファイルの先頭行の固定部分。生成物かどうかの判定にも使う。
const generatedHeader = "# このファイルは mcctld が生成する。編集しないこと。"

// IsGenerated は内容が mcctld の生成した .container ファイルかどうかを返す。
func IsGenerated(content []byte) bool {
	return bytes.HasPrefix(content, []byte(generatedHeader))
}

// RenderContainerFile は Server から quadlet の .container ファイルの内容を生成する。
// 同じ入力からは常に同じ内容を返す。
func RenderContainerFile(s Server) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	limit, err := ContainerMemoryLimit(s.Memory)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}

	line("%s調整は %s/*.conf で行う。", generatedHeader, DropInDirName(s.Name))
	line("[Unit]")
	line("Description=mcctl server %s", s.Name)
	line("")
	line("[Container]")
	line("ContainerName=%s", ContainerName(s.Name))
	line("Image=%s", ImageRef(s.ImageTag))
	// pull は mcctld が互換 API で先に済ませる。
	line("Pull=never")
	for _, kv := range containerEnv(s) {
		line("Environment=%s", quoteUnitValue(kv))
	}
	line("Label=%s=true", LabelManaged)
	line("Label=%s=%s", LabelServer, s.Name)
	// :Z がないと SELinux 環境で /data に書き込めない。
	line("Volume=%s:/data:Z", s.DataDir)
	line("PublishPort=%d:25565/tcp", s.Port)
	// OCI 形式のイメージの HEALTHCHECK は podman が読まないので明示する。
	line("HealthCmd=mc-health")
	line("HealthInterval=30s")
	line("HealthStartPeriod=120s")
	line("HealthRetries=2")
	line("Notify=healthy")
	line("StopTimeout=60")
	// quadlet のコンテナは --rm で動くので、ログは journal に残す。
	line("LogDriver=journald")
	line("Memory=%s", limit)
	line("")
	line("[Service]")
	line("Restart=on-failure")
	line("RestartSec=5")
	line("RestartSteps=5")
	line("RestartMaxDelaySec=5min")
	line("TimeoutStartSec=900")
	line("TimeoutStopSec=90")
	if s.AutoStart {
		line("")
		line("[Install]")
		line("WantedBy=default.target")
	}
	return b.Bytes(), nil
}

// containerEnv はコンテナに渡す環境変数を KEY=VALUE の形で返す。
// 専用のフィールドから作るものが先に並び、Env は重ならないものだけがキー順に続く。
func containerEnv(s Server) []string {
	fixed := [][2]string{
		{"EULA", "TRUE"},
		{"VERSION", s.Version},
		{"TYPE", s.Type},
		{"MEMORY", s.Memory},
		// rootless ではコンテナ内の root がホストの mcctl ユーザーに対応する。
		{"UID", "0"},
		{"GID", "0"},
	}
	env := make([]string, 0, len(fixed)+len(s.Env))
	reserved := make(map[string]bool, len(fixed))
	for _, kv := range fixed {
		reserved[kv[0]] = true
		env = append(env, kv[0]+"="+kv[1])
	}
	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		if reserved[k] {
			continue
		}
		env = append(env, k+"="+s.Env[k])
	}
	return env
}

// quoteUnitValue は値を quadlet（systemd のユニットファイル）の1語として書ける形にする。
//
// quadlet は Environment= の値をそのまま ExecStart の引数にし、systemd がそれを解釈する。
// そのため systemd の指定子（%）と環境変数の展開（$）を二重にしてエスケープする。
// 空白や引用符を含む場合は全体を二重引用符で囲み、\ と " をエスケープする。
// どちらも実機（podman 5.8.2 / systemd 257）で、コンテナに元の値が届くことを確認した。
func quoteUnitValue(s string) string {
	s = strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
	if !strings.ContainsAny(s, " \"'\\") {
		return s
	}
	// 制御文字は Validate で弾いているので、エスケープが要るのは \ と " だけ。
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
