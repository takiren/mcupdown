# 検証用マシンの構築（Ansible）

実際の podman と systemd で mcctl を確かめるための「検証用マシン」を、何度でも同じ状態に作るための playbook。
mcctl 固有のセットアップ（ユーザー、subuid、linger、journal など）は [`scripts/setup.sh`](../../scripts/setup.sh) に一本化していて、ここではそれを転送して実行するだけにしている。

## 前提
- 対象: AlmaLinux 10.x（検証済み: 10.2）。それ以外は `-e mcctl_allow_unsupported_os=true` で続けられるが、確かめていない。
- 対象に SSH でき、パスワードなしで root になれる sudo があること。
- 手元に ansible-core（2.21 で確認）と Go（バイナリとテストのクロスビルドに使う）があること。

## 準備
```sh
cd deploy/ansible
cp inventory.example.yml inventory.yml   # 接続先を書く（inventory.yml はコミットしない）
```

以降のコマンドはすべて `deploy/ansible` で実行する（`ansible.cfg` を読ませるため）。

## playbook
| playbook | 内容 |
| --- | --- |
| `site.yml` | 以下の base → deploy → setup → images をまとめて実行する。まずはこれを流す |
| `base.yml` | OS の確認、podman などのパッケージ。SELinux は Enforcing のまま |
| `deploy.yml` | 手元で `mcctl` / `mcctld` を linux 向けにクロスビルドし、`/usr/local/bin` に配置する。中身が変わったときだけ mcctld を再起動する（ユニットがある場合） |
| `setup.yml` | `scripts/setup.sh` を実行する。setup.sh が `[changed]` を出したときだけ changed になる |
| `images.yml` | itzg のイメージを、サービス用ユーザーとして事前に pull する |
| `integration.yml` | 統合テスト（`//go:build integration`）を対象マシンで実行する |
| `cleanup.yml` | 検証用のサーバーを片付ける。明示したときだけ `scripts/uninstall.sh` で完全に元に戻す |

```sh
ansible-playbook site.yml
ansible-playbook site.yml -e mcctl_user=mcctlci        # 別のユーザー名で作る（まっさらな状態からの確認用）
ansible-playbook deploy.yml                            # バイナリだけ入れ替える
```

主な変数は [`group_vars/all.yml`](group_vars/all.yml) を参照。よく使うもの:

| 変数 | 既定 | 内容 |
| --- | --- | --- |
| `mcctl_user` | `mcctl` | サービス用ユーザー（setup.sh の `--user`） |
| `mcctl_operators` | `[]` | mcctl グループに入れるユーザー（`--add-operator`） |
| `mcctl_persistent_journal` | `true` | `false` で `--no-persistent-journal` |
| `mcctl_mcctld_unit` | `""` | mcctld のユーザーユニットのファイル（手元のパス、#11）。指定すると setup.sh の `--mcctld-unit` で配置して有効にする |
| `mcctl_deploy_binaries` | `true` | `false` でバイナリを配置しない |

## 統合テスト
手元で `GOOS=linux go test -c -tags integration` でテストバイナリを作り、対象マシンに転送して、サービス用ユーザー（linger、systemd-journal グループ、`XDG_RUNTIME_DIR`、`DBUS_SESSION_BUS_ADDRESS`、`HOME` を設定）として実行する。`go test` と同じく、パッケージのディレクトリ（`testdata` も転送する）をカレントにして実行する。

```sh
ansible-playbook integration.yml                                       # 既定: ./internal/engine
ansible-playbook integration.yml -e mcctl_it_run=TestSystemdUserIntegration
ansible-playbook integration.yml -e '{"mcctl_it_packages": ["./internal/engine", "./internal/daemon"]}'
ansible-playbook integration.yml -e mcctl_it_repo_dir=/path/to/other/worktree   # 別のブランチを試す
```

- テストの出力は playbook の出力にそのまま表示される。失敗したパッケージがあれば最後に失敗する。
- テストバイナリは最初のホストのアーキテクチャでビルドする（アーキテクチャの違うホストを混ぜない）。
- 統合テストは名前が `e2e-` で始まるサーバーと、ポート 25570〜25579 を使う約束にしている。途中で止まって残ったものは `cleanup.yml` で片付けられる。

## 片付け
```sh
ansible-playbook cleanup.yml                  # 名前が e2e- で始まる検証用サーバーを片付ける
```

既定では、名前が `mcctl_cleanup_prefix`（`e2e-`）で始まるものだけを対象にする。
- unit を止め、`.container` ファイル、drop-in（`.container.d`）、統合テストが置くユーザーユニット（`mcctl-e2e-*.service`）を消して daemon-reload する
- 残っているコンテナを消す
- データディレクトリ（`~/servers/e2e-*`）を消す（`-e mcctl_cleanup_data=false` で残す）

完全に元に戻す（`scripts/uninstall.sh`）には、確認のためにユーザー名を書く。

```sh
ansible-playbook cleanup.yml -e mcctl_uninstall=true -e mcctl_uninstall_confirm=mcctlci -e mcctl_user=mcctlci
# ワールドのデータとイメージを含むホームディレクトリも消す
ansible-playbook cleanup.yml -e mcctl_uninstall=true -e mcctl_uninstall_confirm=mcctlci -e mcctl_user=mcctlci -e mcctl_uninstall_purge=true
```

ユーザーを消して同じ UID で作り直したときに linger が効かない場合は、[トラブルシューティング](../../docs/troubleshooting.md) を参照（`systemctl restart systemd-logind`）。

## lint
```sh
pipx run --spec ansible-lint==26.9.0 ansible-lint -c deploy/ansible/.ansible-lint deploy/ansible   # リポジトリのルートで
```
CI でも同じバージョンで実行している。
