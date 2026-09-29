#!/usr/bin/env bash
# scripts/setup.sh で行ったセットアップを元に戻す。
#
# 動いているサーバーはすべて止まる。ホームディレクトリ（ワールドのデータを含む）は --purge を付けたときだけ消す。
# journal の永続化（/var/log/journal）は、他の用途にも影響するので元に戻さない。
set -euo pipefail

usage() {
	cat <<'EOF'
使い方: sudo scripts/uninstall.sh [オプション]

オプション:
  --user NAME   サービス用のユーザー名（既定: mcctl。環境変数 MCCTL_USER でも指定できる）
  --purge       ホームディレクトリ（/var/lib/NAME。ワールドのデータ、コンテナのイメージを含む）も消す
  --yes         確認を省略する
  -h, --help    このヘルプを表示する
EOF
}

SVC_USER=${MCCTL_USER:-mcctl}
PURGE=0
ASSUME_YES=0

while (($# > 0)); do
	case $1 in
	--user)
		SVC_USER=${2:?--user には値が必要}
		shift 2
		;;
	--purge)
		PURGE=1
		shift
		;;
	--yes)
		ASSUME_YES=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "不明なオプション: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

step() { printf '\n==> %s\n' "$*"; }
changed() { printf '    [changed] %s\n' "$*"; }
skip() { printf '    [skip]    %s\n' "$*"; }
die() {
	printf 'エラー: %s\n' "$*" >&2
	exit 1
}

((EUID == 0)) || die "root で実行すること（sudo scripts/uninstall.sh）"
id "$SVC_USER" >/dev/null 2>&1 || die "ユーザー $SVC_USER はいない"

UID_=$(id -u "$SVC_USER")
HOME_=$(getent passwd "$SVC_USER" | cut -d: -f6)
TMPFILES_CONF=/etc/tmpfiles.d/$SVC_USER.conf

echo "ユーザー $SVC_USER（uid $UID_）のセットアップを元に戻す。動いているサーバーはすべて止まる。"
if ((PURGE)); then
	echo "--purge: $HOME_ も消す（ワールドのデータを含む。元に戻せない）"
fi
if ((!ASSUME_YES)); then
	if ((PURGE)); then
		read -r -p "続けるならユーザー名（$SVC_USER）を入力: " answer
		[[ $answer == "$SVC_USER" ]] || die "中止した"
	else
		read -r -p "続けますか？ [y/N]: " answer
		[[ $answer == [yY] ]] || die "中止した"
	fi
fi

if ((PURGE)) && systemctl is-active --quiet "user@$UID_.service"; then
	step "podman のストレージを消す"
	# コンテナのストレージには subuid の持ち物のファイルがあるので、podman 自身に消させる。
	# $HOME はサービス用ユーザー側のシェルで展開させる。
	# shellcheck disable=SC2016
	runuser -u "$SVC_USER" -- env XDG_RUNTIME_DIR="/run/user/$UID_" HOME="$HOME_" \
		bash -c 'cd "$HOME" && podman system reset --force' && changed "podman system reset した"
fi

step "linger とユーザー systemd"
if [[ -e /var/lib/systemd/linger/$SVC_USER ]]; then
	loginctl disable-linger "$SVC_USER"
	changed "linger を無効にした"
else
	skip "linger は無効"
fi
if systemctl is-active --quiet "user@$UID_.service"; then
	systemctl stop "user@$UID_.service"
	changed "user@$UID_.service を止めた"
fi
# セッションが closing のまま残っていると、logind がユーザーを覚えたままになり、
# 同じ UID で作り直したときに linger でユーザー systemd が起動しなくなる。
if loginctl show-user "$UID_" >/dev/null 2>&1; then
	loginctl terminate-user "$UID_" || true
	for _ in $(seq 1 10); do
		loginctl show-user "$UID_" >/dev/null 2>&1 || break
		sleep 1
	done
	if loginctl show-user "$UID_" >/dev/null 2>&1; then
		printf '    [warn]    logind がまだユーザー %s を覚えている。同じ UID で作り直す前に systemctl restart systemd-logind で片付けること\n' "$SVC_USER" >&2
	else
		changed "logind のセッションを終了した"
	fi
fi
for _ in $(seq 1 30); do
	pgrep -u "$SVC_USER" >/dev/null || break
	sleep 1
done
pgrep -u "$SVC_USER" >/dev/null && die "$SVC_USER のプロセスが残っている（pgrep -au $SVC_USER で確認すること）"

step "tmpfiles.d"
if [[ -f $TMPFILES_CONF ]]; then
	rm -f "$TMPFILES_CONF"
	changed "$TMPFILES_CONF を消した"
else
	skip "$TMPFILES_CONF はない"
fi
if [[ -d /run/$SVC_USER ]]; then
	rm -rf "/run/$SVC_USER"
	changed "/run/$SVC_USER を消した"
fi

step "subuid / subgid"
for f in /etc/subuid /etc/subgid; do
	if grep -q "^$SVC_USER:" "$f" 2>/dev/null; then
		sed -i "/^$SVC_USER:/d" "$f"
		changed "$f から消した"
	else
		skip "$f に行はない"
	fi
done

step "ユーザーとグループ"
if ((PURGE)); then
	userdel -r "$SVC_USER" 2>/dev/null || { userdel "$SVC_USER" && rm -rf "$HOME_"; }
	changed "ユーザーと $HOME_ を消した"
else
	userdel "$SVC_USER"
	changed "ユーザーを消した（$HOME_ は残した）"
fi
# 操作するユーザーがメンバーに残っていると、userdel はグループを消さない。
if getent group "$SVC_USER" >/dev/null; then
	groupdel "$SVC_USER"
	changed "グループ $SVC_USER を消した"
fi

printf '\n完了\n'
