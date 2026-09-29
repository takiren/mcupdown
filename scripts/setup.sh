#!/usr/bin/env bash
# mcctl を動かすためのホストのセットアップ。
#
# 何度実行しても同じ結果になる（冪等）。各ステップで状態を確認し、必要なときだけ変更する。
# はまりどころの背景は docs/troubleshooting.md を参照。
set -euo pipefail

usage() {
	cat <<'EOF'
使い方: sudo scripts/setup.sh [オプション]

mcctl（rootless podman ＋ quadlet）を動かすためのシステムユーザーと、その周辺を準備する。

オプション:
  --user NAME            サービス用のユーザー名（既定: mcctl。環境変数 MCCTL_USER でも指定できる）
                         ホームは /var/lib/NAME、グループは NAME、ソケットのディレクトリは /run/NAME になる
  --add-operator USER    USER を NAME グループに入れ、mcctl で操作できるようにする（複数回指定できる）
  --no-persistent-journal
                         journal を永続化しない（既定では /var/log/journal を作る）
  --mcctld-unit PATH     mcctld のユーザーユニットのファイルを配置して有効にする（#11 で用意する予定）
  --subid-count N        subuid / subgid の範囲の大きさ（既定: 65536）
  --dry-run              変更せず、何をするかだけ表示する
  -h, --help             このヘルプを表示する
EOF
}

SVC_USER=${MCCTL_USER:-mcctl}
OPERATORS=()
PERSIST_JOURNAL=1
MCCTLD_UNIT=
SUBID_COUNT=65536
DRY_RUN=0

while (($# > 0)); do
	case $1 in
	--user)
		SVC_USER=${2:?--user には値が必要}
		shift 2
		;;
	--add-operator)
		OPERATORS+=("${2:?--add-operator には値が必要}")
		shift 2
		;;
	--no-persistent-journal)
		PERSIST_JOURNAL=0
		shift
		;;
	--mcctld-unit)
		MCCTLD_UNIT=${2:?--mcctld-unit には値が必要}
		shift 2
		;;
	--subid-count)
		SUBID_COUNT=${2:?--subid-count には値が必要}
		shift 2
		;;
	--dry-run)
		DRY_RUN=1
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

if ! [[ $SVC_USER =~ ^[a-z_][a-z0-9_-]{0,30}$ ]]; then
	echo "ユーザー名が不正: $SVC_USER" >&2
	exit 2
fi
if ! [[ $SUBID_COUNT =~ ^[1-9][0-9]*$ ]]; then
	echo "--subid-count は正の整数で指定すること: $SUBID_COUNT" >&2
	exit 2
fi

SVC_HOME=/var/lib/$SVC_USER
SOCK_DIR=/run/$SVC_USER
TMPFILES_CONF=/etc/tmpfiles.d/$SVC_USER.conf

# ---- 出力と実行の補助 ----

step() { printf '\n==> %s\n' "$*"; }
ok() { printf '    [ok]      %s\n' "$*"; }
changed() {
	((DRY_RUN)) && return 0
	printf '    [changed] %s\n' "$*"
}
skip() { printf '    [skip]    %s\n' "$*"; }
warn() { printf '    [warn]    %s\n' "$*" >&2; }
die() {
	printf 'エラー: %s\n' "$*" >&2
	exit 1
}

# 変更を伴うコマンドを実行する。--dry-run のときは表示だけする。
run() {
	if ((DRY_RUN)); then
		printf '    [dry-run] %s\n' "$*"
	else
		"$@"
	fi
}

# サービス用ユーザーの systemd --user を操作する。
# `systemctl --user -M user@` はときどき "Remote peer disconnected" で失敗したので、
# ユーザーのバスに直接つなぐ。
user_systemctl() {
	local uid
	uid=$(svc_uid) || die "ユーザー $SVC_USER がいない"
	runuser -u "$SVC_USER" -- env \
		XDG_RUNTIME_DIR="/run/user/$uid" \
		DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
		systemctl --user "$@"
}

svc_uid() { id -u "$SVC_USER" 2>/dev/null; }

# ---- 前提の確認 ----

preflight() {
	step "前提の確認"
	((EUID == 0)) || ((DRY_RUN)) || die "root で実行すること（sudo scripts/setup.sh）"
	command -v systemctl >/dev/null || die "systemd が見つからない"
	command -v loginctl >/dev/null || die "loginctl が見つからない"
	command -v podman >/dev/null || die "podman が見つからない"
	ok "$(podman --version)"
	local fs
	fs=$(stat -fc %T /sys/fs/cgroup 2>/dev/null || true)
	[[ $fs == cgroup2fs ]] || die "cgroup v2 ではない（/sys/fs/cgroup: ${fs:-不明}）"
	ok "cgroup v2"
	if command -v getenforce >/dev/null; then
		ok "SELinux: $(getenforce)（Enforcing のままでよい。バインドマウントには :Z を付ける）"
	fi
}

# ---- 各ステップ ----

ensure_user() {
	step "システムユーザー $SVC_USER"
	if id "$SVC_USER" >/dev/null 2>&1; then
		ok "既にある（uid $(svc_uid)、ホーム $(getent passwd "$SVC_USER" | cut -d: -f6)）"
		return
	fi
	local nologin
	nologin=$(command -v nologin || echo /sbin/nologin)
	run useradd --system --user-group --create-home --home-dir "$SVC_HOME" --shell "$nologin" "$SVC_USER"
	changed "作成した（ホーム $SVC_HOME）"
}

# /etc/subuid と /etc/subgid のどの範囲とも重ならない開始位置を返す。
next_free_subid() {
	local max_end=100000
	local f start count end
	for f in /etc/subuid /etc/subgid; do
		[[ -f $f ]] || continue
		while IFS=: read -r _ start count; do
			[[ $start =~ ^[0-9]+$ && $count =~ ^[0-9]+$ ]] || continue
			end=$((start + count))
			((end > max_end)) && max_end=$end
		done <"$f"
	done
	# 範囲の大きさの倍数に切り上げる（見やすさのため）
	echo $(((max_end + SUBID_COUNT - 1) / SUBID_COUNT * SUBID_COUNT))
}

ensure_subids() {
	step "subuid / subgid"
	# システムユーザーには useradd が自動で割り当てない。
	local has_uid=0 has_gid=0
	grep -q "^$SVC_USER:" /etc/subuid 2>/dev/null && has_uid=1
	grep -q "^$SVC_USER:" /etc/subgid 2>/dev/null && has_gid=1
	if ((has_uid && has_gid)); then
		ok "subuid: $(grep "^$SVC_USER:" /etc/subuid | cut -d: -f2-)、subgid: $(grep "^$SVC_USER:" /etc/subgid | cut -d: -f2-)"
		return
	fi
	local start end
	start=$(next_free_subid)
	end=$((start + SUBID_COUNT - 1))
	if ((!has_uid)); then
		run usermod --add-subuids "$start-$end" "$SVC_USER"
		changed "subuid $start-$end を付与した"
	fi
	if ((!has_gid)); then
		run usermod --add-subgids "$start-$end" "$SVC_USER"
		changed "subgid $start-$end を付与した"
	fi
}

ensure_journal_group() {
	step "systemd-journal グループ"
	# UID が 1000 未満のユーザーには専用の journal ができないので、自分のログを読むにはこのグループが要る。
	# ユーザー systemd の起動より前に入れておく（後から入れた場合は user@ の再起動が必要）。
	if id -nG "$SVC_USER" 2>/dev/null | tr ' ' '\n' | grep -qx systemd-journal; then
		ok "既にメンバー"
		return
	fi
	run usermod -aG systemd-journal "$SVC_USER"
	changed "追加した"
	local uid
	uid=$(svc_uid || true)
	if [[ -n $uid ]] && systemctl is-active --quiet "user@$uid.service"; then
		warn "user@$uid.service が既に動いているので、グループの追加はまだ反映されていない。"
		warn "反映するには: systemctl restart user@$uid.service（動いているサーバーは止まる）"
	fi
}

ensure_linger() {
	step "linger"
	# linger がないとユーザー systemd が止まり、コンテナは保存されないまま落ちる。
	if [[ -e /var/lib/systemd/linger/$SVC_USER ]]; then
		ok "有効"
	else
		run loginctl enable-linger "$SVC_USER"
		changed "有効にした"
	fi
	((DRY_RUN)) && return
	local uid
	uid=$(svc_uid)
	local i
	for i in $(seq 1 30); do
		if systemctl is-active --quiet "user@$uid.service" && [[ -S /run/user/$uid/bus ]]; then
			ok "user@$uid.service が動いている"
			return
		fi
		# logind に古いセッションが closing のまま残っていると、linger を有効にしても起動しないことがある。
		if ((i == 5)); then
			warn "user@$uid.service が起動しないので、明示的に起動する"
			systemctl start "user@$uid.service" || true
		fi
		sleep 1
	done
	die "user@$uid.service が起動しない（journalctl -u user@$uid.service を確認すること）"
}

ensure_persistent_journal() {
	step "journal の永続化"
	if ((!PERSIST_JOURNAL)); then
		skip "--no-persistent-journal が指定された（ホストを再起動するとログが消える）"
		return
	fi
	if [[ -d /var/log/journal ]]; then
		ok "/var/log/journal がある"
		return
	fi
	run mkdir -p /var/log/journal
	run systemd-tmpfiles --create --prefix /var/log/journal
	run systemctl restart systemd-journald
	run journalctl --flush
	changed "/var/log/journal を作り、journald を再起動した"
}

ensure_podman_socket() {
	step "podman.socket（ユーザーユニット）"
	if ((DRY_RUN)) && ! id "$SVC_USER" >/dev/null 2>&1; then
		run user_systemctl enable --now podman.socket
		return
	fi
	if [[ $(user_systemctl is-enabled podman.socket 2>/dev/null || true) == enabled ]] &&
		user_systemctl is-active --quiet podman.socket; then
		ok "有効で動いている"
		return
	fi
	run user_systemctl enable --now podman.socket
	changed "有効にして起動した"
}

ensure_tmpfiles() {
	step "ソケットのディレクトリ $SOCK_DIR（tmpfiles.d）"
	local want="d $SOCK_DIR 0755 $SVC_USER $SVC_USER -"
	if [[ -f $TMPFILES_CONF ]] && [[ $(<"$TMPFILES_CONF") == "$want" ]]; then
		ok "$TMPFILES_CONF は最新"
	else
		if ((DRY_RUN)); then
			printf '    [dry-run] %s に書き込む: %s\n' "$TMPFILES_CONF" "$want"
		else
			printf '%s\n' "$want" >"$TMPFILES_CONF"
		fi
		changed "$TMPFILES_CONF を書いた"
	fi
	if [[ -d $SOCK_DIR ]] && [[ $(stat -c %U "$SOCK_DIR") == "$SVC_USER" ]]; then
		ok "$SOCK_DIR がある"
	else
		run systemd-tmpfiles --create "$TMPFILES_CONF"
		changed "$SOCK_DIR を作った"
	fi
}

ensure_mcctld_unit() {
	step "mcctld のユーザーユニット"
	# TODO(#11): ユニットファイルとバイナリの配置は #11 で用意する。それまでは指定されたときだけ行う。
	if [[ -z $MCCTLD_UNIT ]]; then
		skip "--mcctld-unit が指定されていない（#11 で用意する予定）"
		return
	fi
	[[ -f $MCCTLD_UNIT ]] || die "ユニットファイルがない: $MCCTLD_UNIT"
	local dest=$SVC_HOME/.config/systemd/user/mcctld.service
	if [[ -f $dest ]] && cmp -s "$MCCTLD_UNIT" "$dest"; then
		ok "$dest は最新"
	else
		run install -D -m 0644 -o "$SVC_USER" -g "$SVC_USER" "$MCCTLD_UNIT" "$dest"
		run chown "$SVC_USER:$SVC_USER" "$SVC_HOME/.config" "$SVC_HOME/.config/systemd" "$SVC_HOME/.config/systemd/user"
		run user_systemctl daemon-reload
		changed "$dest を配置した"
	fi
	if [[ $(user_systemctl is-enabled mcctld.service 2>/dev/null || true) == enabled ]] &&
		user_systemctl is-active --quiet mcctld.service; then
		ok "mcctld.service は有効で動いている"
	else
		run user_systemctl enable --now mcctld.service
		changed "mcctld.service を有効にして起動した"
	fi
}

ensure_operators() {
	step "操作するユーザー（$SVC_USER グループ）"
	if ((${#OPERATORS[@]} == 0)); then
		skip "--add-operator が指定されていない"
		return
	fi
	local op
	for op in "${OPERATORS[@]}"; do
		if ! id "$op" >/dev/null 2>&1; then
			warn "$op というユーザーはいない"
			continue
		fi
		if id -nG "$op" | tr ' ' '\n' | grep -qx "$SVC_USER"; then
			ok "$op は既にメンバー"
		else
			run usermod -aG "$SVC_USER" "$op"
			changed "$op を追加した（反映には $op の再ログインが必要）"
		fi
	done
}

verify() {
	step "確認"
	if ((DRY_RUN)); then
		skip "--dry-run なので確認しない"
		return
	fi
	local uid state sock info
	uid=$(svc_uid)
	state=$(user_systemctl is-system-running 2>/dev/null || true)
	case $state in
	running) ok "ユーザー systemd: $state" ;;
	degraded) warn "ユーザー systemd: degraded（systemctl --user -M $SVC_USER@ --failed で確認すること）" ;;
	*) warn "ユーザー systemd: ${state:-不明}" ;;
	esac

	sock=/run/user/$uid/podman/podman.sock
	if [[ -S $sock ]]; then
		ok "podman のソケット: $sock"
		if command -v curl >/dev/null; then
			info=$(curl -sS --max-time 10 --unix-socket "$sock" http://d/info || true)
			if [[ $info == *'"name=rootless"'* ]]; then
				ok "podman は rootless で動いている"
			else
				warn "podman の /info から rootless と判定できなかった"
			fi
		else
			skip "curl がないので rootless の判定を飛ばした"
		fi
	else
		warn "podman のソケットがない: $sock"
	fi

	if [[ -d $SOCK_DIR ]]; then
		ok "ソケットのディレクトリ: $SOCK_DIR"
	else
		warn "ソケットのディレクトリがない: $SOCK_DIR"
	fi
	if [[ -S $SOCK_DIR/mcctld.sock ]]; then
		ok "mcctld のソケット: $SOCK_DIR/mcctld.sock"
	else
		skip "mcctld のソケットはまだない（mcctld を配置していない場合は正常）"
	fi
}

main() {
	((DRY_RUN)) && echo "--dry-run: 変更はしない"
	echo "サービス用ユーザー: $SVC_USER（ホーム $SVC_HOME）"
	preflight
	ensure_user
	ensure_subids
	ensure_journal_group
	ensure_persistent_journal
	ensure_linger
	ensure_podman_socket
	ensure_tmpfiles
	ensure_mcctld_unit
	ensure_operators
	verify
	printf '\n完了\n'
}

main
