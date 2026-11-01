// Package apiserver は mcctld の HTTP API のサーバー側。
//
// api.StrictServerInterface を daemon の上に実装し、Unix ソケットの接続元による認可を行う。
// internal/api は CLI（mcctl）も使うので、daemon や engine に依存するこのコードは別のパッケージに置く。
package apiserver

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"

	"github.com/takiren/mcupdown/internal/api"
	"github.com/takiren/mcupdown/internal/daemon"
	"github.com/takiren/mcupdown/internal/engine"
)

// Server は api.StrictServerInterface の実装。
type Server struct {
	d *daemon.Daemon
}

var _ api.StrictServerInterface = (*Server)(nil)

// New は daemon を呼ぶ Server を返す。
func New(d *daemon.Daemon) *Server {
	return &Server{d: d}
}

// NewHandler は認可付きの http.Handler を返す。http.Server の ConnContext には ConnContext を設定すること。
func NewHandler(d *daemon.Daemon, authz *Authorizer) http.Handler {
	return authz.Middleware(api.NewHandler(New(d)))
}

func (s *Server) ListServers(ctx context.Context, _ api.ListServersRequestObject) (api.ListServersResponseObject, error) {
	all, err := s.d.List(ctx)
	if err != nil {
		code, body := errorBody(err)
		return api.ListServersdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	out := api.ServerList{Servers: make([]api.Server, 0, len(all))}
	for _, v := range all {
		out.Servers = append(out.Servers, toAPIServer(v))
	}
	return api.ListServers200JSONResponse(out), nil
}

func (s *Server) CreateServer(ctx context.Context, req api.CreateServerRequestObject) (api.CreateServerResponseObject, error) {
	if req.Body == nil {
		code, body := errorBody(daemon.ErrInvalidArgument)
		return api.CreateServerdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	b := req.Body
	v, err := s.d.Create(ctx, daemon.CreateRequest{
		Name:       b.Name,
		Port:       b.Port,
		AcceptEULA: b.AcceptEula,
		Version:    deref(b.Version),
		Type:       deref(b.Type),
		Memory:     deref(b.Memory),
		ImageTag:   deref(b.ImageTag),
		Env:        derefMap(b.Env),
	})
	if err != nil {
		code, body := errorBody(err)
		return api.CreateServerdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	return api.CreateServer201JSONResponse(toAPIServer(v)), nil
}

func (s *Server) GetServer(ctx context.Context, req api.GetServerRequestObject) (api.GetServerResponseObject, error) {
	v, err := s.d.Get(ctx, req.Name)
	if err != nil {
		code, body := errorBody(err)
		return api.GetServerdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	return api.GetServer200JSONResponse(toAPIServer(v)), nil
}

func (s *Server) DeleteServer(ctx context.Context, req api.DeleteServerRequestObject) (api.DeleteServerResponseObject, error) {
	if err := s.d.Remove(ctx, req.Name, deref(req.Params.Purge)); err != nil {
		code, body := errorBody(err)
		return api.DeleteServerdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	return api.DeleteServer204Response{}, nil
}

func (s *Server) UpServer(ctx context.Context, req api.UpServerRequestObject) (api.UpServerResponseObject, error) {
	v, err := s.d.Up(ctx, req.Name, deref(req.Params.Pull))
	if err != nil {
		code, body := errorBody(err)
		return api.UpServerdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	return api.UpServer202JSONResponse(toAPIServer(v)), nil
}

func (s *Server) DownServer(ctx context.Context, req api.DownServerRequestObject) (api.DownServerResponseObject, error) {
	v, err := s.d.Down(ctx, req.Name, deref(req.Params.Force))
	if err != nil {
		code, body := errorBody(err)
		return api.DownServerdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	return api.DownServer202JSONResponse(toAPIServer(v)), nil
}

func (s *Server) GetServerLogs(ctx context.Context, req api.GetServerLogsRequestObject) (api.GetServerLogsResponseObject, error) {
	entries, err := s.d.Logs(ctx, req.Name, deref(req.Params.Tail))
	if err != nil {
		code, body := errorBody(err)
		return api.GetServerLogsdefaultJSONResponse{StatusCode: code, Body: body}, nil
	}
	out := api.LogList{Entries: make([]api.LogEntry, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, api.LogEntry{Time: e.Time, Source: toAPILogSource(e.Source), Message: e.Message})
	}
	return api.GetServerLogs200JSONResponse(out), nil
}

// errorMapping は daemon のエラーと API の ErrorCode・HTTP ステータスの対応。上から順に errors.Is で判定する。
var errorMapping = []struct {
	err    error
	code   api.ErrorCode
	status int
}{
	{daemon.ErrInvalidArgument, api.ErrorCodeInvalidArgument, http.StatusBadRequest},
	{daemon.ErrNotFound, api.ErrorCodeNotFound, http.StatusNotFound},
	{daemon.ErrAlreadyExists, api.ErrorCodeAlreadyExists, http.StatusConflict},
	{daemon.ErrPortInUse, api.ErrorCodePortInUse, http.StatusConflict},
	{daemon.ErrOperationInProgress, api.ErrorCodeOperationInProgress, http.StatusConflict},
	{daemon.ErrServerRunning, api.ErrorCodeServerRunning, http.StatusConflict},
	{daemon.ErrServerStarting, api.ErrorCodeServerStarting, http.StatusConflict},
}

// errorBody はエラーを HTTP ステータスと API のエラーに読み替える。
// 想定外のエラーは 500 にして、詳細はログにだけ残す（message は利用者にそのまま表示される）。
func errorBody(err error) (int, api.Error) {
	for _, m := range errorMapping {
		if errors.Is(err, m.err) {
			return m.status, api.Error{Error: api.ErrorBody{Code: m.code, Message: err.Error()}}
		}
	}
	slog.Error("request failed", "error", err)
	return http.StatusInternalServerError, api.Error{Error: api.ErrorBody{
		Code:    api.ErrorCodeInternal,
		Message: "internal error (see mcctld logs)",
	}}
}

func toAPIServer(v daemon.Server) api.Server {
	env := maps.Clone(v.Spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	out := api.Server{
		Spec: api.ServerSpec{
			Name:     v.Spec.Name,
			Port:     v.Spec.Port,
			Version:  v.Spec.Version,
			Type:     v.Spec.Type,
			Memory:   v.Spec.Memory,
			ImageTag: v.Spec.ImageTag,
			Env:      env,
		},
		DesiredState: api.DesiredState(v.Spec.DesiredState),
		Status: api.Status{
			State:    api.ActualState(v.State),
			Health:   api.Health(v.Health),
			Restarts: int(v.Restarts),
		},
	}
	if op := v.LastOp; op != nil {
		o := &api.Operation{
			Kind:      api.OperationKind(op.Kind),
			Result:    api.OperationResult(op.Result),
			StartedAt: op.StartedAt,
		}
		if !op.FinishedAt.IsZero() {
			t := op.FinishedAt
			o.FinishedAt = &t
		}
		if op.Error != "" {
			e := op.Error
			o.Error = &e
		}
		out.LastOperation = o
	}
	return out
}

func toAPILogSource(s engine.LogSource) api.LogEntrySource {
	if s == engine.LogSourceSystemd {
		return api.LogEntrySourceSystemd
	}
	return api.LogEntrySourceContainer
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func derefMap(p *map[string]string) map[string]string {
	if p == nil {
		return nil
	}
	return maps.Clone(*p)
}
