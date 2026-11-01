// Package web serves the phone page and its small JSON API.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"

	"github.com/subpathdev/heating-control/internal/controller"
)

//go:embed static
var static embed.FS

const maxBodyBytes = 1024

// Rooms is what the page can read and change.
type Rooms interface {
	Rooms() []controller.RoomStatus
	Limits() (min, max float64)
	SetTarget(id string, target float64) error
}

// Register adds the page and API to mux.
func Register(mux *http.ServeMux, rooms Rooms) {
	content, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(content))

	mux.HandleFunc("GET /api/rooms", func(w http.ResponseWriter, _ *http.Request) {
		min, max := rooms.Limits()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"rooms":     rooms.Rooms(),
			"minTarget": min,
			"maxTarget": max,
		}); err != nil {
			log.Printf("write rooms: %v", err)
		}
	})

	mux.HandleFunc("POST /api/rooms/{id}/target", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Target *float64 `json:"target"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Target == nil {
			http.Error(w, "body must be {\"target\": <number>}", http.StatusBadRequest)
			return
		}
		switch err := rooms.SetTarget(r.PathValue("id"), *body.Target); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, controller.ErrUnknownRoom):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, controller.ErrTargetOutOfRange):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			log.Printf("set target: %v", err)
			http.Error(w, "could not reach the thermostat", http.StatusBadGateway)
		}
	})
}
