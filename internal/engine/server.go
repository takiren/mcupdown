package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ImageRepository は使うコンテナイメージのリポジトリ。タグは Server.ImageTag で選ぶ。
const ImageRepository = "docker.io/itzg/minecraft-server"

// コンテナに付けるラベル。
const (
	LabelManaged = "mcctl.managed"
	LabelServer  = "mcctl.server"
)

// ErrInvalidServer は Server の値が .container ファイルに書けないときのエラー。
var ErrInvalidServer = errors.New("invalid server definition")

// Server は .container ファイルに落とすサーバーの定義。
// Store や API の型とは独立していて、変換はデーモン側で行う。
type Server struct {
	// Name はサーバー名。unit 名やコンテナ名にも使う。
	Name string
	// Port はホスト側で公開するポート。コンテナ側は常に 25565。
	Port int
	// Version は itzg の VERSION。
	Version string
	// Type は itzg の TYPE。
	Type string
	// Memory は JVM のヒープ（itzg の MEMORY）。"2G" や "512M" の形式。
	Memory string
	// ImageTag は ImageRepository のタグ。
	ImageTag string
	// Env は itzg に渡す任意の環境変数。専用のフィールドと重なるキーは無視される。
	Env map[string]string
	// DataDir はコンテナの /data にバインドマウントするホスト側のディレクトリ（絶対パス）。
	DataDir string
	// AutoStart が true なら [Install] を出力し、ホストの再起動後に自動で起動する。
	// desiredState が running のときに true にする。
	AutoStart bool
}

// UnitName はサーバーの systemd の unit 名を返す。
func UnitName(server string) string { return "mcctl-" + server + ".service" }

// ContainerName はサーバーのコンテナ名を返す。
func ContainerName(server string) string { return "mcctl-" + server }

// ContainerFileName はサーバーの quadlet ファイル名を返す。
func ContainerFileName(server string) string { return "mcctl-" + server + ".container" }

// DropInDirName はサーバーの quadlet の drop-in ディレクトリ名を返す。
func DropInDirName(server string) string { return ContainerFileName(server) + ".d" }

// ImageRef はタグからイメージの参照を返す。
func ImageRef(tag string) string { return ImageRepository + ":" + tag }

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	tagPattern      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	envKeyPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memoryPattern   = regexp.MustCompile(`^([1-9][0-9]*)([MG])$`)
	dataDirPattern  = regexp.MustCompile(`^/[A-Za-z0-9/._-]*$`)
	controlCharsSet = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f"
)

// ValidName はサーバー名として使えるかを返す。ファイル名や unit 名に埋め込むので厳しく制限する。
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Validate は .container ファイルに安全に書けるかを検証する。
func (s Server) Validate() error {
	if !ValidName(s.Name) {
		return fmt.Errorf("%w: name %q", ErrInvalidServer, s.Name)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("%w: port %d", ErrInvalidServer, s.Port)
	}
	if !tagPattern.MatchString(s.ImageTag) {
		return fmt.Errorf("%w: image tag %q", ErrInvalidServer, s.ImageTag)
	}
	if _, err := memoryMiB(s.Memory); err != nil {
		return err
	}
	// Volume= はホスト側のパスをそのまま podman の引数にするので、区切り文字や systemd の指定子になる文字を許さない。
	if !dataDirPattern.MatchString(s.DataDir) || filepath.Clean(s.DataDir) != s.DataDir {
		return fmt.Errorf("%w: data dir %q", ErrInvalidServer, s.DataDir)
	}
	for field, v := range map[string]string{"version": s.Version, "type": s.Type} {
		if v == "" || strings.ContainsAny(v, controlCharsSet) {
			return fmt.Errorf("%w: %s %q", ErrInvalidServer, field, v)
		}
	}
	for k, v := range s.Env {
		if !envKeyPattern.MatchString(k) {
			return fmt.Errorf("%w: env key %q", ErrInvalidServer, k)
		}
		if strings.ContainsAny(v, controlCharsSet) {
			return fmt.Errorf("%w: env %s contains control characters", ErrInvalidServer, k)
		}
	}
	return nil
}

// memoryMiB は "2G" や "512M" を MiB に変換する。
func memoryMiB(heap string) (int64, error) {
	m := memoryPattern.FindStringSubmatch(heap)
	if m == nil {
		return 0, fmt.Errorf("%w: memory %q", ErrInvalidServer, heap)
	}
	var n int64
	if _, err := fmt.Sscan(m[1], &n); err != nil || n > 1<<20 {
		return 0, fmt.Errorf("%w: memory %q", ErrInvalidServer, heap)
	}
	if m[2] == "G" {
		n *= 1024
	}
	return n, nil
}

// ContainerMemoryLimit は JVM のヒープからコンテナのメモリ上限を計算する。
// ヒープ以外（メタスペース、スレッド、ネイティブのバッファなど）を見込んで「ヒープ × 1.25 + 512M」とする。
// 戻り値は podman の --memory に渡せる形式（MiB 単位、例: "3072m"）。
func ContainerMemoryLimit(heap string) (string, error) {
	mib, err := memoryMiB(heap)
	if err != nil {
		return "", err
	}
	limit := (mib*5+3)/4 + 512 // ×1.25 を切り上げる
	return fmt.Sprintf("%dm", limit), nil
}
