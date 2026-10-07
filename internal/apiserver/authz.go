package apiserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/user"
	"slices"
	"strconv"

	"github.com/takiren/mcupdown/internal/api"
)

// Authorizer は Unix ソケットの接続元の UID で、変更系の操作を許すかを決める。
//
// 参照系（GET）は誰でも呼べる。変更系は root、mcctld 自身の UID、操作用のグループのメンバーだけ。
// 接続元の UID を取れない OS（macOS）では認可を行わない（開発用）。
type Authorizer struct {
	// SelfUID は mcctld 自身の UID。
	SelfUID uint32
	// Group は操作用のグループ名（既定 mcctl）。
	Group string
	// MemberOf は uid のユーザーが group に属するかを返す。nil なら os/user で調べる。テストで差し替える。
	MemberOf func(uid uint32, group string) (bool, error)
}

type peerKey struct{}

// peer は接続元の情報。ok が false なら UID を取れなかった。
type peer struct {
	uid uint32
	ok  bool
}

// ConnContext は http.Server.ConnContext に設定する。接続元の UID を context に入れる。
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	uid, ok := peerUID(c)
	return context.WithValue(ctx, peerKey{}, peer{uid: uid, ok: ok})
}

// WithPeerUID は uid を接続元として context に入れる。テスト用。
func WithPeerUID(ctx context.Context, uid uint32) context.Context {
	return context.WithValue(ctx, peerKey{}, peer{uid: uid, ok: true})
}

// Middleware は変更系のリクエストを認可する。
func (a *Authorizer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		p, _ := r.Context().Value(peerKey{}).(peer)
		if !p.ok {
			if peerCredSupported {
				// 取れるはずの UID が取れないときは、安全側に倒して断る。
				api.WriteError(w, http.StatusForbidden, api.ErrorCodeForbidden, "could not identify the caller")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := a.allowed(p.uid)
		if err != nil {
			slog.Warn("failed to check group membership", "uid", p.uid, "group", a.Group, "error", err)
		}
		if !allowed {
			api.WriteError(w, http.StatusForbidden, api.ErrorCodeForbidden,
				fmt.Sprintf("uid %d is not a member of group %s", p.uid, a.Group))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authorizer) allowed(uid uint32) (bool, error) {
	if uid == 0 || uid == a.SelfUID {
		return true, nil
	}
	memberOf := a.MemberOf
	if memberOf == nil {
		memberOf = memberOfGroup
	}
	return memberOf(uid, a.Group)
}

// memberOfGroup は /etc/passwd と /etc/group（または NSS）で、uid が group に属するかを調べる。
// 主グループも所属として扱う。
func memberOfGroup(uid uint32, group string) (bool, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return false, err
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return false, err
	}
	if u.Gid == g.Gid {
		return true, nil
	}
	gids, err := u.GroupIds()
	if err != nil {
		return false, err
	}
	return slices.Contains(gids, g.Gid), nil
}
