// Package health serves the liveness and readiness endpoints.
package health

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const checkTimeout = 2 * time.Second

// Checker is a dependency the program needs in order to do useful work.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// Register adds /healthz and /readyz to mux. /healthz only says the process
// responds, so a broker outage doesn't restart the pod. /readyz runs checkers.
func Register(mux *http.ServeMux, version string, checkers ...Checker) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, version)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		defer cancel()

		status := http.StatusOK
		body := ""
		for _, c := range checkers {
			if err := c.Check(ctx); err != nil {
				status = http.StatusServiceUnavailable
				body += fmt.Sprintf("%s: %v\n", c.Name(), err)
				continue
			}
			body += fmt.Sprintf("%s: ok\n", c.Name())
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	})
}
