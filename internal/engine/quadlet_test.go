package engine

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "testdata/*.golden を更新する")

func testServer() Server {
	return Server{
		Name:     "survival",
		Port:     25565,
		Version:  "1.21.4",
		Type:     "PAPER",
		Memory:   "2G",
		ImageTag: "java21",
		DataDir:  "/var/lib/mcctl/servers/survival",
	}
}

func TestRenderContainerFileGolden(t *testing.T) {
	running := testServer()
	running.AutoStart = true
	running.Env = map[string]string{
		"DIFFICULTY": "hard",
		// 専用のフィールドと重なるキーは無視される。
		"MEMORY": "8G",
		"EULA":   "FALSE",
		"UID":    "1000",
		// 空白、systemd の指定子（%）、変数展開（$）、引用符、バックスラッシュ、非 ASCII を含む値。
		"MOTD":              `§lWelcome 100% "fun" $HOME \o/`,
		"MODRINTH_PROJECTS": "fabric-api,lithium",
		"OPS":               "it's_me",
	}

	stopped := testServer()
	stopped.Name = "creative"
	stopped.Port = 25566
	stopped.Type = "VANILLA"
	stopped.Version = "LATEST"
	stopped.Memory = "1536M"
	stopped.ImageTag = "latest"
	stopped.DataDir = "/var/lib/mcctl/servers/creative"

	for name, s := range map[string]Server{"running": running, "stopped": stopped} {
		t.Run(name, func(t *testing.T) {
			got, err := RenderContainerFile(s)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", name+".container.golden")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v（go test ./internal/engine/ -run Golden -update で作る）", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("生成結果がゴールデンファイルと違う（意図した変更なら -update で更新する）\n--- got\n%s\n--- want\n%s", got, want)
			}
			if !IsGenerated(got) {
				t.Error("IsGenerated = false")
			}
		})
	}
}

func TestRenderContainerFileInstall(t *testing.T) {
	s := testServer()
	s.AutoStart = false
	off, err := RenderContainerFile(s)
	if err != nil {
		t.Fatal(err)
	}
	s.AutoStart = true
	on, err := RenderContainerFile(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(off, []byte("[Install]")) {
		t.Error("AutoStart=false なのに [Install] がある")
	}
	if !bytes.HasSuffix(on, []byte("\n[Install]\nWantedBy=default.target\n")) {
		t.Errorf("AutoStart=true なのに [Install] で終わっていない:\n%s", on)
	}
}

func TestContainerEnvPrecedence(t *testing.T) {
	s := testServer()
	s.Env = map[string]string{"VERSION": "1.0", "TYPE": "FORGE", "GID": "5", "ZZZ": "last", "AAA": "first"}
	got := strings.Join(containerEnv(s), " ")
	want := "EULA=TRUE VERSION=1.21.4 TYPE=PAPER MEMORY=2G UID=0 GID=0 AAA=first ZZZ=last"
	if got != want {
		t.Errorf("containerEnv =\n %s\nwant\n %s", got, want)
	}
}

func TestQuoteUnitValue(t *testing.T) {
	for in, want := range map[string]string{
		"PLAIN=abc":         "PLAIN=abc",
		"MOTD=A§lB":         "MOTD=A§lB",
		"PCT=50%":           "PCT=50%%",
		"DOLLAR=a$b":        "DOLLAR=a$$b",
		"BRACE=x${HOME}y":   "BRACE=x$${HOME}y",
		"SPACE=hello world": `"SPACE=hello world"`,
		`QUOTE=say "hi"`:    `"QUOTE=say \"hi\""`,
		`BS=a\b`:            `"BS=a\\b"`,
		"APOS=it's":         `"APOS=it's"`,
	} {
		if got := quoteUnitValue(in); got != want {
			t.Errorf("quoteUnitValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContainerMemoryLimit(t *testing.T) {
	for heap, want := range map[string]string{
		"2G":    "3072m", // 2048 × 1.25 + 512
		"1G":    "1792m",
		"512M":  "1152m",
		"1001M": "1764m", // 1251.25 を切り上げて 1252 + 512
		"4G":    "5632m",
	} {
		got, err := ContainerMemoryLimit(heap)
		if err != nil || got != want {
			t.Errorf("ContainerMemoryLimit(%q) = %q, %v; want %q", heap, got, err, want)
		}
	}
	for _, bad := range []string{"", "2", "2g", "0G", "02G", "2GB", "-1G", "99999999999G"} {
		if _, err := ContainerMemoryLimit(bad); !errors.Is(err, ErrInvalidServer) {
			t.Errorf("ContainerMemoryLimit(%q) err = %v, want ErrInvalidServer", bad, err)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Server){
		"name uppercase":       func(s *Server) { s.Name = "Survival" },
		"name path traversal":  func(s *Server) { s.Name = "../etc" },
		"name too long":        func(s *Server) { s.Name = strings.Repeat("a", 33) },
		"port zero":            func(s *Server) { s.Port = 0 },
		"port too big":         func(s *Server) { s.Port = 65536 },
		"image tag with slash": func(s *Server) { s.ImageTag = "evil/tag" },
		"image tag empty":      func(s *Server) { s.ImageTag = "" },
		"memory lowercase":     func(s *Server) { s.Memory = "2g" },
		"data dir relative":    func(s *Server) { s.DataDir = "servers/survival" },
		"data dir with colon":  func(s *Server) { s.DataDir = "/data:/etc" },
		"data dir with space":  func(s *Server) { s.DataDir = "/var/lib/my server" },
		"data dir not clean":   func(s *Server) { s.DataDir = "/var/lib/../etc" },
		"data dir with %":      func(s *Server) { s.DataDir = "/var/%h" },
		"version empty":        func(s *Server) { s.Version = "" },
		"type with newline":    func(s *Server) { s.Type = "PAPER\nExecStart=/bin/sh" },
		"env key invalid":      func(s *Server) { s.Env = map[string]string{"A-B": "x"} },
		"env value newline":    func(s *Server) { s.Env = map[string]string{"MOTD": "a\n[Service]"} },
		"env value tab":        func(s *Server) { s.Env = map[string]string{"MOTD": "a\tb"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := testServer()
			mutate(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidServer) {
				t.Errorf("Validate() = %v, want ErrInvalidServer", err)
			}
			if _, err := RenderContainerFile(s); !errors.Is(err, ErrInvalidServer) {
				t.Errorf("RenderContainerFile() err = %v, want ErrInvalidServer", err)
			}
		})
	}
	if err := testServer().Validate(); err != nil {
		t.Errorf("valid server: %v", err)
	}
}

func TestNames(t *testing.T) {
	if got := UnitName("survival"); got != "mcctl-survival.service" {
		t.Errorf("UnitName = %q", got)
	}
	if got := ContainerName("survival"); got != "mcctl-survival" {
		t.Errorf("ContainerName = %q", got)
	}
	if got := ContainerFileName("survival"); got != "mcctl-survival.container" {
		t.Errorf("ContainerFileName = %q", got)
	}
	if got := DropInDirName("survival"); got != "mcctl-survival.container.d" {
		t.Errorf("DropInDirName = %q", got)
	}
	if got := ImageRef("java21"); got != "docker.io/itzg/minecraft-server:java21" {
		t.Errorf("ImageRef = %q", got)
	}
}
