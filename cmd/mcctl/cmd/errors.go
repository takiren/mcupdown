package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/takiren/mcupdown/internal/api"
)

// connError は mcctld に接続できなかったときのエラーを、確認すべきことの案内付きで返す。
func (a *app) connError(err error) error {
	return fmt.Errorf("mcctld に接続できません（ソケット: %s）: %w\nmcctld が動いているか、--socket のパスが正しいか確認してください", a.socket, err)
}

// responseError は想定外のステータスのレスポンスを、ErrorCode に応じた案内付きのエラーにする。
// name は案内に使うサーバー名で、サーバーを指定しない操作では空文字列。
func responseError(status int, body []byte, name string) error {
	var e api.Error
	if err := json.Unmarshal(body, &e); err != nil || e.Error.Code == "" {
		return fmt.Errorf("mcctld が予期しない応答を返しました（HTTP %d）: %s", status, body)
	}
	msg := e.Error.Message
	if hint := errorHint(e.Error.Code, name); hint != "" {
		msg += "\n" + hint
	}
	return errors.New(msg)
}

func errorHint(code api.ErrorCode, name string) string {
	switch code {
	case api.ErrorCodeServerStarting:
		return fmt.Sprintf("起動処理の途中です。強制的に止めるには mcctl down %s --force を実行してください（最大 60 秒後に強制終了され、ワールドが壊れるおそれがあります）", name)
	case api.ErrorCodeServerRunning:
		return fmt.Sprintf("動いているサーバーは削除できません。先に mcctl down %s で停止してください", name)
	case api.ErrorCodeForbidden:
		return "この操作には mcctl グループのメンバーである必要があります（sudo usermod -aG mcctl $USER を実行し、ログインし直してください）"
	case api.ErrorCodeOperationInProgress:
		return fmt.Sprintf("別の操作を実行中です。mcctl status %s で進み具合を確認してください", name)
	case api.ErrorCodeNotFound:
		if name != "" {
			return "mcctl list で登録済みのサーバーを確認してください"
		}
	case api.ErrorCodePortInUse:
		return "mcctl list で使用中のポートを確認してください"
	}
	return ""
}

// expect はステータスが want でなければエラーを返す。
func expect(res interface{ StatusCode() int }, body []byte, want int, name string) error {
	if res.StatusCode() == want {
		return nil
	}
	return responseError(res.StatusCode(), body, name)
}
