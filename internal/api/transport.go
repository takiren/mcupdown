package api

import (
	"context"
	"net"
	"net/http"
)

// BasePath は API のパスの接頭辞。api/openapi.yaml の servers と合わせる。
const BasePath = "/v1"

// NewHandler は StrictServerInterface の実装を BasePath の下にマウントした http.Handler を返す。
func NewHandler(si StrictServerInterface) http.Handler {
	return HandlerWithOptions(NewStrictHandler(si, nil), StdHTTPServerOptions{
		BaseURL: BasePath,
	})
}

// NewUnixSocketClient は Unix ソケット上の mcctld に接続するクライアントを返す。
func NewUnixSocketClient(socketPath string) (*ClientWithResponses, error) {
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	// ホスト名は使われないが、URL として有効である必要がある。
	return NewClientWithResponses("http://mcctld"+BasePath, WithHTTPClient(hc))
}
