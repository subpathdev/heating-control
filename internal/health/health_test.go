package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeChecker struct{ err error }

func (fakeChecker) Name() string                  { return "fake" }
func (f fakeChecker) Check(context.Context) error { return f.err }

func get(t *testing.T, path string, checkers ...Checker) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, "1.2.3", checkers...)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthzIgnoresCheckers(t *testing.T) {
	rec := get(t, "/healthz", fakeChecker{err: errors.New("down")})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "1.2.3") {
		t.Fatalf("body = %q, want the version", rec.Body.String())
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"healthy", nil, http.StatusOK},
		{"failing", errors.New("down"), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := get(t, "/readyz", fakeChecker{err: tt.err}); rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
