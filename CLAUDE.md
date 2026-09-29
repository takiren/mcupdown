# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 概要
1台のホストで少数のマイクラサーバー（Java 版）をコンテナで管理するツール。常駐デーモン `mcctld` と、その API を叩くだけの CLI `mcctl` からなる。リポジトリ名は `mcupdown`（ifupdown 由来）。

**要件と設計の正は `docs/requirements.md`**。README は初期の構想（docker 対応、rootful 対応など）のままで、現在の設計とは異なる。設計判断の根拠（実機での検証結果）は GitHub Issue #2 のコメントにある。

ドキュメント、コードのコメント、Issue、PR は日本語で書く。

## コマンド
CI（`.github/workflows/ci.yml`）と同じチェック:

```sh
go mod tidy -diff                 # go.mod / go.sum の整合
go generate ./...                 # API のコード生成（生成物に差分が出たら CI が落ちる）
go build ./...                    # CI は GOOS=linux と GOOS=darwin の両方でビルドする
go test -race -shuffle=on ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...
```

- 1つのテストだけ実行: `go test -race -run 'TestUnixSocketRoundTrip/down_with_force' ./internal/api/`
- 手元の golangci-lint が古い Go でビルドされていると go1.27 のモジュールを扱えないので、上のように CI と同じバージョンを `go run` で使う。
- oapi-codegen は `go.mod` の `tool` ディレクティブで固定している（`go tool oapi-codegen`）。

## アーキテクチャ
```
mcctl ──HTTP over Unix socket (/run/mcctl/mcctld.sock)──▶ mcctld
  mcctld ├─ 書き込み ─▶ ~mcctl/.config/containers/systemd/mcctl-<name>.container（quadlet）
         ├─ D-Bus ───▶ systemd --user（start / stop / daemon-reload / unit の状態）
         ├─ 互換 API ─▶ podman.sock（pull / inspect / exec）
         ├─ journalctl --user -o json（ログ）
         └─ Store（/var/lib/mcctl/state.json）
```

押さえておくべき設計:
- **rootless podman 専用**。コンテナのライフサイクルは quadlet と systemd --user に任せ、mcctld は `.container` ファイルを生成するフロントエンドとして動く。互換 API でコンテナを直接作らない（ホストのシャットダウン時にワールドが保存されないため）。
- **Store が正**で、Store には「求める状態」（定義と desiredState）だけを持つ。`.container` ファイルは Store から毎回生成する生成物。実際の状態は常に systemd と podman に問い合わせる。
- desiredState は `.container` の `[Install]` の有無に反映する。これで、ホストの再起動後に起動するかどうかが一致する。
- up / down は非同期（202）。サーバーごとのロックを持ち、操作中の変更系は 409。最後の操作の結果はデーモンのメモリにだけ持つ。
- mcctld は mcctl ユーザーの systemd --user サービスとして動く。mcctl ユーザーは linger と `systemd-journal` グループが必須。

依存の向き: `cmd/mcctld` → `internal/api`（ハンドラ）→ `internal/daemon` → `internal/store`, `internal/engine`。
`cmd/mcctl` は `internal/api` の生成クライアント（`api.NewUnixSocketClient`）を使うだけで、ロジックを持たない。

### API（`api/openapi.yaml` → `internal/api`）
- 仕様を変えたら `go generate ./...` で再生成し、生成物もコミットする。`internal/api/api.gen.go` は手で編集しない。
- 生成するのは strict server（標準の net/http）とクライアント。ハンドラは `StrictServerInterface` を実装し、`api.NewHandler` で `/v1`（`api.BasePath`）の下にマウントする。仕様の `servers` の URL と `BasePath` は合わせておく。
- enum の定数名には常に型名が前に付く（`ErrorCodeServerStarting`、`ActualStateRunning` など）。
- 409 の理由は `ErrorCode` で区別する。新しい失敗の種類を足すときは enum に追加する。

## 実装で守ること（#2 の検証で分かったこと）
- podman は OCI 形式のイメージの HEALTHCHECK を読まない。`.container` に `HealthCmd=mc-health` などを明示する。
- SELinux 環境では、バインドマウントに `:Z` を付けないと `/data` に書き込めない。
- rootless なので itzg には常に `UID=0` / `GID=0` を渡す（ホストの mcctl ユーザーに対応する）。
- 起動処理の途中の SIGTERM は itzg／マイクラが無視し、60 秒後に SIGKILL される。そのため starting 中の down は既定で 409 にする。
- quadlet のコンテナは `--rm` で動くので、ログは `LogDriver=journald` にして journal から読む。

## 開発環境
- macOS ではユニットテストと CLI の開発だけを行う。外部とのやり取り（quadlet ファイル、systemd、podman、journal）はインターフェースに切り出し、フェイクでテストする。
- 実際の podman と systemd を使う確認は Linux のマシンで行う。検証用マシンは Ansible で構築できるようにする予定（#20）。
- mcctl ユーザーとして podman や systemctl --user を操作するときは、`XDG_RUNTIME_DIR` と `DBUS_SESSION_BUS_ADDRESS` が必要（`systemctl --user -M mcctl@` も使える）。

## 進め方
- main は保護されている。作業はブランチを切って PR を出す。PR には対応する Issue を `Closes #N` で書く。
- タスクは GitHub Issue（マイルストーン「MVP」）で管理している。Issue の本文を書き換えるときは、置き換えに失敗したら止まる方法で行い、本文が空にならないことを確認する。
