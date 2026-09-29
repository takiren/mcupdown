# はまりどころ

rootless podman ＋ quadlet で mcctl を動かすときのはまりどころ。多くは `scripts/setup.sh` が対処する。
根拠は #2 の検証（AlmaLinux 10.2 / podman 5.8.2 / SELinux Enforcing / cgroup v2）。

以下、サービス用のユーザーを `mcctl`（uid は環境によって違うので `<uid>` と書く。`id -u mcctl` で確認できる）とする。

## セットアップ

### システムユーザーに subuid / subgid が付かない
- **症状**: rootless podman でイメージの pull やコンテナの起動に失敗する（`potentially insufficient UIDs or GIDs available in user namespace` など）。
- **原因**: `useradd --system` で作ったユーザーには、`/etc/subuid` と `/etc/subgid` の範囲が自動で割り当てられない。
- **対処**: 既存の範囲と重ならないように付与する（setup.sh が行う）。
  ```sh
  usermod --add-subuids 1000000-1065535 --add-subgids 1000000-1065535 mcctl
  ```

### linger がないとコンテナが動かない
- **症状**: ログアウトやセッションの終了で、動いていたサーバーが落ちる。ワールドは保存されない。linger を外した場合も同じ。
- **原因**: rootless podman はユーザー systemd（`user@<uid>.service`）に依存している。linger がないと、ログインしていない間はユーザー systemd が動かない。自前で `podman system service` を起動しても、コンテナの起動に失敗する（`crun: sd-bus call: Interactive authentication required`）。healthcheck の timer も作れない。
- **対処**: `loginctl enable-linger mcctl`（setup.sh が行う）。linger は必須。

### `sudo -u mcctl podman ...` がうまく動かない
- **症状**: `sudo -u mcctl podman ps` などが、ランタイムディレクトリや D-Bus のエラーで失敗する。環境によっては `sudo -u` 自体がパスワードを求める。
- **原因**: rootless podman と `systemctl --user` には、`XDG_RUNTIME_DIR` と `DBUS_SESSION_BUS_ADDRESS` が必要。`sudo` はこれらを設定しない。
- **対処**: `runuser` で環境変数を付けて実行する。
  ```sh
  sudo runuser -u mcctl -- env XDG_RUNTIME_DIR=/run/user/<uid> \
    DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus \
    bash -c 'cd ~ && podman ps && systemctl --user status'
  ```
  `cd ~` は、root のカレントディレクトリに mcctl がアクセスできない場合の警告を避けるため。
  `systemctl --user -M mcctl@` でも操作できるが、下の「`systemctl --user -M user@` がセッションを残す」を参照。

### `systemctl --user -M user@` がセッションを残す
- **症状**: ユーザーを消して同じ UID で作り直すと、linger を有効にしてもユーザー systemd が起動しない。`loginctl list-users` にユーザーが `closing` のまま残っている。`systemctl --user -M user@` がときどき `Remote peer disconnected` で失敗する。
- **原因**: AlmaLinux 10.2（systemd 257）では、`systemctl --user -M user@` が呼び出すたびに logind のセッション（`Class=background`）を作り、それが `closing` のまま残る。`loginctl terminate-user` や `terminate-session` でも消えない。
- **対処**: スクリプトでは `-M` を使わず、`runuser` でユーザーのバスに直接つなぐ（setup.sh はそうしている）。残ってしまったセッションは `systemctl restart systemd-logind` で片付く。uninstall.sh は、片付かなかったときに警告を出す。

## コンテナ

### podman がイメージの HEALTHCHECK を読まない
- **症状**: itzg のイメージには HEALTHCHECK があるはずなのに、コンテナの health が出ない（互換 API でイメージを inspect すると `Healthcheck: null`）。
- **原因**: itzg/minecraft-server は OCI 形式で配布されている。podman は OCI 形式のイメージの HEALTHCHECK を読まない（レジストリ上のイメージの config には `mc-health` が入っている）。
- **対処**: `.container` に `HealthCmd=mc-health` などを明示する（mcctld が生成する）。

### SELinux で `/data` に書き込めない
- **症状**: コンテナが `Permission denied` で `/data/eula.txt` を書けずに落ち、再起動を繰り返す。ホスト側のファイルの持ち主は正しい。
- **原因**: SELinux が Enforcing のとき、ホストのディレクトリには `container_file_t` のラベルが必要。
- **対処**: バインドマウントに `:Z` を付ける（`Volume=...:/data:Z`。mcctld が生成する）。SELinux を無効にする必要はない。

### データディレクトリのファイルの持ち主がおかしい
- **症状**: ホスト上で、ワールドのファイルが見知らぬ UID の持ち物になり、mcctl ユーザーから編集できない。
- **原因**: itzg のイメージは既定でコンテナ内の UID 1000 として動く。rootless では、それが subuid の範囲の UID に対応する。
- **対処**: `UID=0` / `GID=0` を渡す（mcctld が行う）。rootless では、コンテナ内の root がホストの mcctl ユーザーに対応する。

### 起動処理の途中で止めると、60 秒待たされて強制終了される
- **症状**: 起動直後に停止すると、60 秒かかって終了コード 137 で止まる。ワールドの生成中なら壊れるおそれがある。
- **原因**: 起動処理の途中の SIGTERM を、itzg／マイクラが無視する。猶予（60 秒）が過ぎると SIGKILL される。healthy になった後なら数秒で、ワールドを保存して止まる。
- **対処**: mcctl は、起動処理の途中（starting）の down を 409 で断る。どうしても止めたいときは `--force` を付ける。

### 互換 API で作ったコンテナは、ホストのシャットダウン時に保存されない
- **症状**: ホストを再起動すると、ワールドの最後の変更が失われている（latest.log に停止処理が残っていない）。
- **原因**: 互換 API（`docker run` 相当）で直接作ったコンテナは、systemd の停止順序に組み込まれない。
- **対処**: mcctl は quadlet（systemd のユニット）でコンテナを動かす。quadlet なら、シャットダウン時に正常に停止する。手でコンテナを作らないこと。

### restart policy の `on-failure` が間隔を空けずに再起動を繰り返す
- **症状**: 起動に失敗し続けるコンテナが、数十秒で何十回も再起動する。
- **原因**: podman の restart policy には、再起動の間隔を延ばす仕組み（バックオフ）がない。
- **対処**: mcctl はコンテナの restart policy を使わず、systemd の `Restart=on-failure` と `RestartSteps` / `RestartMaxDelaySec` で間隔を延ばす。

## ログ

### mcctl ユーザーが自分のログを読めない
- **症状**: `journalctl --user` が `No journal files were opened due to insufficient permissions.` になる。`LogDriver=journald` のコンテナで、互換 API の logs が空になる。
- **原因**: journald は UID が 1000 未満のユーザー（システムユーザー）には専用の journal を作らず、`system.journal` に書く。journal を永続化しても変わらない。
- **対処**: mcctl を `systemd-journal` グループに入れる（setup.sh が行う）。**グループを付けた時点でユーザー systemd が既に動いていた場合は、`systemctl restart user@<uid>.service` が必要**（動いているサーバーは止まる）。setup.sh は linger より前にグループを付けるので、新規のセットアップでは不要。

### ホストを再起動するとログが消える
- **症状**: 再起動の前に落ちたサーバーの原因を、`mcctl logs` で追えない。
- **原因**: `/var/log/journal` がないと、journal は揮発性（`/run/log/journal`）になる。AlmaLinux 10 の既定はこの状態だった。
- **対処**: `/var/log/journal` を作って journald を再起動する（setup.sh が行う。`--no-persistent-journal` で無効にできる）。

## デバッグ

### podman の API を直接叩く
- 互換 API（Docker 互換）: `sudo curl --unix-socket /run/user/<uid>/podman/podman.sock http://d/containers/json`
- libpod API にはバージョンの接頭辞が要る: `http://d/v5.0.0/libpod/containers/json`。接頭辞がないと JSON ではない応答が返る。

### サーバーのログを追いかける
`mcctl logs` は末尾を表示するだけで、追いかける機能（`-f`）はない。当面は journal を直接見る。
```sh
sudo journalctl -f _SYSTEMD_USER_UNIT=mcctl-<name>.service
```
