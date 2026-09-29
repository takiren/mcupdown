# 要件定義

## 概要
1台のホストで少数のマイクラサーバー（Java 版）をコンテナで管理するツール。
k8s を使うほどではないが、手でコンテナを管理するのは面倒という人向け。

- `mcctld`: 常駐デーモン。サーバーの求める状態を保持し、quadlet の `.container` ファイルを管理するフロントエンドとして動く。
- `mcctl`: CLI クライアント。`mcctld` の API を叩くだけで、ロジックは持たない。

リポジトリ名は `mcupdown`（ifupdown 由来）、コマンド名は打ちやすさ優先で `mcctl` / `mcctld`。

```
                                        ┌─ 書き込み ──▶ ~/.config/containers/systemd/mcctl-<name>.container
mcctl ──HTTP over Unix socket──▶ mcctld ┼─ D-Bus ────▶ systemd --user ──(quadlet)──▶ podman ──▶ itzg/minecraft-server
                                   │    └─ 互換 API ─▶ podman.sock（pull / inspect / exec）
                                   └──▶ Store (state.json)
```

## 基本方針
| 項目 | 決定 |
| --- | --- |
| CLI ⇔ デーモン | Unix ソケット上の HTTP。後から API サーバーを生やすときも同じハンドラを使う |
| API 仕様 | OpenAPI (`api/openapi.yaml`) を正とし、oapi-codegen でサーバー I/F とクライアントを生成 |
| コンテナイメージ | `itzg/minecraft-server`。自前ビルドはしない |
| コンテナエンジン | **rootless podman 専用**（docker と rootful podman には対応しない） |
| ライフサイクル | **quadlet ＋ systemd --user**。mcctld は `.container` ファイルを生成し、systemd を D-Bus（go-systemd）で操作する |
| 参照系 | podman の Docker 互換 API（pull / inspect / exec）。ログは journal（`journalctl --user`） |
| 操作モデル | 命令型 CLI。サーバーは名前で識別 |
| 正の所在 | **Store が正**。`.container` ファイルは Store から生成する生成物 |
| Store の役割 | **求める状態のみ**保持する。実際の状態は常に systemd と podman に問い合わせる |
| 非同期 | 時間のかかる操作（up / down）は 202 で即時応答し、進み具合は `status` で確認する |

quadlet を選んだ理由は #2 の検証結果を参照。要点:
- 互換 API で直接作ったコンテナは、ホストのシャットダウン時にワールドを保存せずに終了する。quadlet なら正常に停止する。
- 正常停止、起動順、再起動の間隔（バックオフ）、ホストの再起動後の自動起動を systemd に任せられる。
- `Notify=healthy` で、unit の状態がそのまま starting / running を表す。

## 実行環境
- Linux（systemd）＋ rootless podman。検証環境は AlmaLinux 10.2 / podman 5.8.2 / SELinux Enforcing / cgroup v2。
- macOS は、フェイクを使ったユニットテストと CLI の開発にだけ使う。実際の動作確認は Linux の VM で行う。
- 専用のシステムユーザー `mcctl` を作り、**linger を有効にする（必須）**。
  - linger がないとユーザー systemd が止まり、コンテナは保存されないまま落ちる。コンテナの起動自体もできない（#2）。
  - システムユーザーには subuid / subgid が自動で割り当てられないので、手動で付与する。
- `mcctld` は **mcctl ユーザーの systemd --user サービス**として動かす（`WantedBy=default.target`）。
- 起動時に podman の `/info` を確認し、rootless でなければエラーで終了する。

### パス（既定値。フラグ・環境変数で上書き可能）
| 用途 | パス |
| --- | --- |
| API ソケット | `/run/mcctl/mcctld.sock`（0666）。`/run/mcctl`（0755, mcctl:mcctl）は tmpfiles.d で作る |
| Store | `/var/lib/mcctl/state.json` |
| サーバーデータ | `/var/lib/mcctl/servers/<name>/`（コンテナの `/data` にバインドマウント） |
| quadlet ファイル | `/var/lib/mcctl/.config/containers/systemd/mcctl-<name>.container` |
| 手動で調整する drop-in | `/var/lib/mcctl/.config/containers/systemd/mcctl-<name>.container.d/*.conf` |
| podman の API ソケット | `$XDG_RUNTIME_DIR/podman/podman.sock`（`podman.socket` ユーザーユニット） |

`/var/lib/mcctl` は mcctl ユーザーのホームディレクトリ。設定ファイルは当面作らない。

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

## 生成する `.container` ファイル
Store の定義から毎回生成し、上書きする。先頭に「mcctld が生成したファイルなので編集しないこと、調整は drop-in で行うこと」と書く。
手で調整したい場合は、quadlet の drop-in（`mcctl-<name>.container.d/*.conf`）を使う。mcctld は drop-in のディレクトリには触らない。

```ini
# このファイルは mcctld が生成する。編集しないこと。調整は mcctl-<name>.container.d/*.conf で行う。
[Unit]
Description=mcctl server <name>

[Container]
ContainerName=mcctl-<name>
Image=docker.io/itzg/minecraft-server:<imageTag>
Pull=never                         # pull は mcctld が先に済ませる
Environment=EULA=TRUE VERSION=... TYPE=... MEMORY=... UID=0 GID=0 <env...>
Label=mcctl.managed=true mcctl.server=<name>
Volume=/var/lib/mcctl/servers/<name>:/data:Z
PublishPort=<port>:25565/tcp
HealthCmd=mc-health                # OCI イメージの HEALTHCHECK は podman が読まないので明示する
HealthInterval=30s
HealthStartPeriod=120s
HealthRetries=2
Notify=healthy
StopTimeout=60
LogDriver=journald
PodmanArgs=--memory=<上限>

[Service]
Restart=on-failure
RestartSec=5
RestartSteps=5
RestartMaxDelaySec=5min
TimeoutStartSec=900
TimeoutStopSec=90

[Install]                          # desiredState=running のときだけ出力する
WantedBy=default.target
```

- `UID=0` / `GID=0`: rootless ではコンテナ内の root がホストの mcctl ユーザーに対応する。データディレクトリの中身は mcctl の持ち物になり、ホストからは `sudo -u mcctl` で編集できる（#2 で確認済み）。
- `:Z`: SELinux 環境では、これがないと `/data` に書き込めない（#2）。
- `[Install]` の有無で、ホストの再起動後に自動で起動するかどうかが desiredState と一致する。
- `LogDriver=journald`: quadlet のコンテナは `--rm` で動くため、再起動や停止のたびにコンテナのログが消える。journald に流すことで、クラッシュ前のログ、systemd の終了理由と再起動の記録、新しいコンテナの起動ログが、1本の journal に時系列で残る（#2 で確認済み）。
- 各値（タイムアウト、バックオフ）は初期値。運用しながら調整する。

## 操作の流れ
すべての変更系の操作はサーバーごとのロックを取る。操作中に同じサーバーへ変更系の操作が来たら 409 を返す。別のサーバーへの操作は並行して実行できる。

| 操作 | 同期部分（応答まで） | 非同期部分 |
| --- | --- | --- |
| create | 検証（名前、ポートの重複、EULA）→ Store に登録 → データディレクトリを作成 → 201 | – |
| up | Store の desiredState を running にする → 202 | イメージがなければ（または `--pull` 指定時）互換 API で pull → `.container` を生成（`[Install]` あり）→ daemon-reload → StartUnit |
| down | 状態が starting なら 409（`--force` で強行）→ Store の desiredState を stopped にする → 202 | `.container` を生成（`[Install]` なし）→ daemon-reload → StopUnit（ジョブの完了を待つ） |
| rm | desiredState が running か unit が動いていれば 409 → `.container` を削除 → daemon-reload → Store から削除。`--purge` ならデータディレクトリと drop-in も削除 → 204 | – |

- 起動処理の途中の SIGTERM は itzg／マイクラが無視し、60 秒後に SIGKILL される（#2）。ワールドの生成中に壊すおそれがあるので、down は既定で断る。
- pull の失敗は、最後の操作のエラーとして status に出る。unit の起動時間には pull の時間が含まれない。

### 実際の状態（status）
systemd の unit の状態を読み替える。あわせて health（互換 API の inspect）と NRestarts を表示する。

| unit（ActiveState / SubState） | 表示 |
| --- | --- |
| inactive | `stopped` |
| activating / start | `starting` |
| active / running | `running` |
| deactivating | `stopping` |
| activating / auto-restart | `restarting` |
| failed | `failed` |

### 失敗の通知
- サーバーごとに「最後の操作（種類、開始時刻と終了時刻、結果、エラー）」をデーモンのメモリに持ち、`status` に表示する。
- 永続化はしない（Store には求める状態だけを保存するため）。
- unit が `failed` になった理由は journal で確認する（`mcctl logs`）。

### ログ
- mcctld は `journalctl --user -u mcctl-<name>.service -o json -n N` を実行して読み、必要な項目だけ返す。sdjournal（cgo と libsystemd が必要）は使わない。
- mcctl はシステムユーザー（UID < 1000）なので、journald は専用の journal を作らず `system.journal` に入れる。そのため **mcctl を `systemd-journal` グループに入れる**（#2 で確認済み）。代わりに mcctl はシステム全体のログを読めるようになるが、mcctld は自分の unit のログだけを返す。
- journal が揮発性（`/var/log/journal` がない）だと、ホストの再起動でログが消える。永続化を推奨する（AlmaLinux 10 の既定は揮発性だった）。

### 起動時同期
ホストの再起動後の自動起動とクラッシュからの復旧は systemd が担うので、起動時同期は最小限にする。
- Store のすべてのサーバーについて `.container` を生成し直し、daemon-reload する（手で編集された内容や古い形式の内容を直す）。
- desiredState が running なのに unit が inactive なら StartUnit する（ファイルを書いた直後にデーモンが落ちた場合など）。`failed` はそのままにして status に出す。
- mcctld が生成したものなのに Store にない `.container` ファイルは、ログに警告を出すだけで触らない。
- 動いている間に定期的な reconcile はしない。

### マイクラのコマンド実行（MVP の後）
- 互換 API の exec で、コンテナ内の `rcon-cli` を実行する。RCON のポートは外に出さない（#2 で動作を確認済み）。

## CLI
### MVP
| コマンド | 動作 | 応答 |
| --- | --- | --- |
| `mcctl create <name> --port N --accept-eula [...]` | Store に登録し、データディレクトリを作る | 同期 |
| `mcctl up <name> [--pull]` | desiredState を running にして起動する | 非同期 |
| `mcctl down <name> [--force]` | desiredState を stopped にして停止する。起動処理の途中なら `--force` が必要 | 非同期 |
| `mcctl rm <name> [--purge]` | Store から削除する。`--purge` でデータと drop-in も削除する | 同期 |
| `mcctl list [-o json]` | 一覧を表示する | 同期 |
| `mcctl status <name> [-o json]` | 詳細（求める状態、実際の状態、health、再起動回数、最後の操作）を表示する | 同期 |
| `mcctl logs <name> [--tail N]` | サーバーのログ（journal）の末尾を表示する（既定 200 件）。停止中でも過去のログを読める | 同期 |

全コマンド共通: `--socket` でソケットのパスを指定できる。環境変数 `MCCTL_SOCKET` でも可。

出力イメージ:
```
$ mcctl list
NAME      TYPE     VERSION  PORT   DESIRED  ACTUAL
survival  PAPER    1.21.4   25565  running  running
creative  VANILLA  1.21.4   25566  stopped  stopped
old       VANILLA  1.12.2   25567  running  starting

$ mcctl status old
Name:      old
Desired:   running
Actual:    starting (health: starting)
Restarts:  0
Image:     itzg/minecraft-server:java8
Last op:   up  2026-09-27 14:02  ok
```

### MVP の後
- `set`: 設定の変更。Store だけを書き換え、次の up で反映する。status には「未反映の変更あり」と表示する。
- `exec`: rcon-cli でマイクラのコマンドを実行する。
- `restart`: down してから up する。
- `logs -f`: 新しいログを流し続ける。当面は `sudo journalctl -f _SYSTEMD_USER_UNIT=mcctl-<name>.service` で代用する。
- `mcup` / `mcdown` という別名（遊び）。

### スコープ外
- バックアップ機能（当面はホストのディレクトリを rsync する）
- 統合版（Bedrock）
- リモート API（TCP＋認証）
- Web UI
- docker と rootful podman への対応

## API 草案（`/v1`）
| メソッド | パス | 対応するコマンド | 成功時 |
| --- | --- | --- | --- |
| GET | `/v1/servers` | list | 200 |
| POST | `/v1/servers` | create | 201 |
| GET | `/v1/servers/{name}` | status | 200 |
| DELETE | `/v1/servers/{name}?purge=bool` | rm | 204 |
| POST | `/v1/servers/{name}/up?pull=bool` | up | 202 |
| POST | `/v1/servers/{name}/down?force=bool` | down | 202 |
| GET | `/v1/servers/{name}/logs?tail=N` | logs | 200 |

エラー形式: `{"error": {"code": "conflict", "message": "..."}}`
主なステータス: 400（検証エラー）、403（認可）、404、409（操作中、ポートの重複、実行中のサーバーを rm しようとした、起動処理の途中で down しようとした）

## テスト
- 外部とのやり取りは小さなインターフェースに切り出す（quadlet ファイルの書き込み、systemd、podman の互換 API、journal）。デーモンのロジックと API はフェイクを使ってユニットテストする。
- `.container` ファイルの生成は、ゴールデンファイルでテストする。
- 実際の podman と systemd を使うテストは `//go:build integration` を付け、Linux の VM で手動で実行する。

## 実装時に確認するリスク
- **メモリ上限**: quadlet の専用キーがあればそれを使い、なければ `PodmanArgs=--memory` を使う。
- **起動の初回**: サーバー本体のダウンロードとワールドの生成を含めて `TimeoutStartSec=900` に収まるか。Mod を多く入れたサーバーでは足りない可能性がある。

## セットアップ（概要。詳細は #11）
1. `useradd --system --create-home --home-dir /var/lib/mcctl mcctl` と、subuid / subgid の付与
2. `usermod -aG systemd-journal mcctl`（ユーザー systemd が起動する前に行う。後から行う場合は `user@<uid>.service` を再起動する）
3. `loginctl enable-linger mcctl`
4. （推奨）`mkdir -p /var/log/journal` で journal を永続化する
5. `systemctl --user -M mcctl@ enable --now podman.socket`
6. tmpfiles.d で `/run/mcctl` を作る
7. `systemctl --user -M mcctl@ enable --now mcctld`
8. 操作するユーザーを `mcctl` グループに入れる

## リポジトリ構成
| パス | 役割 |
| --- | --- |
| `cmd/mcctl` | CLI（cobra）。生成したクライアントで API を叩くだけ |
| `cmd/mcctld` | デーモンのエントリポイント。フラグの解釈と各パッケージの組み立て |
| `api/openapi.yaml` | API 仕様（正） |
| `internal/api` | oapi-codegen の生成コード（サーバー I/F とクライアント） |
| `internal/daemon` | 中核のロジック（操作、ロック、最後の操作、起動時同期） |
| `internal/store` | Store（JSON ファイルの実装） |
| `internal/engine` | quadlet ファイルの生成、systemd（D-Bus）、podman の互換 API、journal。インターフェースとフェイク |

依存の向き: `cmd/mcctld` → `internal/api`（ハンドラ）→ `internal/daemon` → `internal/store`, `internal/engine`
