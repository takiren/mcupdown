# 要件定義

## 概要
1台のホストで少数のマイクラサーバー（Java 版）をコンテナで管理するツール。
k8s を使うほどではないが、手でコンテナを管理するのは面倒という人向け。

- `mcctld`: 常駐デーモン。コンテナエンジンを操作し、サーバーの求める状態を保持する。
- `mcctl`: CLI クライアント。`mcctld` の API を叩くだけで、ロジックは持たない。

リポジトリ名は `mcupdown`（ifupdown 由来）、コマンド名は打ちやすさ優先で `mcctl` / `mcctld`。

```
mcctl ──HTTP over Unix socket──▶ mcctld ──Docker Engine API──▶ docker / podman ──▶ itzg/minecraft-server
                                   │
                                   └──▶ Store (state.json)
```

## 基本方針
| 項目 | 決定 |
| --- | --- |
| CLI ⇔ デーモン | Unix ソケット上の HTTP。後から API サーバーを生やすときも同じハンドラを使う |
| API 仕様 | OpenAPI (`api/openapi.yaml`) を正とし、oapi-codegen でサーバー I/F とクライアントを生成 |
| コンテナイメージ | `itzg/minecraft-server`。自前ビルドはしない |
| エンジン操作 | Docker Engine API。podman は Docker 互換ソケットで対応 |
| 操作モデル | 命令型 CLI。サーバーは名前で識別 |
| Store の役割 | **求める状態のみ**保持する。実際の状態は常にエンジンに問い合わせる |
| 非同期 | 時間のかかる操作（up / down）は 202 で即時応答し、進み具合は `status` で確認する |

## 実行環境
- 本番は Linux（systemd）。macOS は開発用で、Docker Desktop などで動けばよい。認可は簡略化してよい。
- `mcctld` は専用のシステムユーザー `mcctl` で systemd のサービスとして常駐させる。
  - rootless podman（mcctl ユーザーのもの）か、docker グループ経由の docker を使う。
- 対象エンジンは docker と podman。rootless / rootful の両方で動くこと。
- エンジンのソケットは `DOCKER_HOST` で指定する。

### パス（既定値。すべてフラグ・環境変数で上書き可能）
| 用途 | パス |
| --- | --- |
| API ソケット | `/run/mcctl/mcctld.sock`（0666, owner mcctl） |
| Store | `/var/lib/mcctl/state.json` |
| サーバーデータ | `/var/lib/mcctl/servers/<name>/`（コンテナの `/data` にバインドマウント） |

設定ファイルは当面作らない。

## 認可
- ソケットは 0666。接続元の UID をカーネルから取得する（Linux: `SO_PEERCRED`）。
- 参照系（list / status / logs）は誰でも実行できる。
- 変更系（create / up / down / rm）は `mcctl` グループのメンバーだけ実行できる。それ以外には 403 を返す。
- macOS では認可をスキップしてよい。

## サーバー定義（Store に保存する内容）
| フィールド | CLI フラグ | 既定値 | 備考 |
| --- | --- | --- | --- |
| name | 位置引数 | 必須 | `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`。作成後は変更不可 |
| port | `--port` | 必須 | 他のサーバーと重複したら create を失敗させる |
| eulaAccepted | `--accept-eula` | 必須 | 指定がなければ create を失敗させる |
| version | `--version` | `LATEST` | itzg の `VERSION` |
| type | `--type` | `VANILLA` | itzg の `TYPE` にそのまま渡す。値の検証はイメージに任せる |
| memory | `--memory` | `2G`（仮） | 下記「メモリ」を参照 |
| imageTag | `--image-tag` | `latest` | Java のバージョン選択に使う（`java8` など） |
| env | `--env KEY=VAL`（複数可） | なし | 任意の itzg 環境変数。専用フラグと重なったら専用フラグを優先する |
| desiredState | — | `stopped` | `up` で `running`、`down` で `stopped` にする |

### メモリ
- `--memory` の値を JVM のヒープ（itzg の `MEMORY`）に渡す。
- コンテナのメモリ上限は、ヒープ以外に使う分を見込んで自動で計算する（初期案: `ヒープ × 1.25 + 512M`）。

## コンテナの扱い
- コンテナ名は `mcctl-<name>`。ラベルは `mcctl.managed=true`, `mcctl.server=<name>`。
- restart policy は `on-failure`。ホストが再起動した後の復旧は、デーモンの起動時同期に任せる。
- `down` は SIGTERM を送り、猶予 60 秒で正常に停止させてから**コンテナを削除**する。
- `up` は Store の定義から**毎回コンテナを作り直す**。設定の変更は次の up で反映される。
- イメージは up のときに手元になければ pull する。`--pull` を付けると常に pull する。
- 起動完了はコンテナの healthcheck（itzg の mc-health）が healthy になったことで判定する。

### UID/GID
- Engine API の `/info` で rootless かどうかを自動で判定する。
  - rootless なら `UID=0` / `GID=0` を渡す（コンテナ内の root はホストの mcctl ユーザーに対応する）。
  - rootful なら mcctld 自身の UID/GID を渡す。
- どちらの場合も、データディレクトリの中身はホストの mcctl ユーザーの持ち物になる。ホストから編集するときは `sudo -u mcctl` を使う。

### マイクラのコマンド実行（MVP の後）
- Engine API の exec で、コンテナ内の `rcon-cli` を実行する。RCON のポートは外に出さない。

## デーモンの動作
### 起動時同期
- Store の desiredState と実際のコンテナを突き合わせる。
  - `running` なのにコンテナがない、または動いていない → up する
  - `stopped` なのにコンテナがある → down する
- `mcctl.managed=true` のラベルが付いているのに Store にないコンテナは、ログに警告を出すだけで触らない。
- 動いている間に定期的な reconcile はしない。クラッシュからの復旧は restart policy に任せる。

### 同時操作
- サーバーごとにロックを持つ。操作の実行中に同じサーバーへ変更系の操作が来たら 409 Conflict を返す。
- 別のサーバーへの操作は並行して実行できる。

### 失敗の通知
- サーバーごとに「最後の操作（種類、開始時刻と終了時刻、結果、エラー）」をデーモンのメモリに持ち、`status` に表示する。
- 永続化はしない（Store には求める状態だけを保存するため）。

## CLI
### MVP
| コマンド | 動作 | 応答 |
| --- | --- | --- |
| `mcctl create <name> --port N --accept-eula [...]` | Store に登録し、データディレクトリを作る | 同期 |
| `mcctl up <name> [--pull]` | desiredState を running にして起動する | 非同期 |
| `mcctl down <name>` | desiredState を stopped にして停止・削除する | 非同期 |
| `mcctl rm <name> [--purge]` | Store から削除する。`--purge` でデータも削除する。desiredState が running か、コンテナがあれば 409 | 同期 |
| `mcctl list [-o json]` | 一覧を表示する | 同期 |
| `mcctl status <name> [-o json]` | 詳細（求める状態、実際の状態、health、最後の操作）を表示する | 同期 |
| `mcctl logs <name> [-f] [--tail N]` | コンテナのログを表示する | ストリーム |

全コマンド共通: `--socket` でソケットのパスを指定できる。環境変数 `MCCTL_SOCKET` でも可。

出力イメージ:
```
$ mcctl list
NAME      TYPE     VERSION  PORT   DESIRED  ACTUAL
survival  PAPER    1.21.4   25565  running  running
creative  VANILLA  1.21.4   25566  stopped  -
old       VANILLA  1.12.2   25567  running  starting

$ mcctl status old
Name:     old
Desired:  running
Actual:   starting (health: starting)
Image:    itzg/minecraft-server:java8
Last op:  up  2026-09-27 14:02  ok
```

### MVP の後
- `set`: 設定の変更。Store だけを書き換え、次の up で反映する。status には「未反映の変更あり」と表示する。
- `exec`: rcon-cli でマイクラのコマンドを実行する。
- `restart`: down してから up する。
- `mcup` / `mcdown` という別名（遊び）。

### スコープ外
- バックアップ機能（当面はホストのディレクトリを rsync する）
- 統合版（Bedrock）
- リモート API（TCP＋認証）
- Web UI

## API 草案（`/v1`）
| メソッド | パス | 対応するコマンド | 成功時 |
| --- | --- | --- | --- |
| GET | `/v1/servers` | list | 200 |
| POST | `/v1/servers` | create | 201 |
| GET | `/v1/servers/{name}` | status | 200 |
| DELETE | `/v1/servers/{name}?purge=bool` | rm | 204 |
| POST | `/v1/servers/{name}/up?pull=bool` | up | 202 |
| POST | `/v1/servers/{name}/down` | down | 202 |
| GET | `/v1/servers/{name}/logs?follow=bool&tail=N` | logs | 200（ストリーム） |

エラー形式: `{"error": {"code": "conflict", "message": "..."}}`
主なステータス: 400（検証エラー）、403（認可）、404、409（操作中、ポートの重複、実行中のサーバーを rm しようとした）

## テスト
- コンテナ操作は小さな `Engine` インターフェースに切り出す。デーモンのロジックと API はフェイクを使ってユニットテストする。
- 実際の docker / podman を使うテストは `//go:build integration` を付け、手動で実行する。

## 実装時に確認するリスク
- **podman の healthcheck**: podman は healthcheck を systemd タイマーで動かす。systemd のユーザーセッションがない環境（linger なしの mcctl ユーザーなど）では、health がずっと `starting` のままになるおそれがある。そうなったらデーモンが自分で health を問い合わせる、などの代わりの手段を考える。
- **podman の互換 API**: 互換 API で、restart policy・メモリ上限・`/info` による rootless 判定が期待どおりに動くか確認する。
- **ホストの再起動後**: rootless podman の場合、mcctl ユーザーの linger を有効にしておかないとデーモンが起動しない。セットアップ手順に書く。

## リポジトリ構成（予定）
- `cmd/mcctl`: CLI（今の `cmd/client` を改名する）
- `cmd/mcctld`: デーモン
- `api/openapi.yaml`: API 仕様
- `internal/store`: Store（JSON ファイルの実装）
- `internal/engine`: Engine インターフェースと Docker API の実装
