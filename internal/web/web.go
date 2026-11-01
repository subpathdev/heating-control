// Package web serves the phone page and implements the API in internal/api.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/subpathdev/heating-control/internal/api"
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

// Handler returns the page, the API and its OpenAPI document. The page has no
// login, so it refuses requests from other websites, and allowedHosts protects
// that check against DNS rebinding.
func Handler(rooms Rooms, allowedHosts []string) http.Handler {
	mux := http.NewServeMux()
	content, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(content))

	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		if _, err := w.Write(api.Spec); err != nil {
			log.Printf("write openapi.yaml: %v", err)
		}
	})

	strict := api.NewStrictHandlerWithOptions(server{rooms: rooms}, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest,
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("api: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		},
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: badRequest,
		Middlewares:      []api.MiddlewareFunc{limitBody},
	})

	crossOrigin := http.NewCrossOriginProtection()
	crossOrigin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, "request from another website")
	}))
	return checkHost(allowedHosts, crossOrigin.Handler(mux))
}

// checkHost refuses unknown host names. With DNS rebinding another website's
// name points at this server, so its requests look same-origin. Such a request
// always carries that name, never an IP address.
func checkHost(allowedHosts []string, next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true}
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if net.ParseIP(host) == nil && !allowed[strings.ToLower(host)] {
			writeError(w, http.StatusForbidden, "unknown host name, add it to allowedHosts")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// server implements api.StrictServerInterface.
type server struct {
	rooms Rooms
}

func (s server) ListRooms(context.Context, api.ListRoomsRequestObject) (api.ListRoomsResponseObject, error) {
	statuses := s.rooms.Rooms()
	rooms := make([]api.Room, 0, len(statuses))
	for _, r := range statuses {
		rooms = append(rooms, api.Room{
			Id:          r.ID,
			Label:       r.Label,
			Temperature: r.Temperature,
			Target:      r.Target,
			Mode:        toAPIMode(r.Mode),
			Heating:     r.Heating,
		})
	}
	min, max := s.rooms.Limits()
	noStore := "no-store"
	return api.ListRooms200JSONResponse{
		Body:    api.RoomList{Rooms: rooms, MinTarget: min, MaxTarget: max},
		Headers: api.ListRooms200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

func (s server) SetRoomTarget(_ context.Context, r api.SetRoomTargetRequestObject) (api.SetRoomTargetResponseObject, error) {
	switch err := s.rooms.SetTarget(r.Id, r.Body.Target); {
	case err == nil:
		return api.SetRoomTarget204Response{}, nil
	case errors.Is(err, controller.ErrUnknownRoom):
		return api.SetRoomTarget404JSONResponse{Message: err.Error()}, nil
	case errors.Is(err, controller.ErrTargetOutOfRange):
		return api.SetRoomTarget400JSONResponse{Message: err.Error()}, nil
	default:
		log.Printf("set target: %v", err)
		return api.SetRoomTarget502JSONResponse{Message: "could not reach the thermostat"}, nil
	}
}

func toAPIMode(m controller.Mode) api.Mode {
	switch m {
	case controller.ModeOff:
		return api.Off
	case controller.ModeAuto:
		return api.Auto
	case controller.ModeHeat:
		return api.Heat
	default:
		return api.Unknown
	}
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func badRequest(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, http.StatusBadRequest, err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(api.Error{Message: message}); err != nil {
		log.Printf("write error: %v", err)
	}
}
