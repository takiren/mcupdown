package apiserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizer(t *testing.T) {
	const self, member, outsider = 991, 1001, 1500
	a := &Authorizer{
		SelfUID: self,
		Group:   "mcctl",
		MemberOf: func(uid uint32, group string) (bool, error) {
			if group != "mcctl" {
				return false, errors.New("unexpected group")
			}
			if uid == 1600 {
				return false, errors.New("no such user")
			}
			return uid == member, nil
		},
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := a.Middleware(ok)

	tests := []struct {
		name   string
		method string
		uid    uint32
		want   int
	}{
		{"outsider can read", http.MethodGet, outsider, http.StatusNoContent},
		{"outsider cannot mutate", http.MethodPost, outsider, http.StatusForbidden},
		{"outsider cannot delete", http.MethodDelete, outsider, http.StatusForbidden},
		{"member can mutate", http.MethodPost, member, http.StatusNoContent},
		{"root can mutate", http.MethodPost, 0, http.StatusNoContent},
		{"self can mutate", http.MethodPost, self, http.StatusNoContent},
		{"lookup failure is denied", http.MethodPost, 1600, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(WithPeerUID(t.Context(), tt.uid), tt.method, "/v1/servers", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
			if tt.want == http.StatusForbidden && rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("forbidden response is not JSON")
			}
		})
	}

	t.Run("unknown peer", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/servers", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		// UID を取れる OS（Linux）では取れなかったら断り、取れない OS（macOS）では認可しない。
		want := http.StatusNoContent
		if peerCredSupported {
			want = http.StatusForbidden
		}
		if rec.Code != want {
			t.Errorf("status = %d, want %d", rec.Code, want)
		}
	})
}
