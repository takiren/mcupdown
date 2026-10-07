package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
)

// BasePath は API のパスの接頭辞。api/openapi.yaml の servers と合わせる。
const BasePath = "/v1"

// NewHandler は StrictServerInterface の実装を BasePath の下にマウントした http.Handler を返す。
// リクエストの解釈に失敗したとき（JSON やクエリパラメータが不正など）も、
// 他のエラーと同じ {"error": {"code", "message"}} の形で返す。
func NewHandler(si StrictServerInterface) http.Handler {
	badRequest := func(w http.ResponseWriter, _ *http.Request, err error) {
		WriteError(w, http.StatusBadRequest, ErrorCodeInvalidArgument, err.Error())
	}
	strict := NewStrictHandlerWithOptions(si, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest,
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			WriteError(w, http.StatusInternalServerError, ErrorCodeInternal, "internal error")
		},
	})
	return HandlerWithOptions(strict, StdHTTPServerOptions{
		BaseURL:          BasePath,
		ErrorHandlerFunc: badRequest,
	})
}

// WriteError はエラーを {"error": {"code", "message"}} の形で書き出す。
func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Error: ErrorBody{Code: code, Message: message}})
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
