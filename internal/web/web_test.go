package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/subpathdev/heating-control/internal/controller"
)

type fakeRooms struct {
	id     string
	target float64
	err    error
}

func (f *fakeRooms) Rooms() []controller.RoomStatus {
	return []controller.RoomStatus{{ID: "office", Label: "Office"}}
}
func (f *fakeRooms) Limits() (float64, float64) { return 10, 25 }
func (f *fakeRooms) SetTarget(id string, target float64) error {
	f.id, f.target = id, target
	return f.err
}

func Handler(rooms Rooms) http.Handler {
	mux := http.NewServeMux()
	Register(mux, rooms)
	return http.NewCrossOriginProtection().Handler(mux)
}

func post(t *testing.T, h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = "home:30081"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPageIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(&fakeRooms{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("status %d, body %.60q", rec.Code, rec.Body.String())
	}
}

func TestGetRooms(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(&fakeRooms{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/rooms", nil))
	var got struct {
		Rooms     []controller.RoomStatus `json:"rooms"`
		MaxTarget float64                 `json:"maxTarget"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rooms) != 1 || got.Rooms[0].ID != "office" || got.MaxTarget != 25 {
		t.Fatalf("got %+v", got)
	}
}

func TestSetTarget(t *testing.T) {
	f := &fakeRooms{}
	rec := post(t, Handler(f), "/api/rooms/office/target", `{"target":20.5}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.id != "office" || f.target != 20.5 {
		t.Fatalf("got %s %v", f.id, f.target)
	}
}

func TestSetTargetErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
		want int
	}{
		{"bad json", `nope`, nil, http.StatusBadRequest},
		{"missing target", `{}`, nil, http.StatusBadRequest},
		{"unknown room", `{"target":20}`, controller.ErrUnknownRoom, http.StatusNotFound},
		{"out of range", `{"target":99}`, controller.ErrTargetOutOfRange, http.StatusBadRequest},
		{"broker down", `{"target":20}`, errors.New("down"), http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(t, Handler(&fakeRooms{err: tt.err}), "/api/rooms/office/target", tt.body, nil)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestOtherWebsitesAreRefused(t *testing.T) {
	f := &fakeRooms{}
	rec := post(t, Handler(f), "/api/rooms/office/target", `{"target":20}`, map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if f.id != "" {
		t.Fatal("the target was changed")
	}
	rec = post(t, Handler(f), "/api/rooms/office/target", `{"target":20}`, map[string]string{"Origin": "http://home:30081"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same origin: status = %d, want 204", rec.Code)
	}
}
