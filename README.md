# mcupdown

1台のホストで、少数のマイクラサーバー（Java 版）をコンテナで管理するツール。
k8s を使うほどではないが、手でコンテナを管理するのは面倒、という人向け。複数のホストで管理したいなら、他のサービスを使う方が楽だと思う。

> **開発中**。まだ動かない。MVP の進み具合は [マイルストーン「MVP」](https://github.com/takiren/mcupdown/milestone/1) を参照。

## 特徴
- サーバーは [itzg/minecraft-server](https://github.com/itzg/docker-minecraft-server) のコンテナで動かす。サーバー本体のダウンロードや Java のバージョンはイメージに任せる。
- rootless podman と quadlet（systemd）でコンテナを管理する。正常停止、クラッシュ時の再起動、ホストの再起動後の自動起動は systemd が担う。
- 常駐デーモン `mcctld` と、その API を叩くだけの CLI `mcctl` からなる。API は Unix ソケット上の HTTP なので、後から Web UI などを足しやすい。
- サーバーの一覧や状態は誰でも見られる。起動や停止などの操作は `mcctl` グループのメンバーだけができる。

## 動作環境
- Linux（systemd）
- rootless podman（docker と rootful podman には対応しない）
- 専用のシステムユーザー `mcctl`（linger を有効にする）

検証環境は AlmaLinux 10.2 / podman 5.8.2 / SELinux Enforcing。

## 使い方（予定）
```sh
mcctl create survival --port 25565 --version 1.21.4 --type PAPER --memory 4G --accept-eula
mcctl up survival          # 非同期で起動する
mcctl status survival      # starting → running
mcctl logs survival --tail 50
mcctl down survival        # ワールドを保存して停止する
mcctl rm survival          # 登録を消す（--purge でワールドも消す）
```

## ドキュメント
- [要件定義](docs/requirements.md): 設計と仕様の正。
- [API 仕様](api/openapi.yaml)
- 設計判断の根拠（podman と quadlet の検証結果）: [#2](https://github.com/takiren/mcupdown/issues/2)

## 開発
Go のバージョンは `go.mod` を参照。

```sh
go generate ./...        # api/openapi.yaml からコードを生成する
go build ./...
go test -race ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...
```

macOS ではユニットテストと CLI の開発だけができる。podman と systemd を使う確認は Linux のマシンで行う。

## ライセンス
[MIT](LICENSE)
