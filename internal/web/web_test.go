package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/subpathdev/heating-control/internal/api"
	"github.com/subpathdev/heating-control/internal/controller"
)

type fakeRooms struct {
	id     string
	target float64
	err    error
}

func (f *fakeRooms) Rooms() []controller.RoomStatus {
	return []controller.RoomStatus{
		{ID: "office", Label: "Office", Mode: controller.ModeOff},
		{ID: "bedroom", Label: "Bedroom"},
	}
}
func (f *fakeRooms) Limits() (float64, float64) { return 10, 25 }
func (f *fakeRooms) SetTarget(id string, target float64) error {
	f.id, f.target = id, target
	return f.err
}

const testHost = "192.168.178.60:30081"

func handler(rooms Rooms) http.Handler {
	return Handler(rooms, []string{"heating.example"})
}

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = testHost
	handler(&fakeRooms{}).ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPageIsServed(t *testing.T) {
	rec := get(t, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("status %d, body %.60q", rec.Code, rec.Body.String())
	}
}

func TestSpecIsServed(t *testing.T) {
	rec := get(t, "/api/openapi.yaml")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "openapi: 3") {
		t.Fatalf("status %d, body %.60q", rec.Code, rec.Body.String())
	}
}

func TestListRooms(t *testing.T) {
	rec := get(t, "/api/rooms")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var got api.RoomList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rooms) != 2 || got.Rooms[0].Id != "office" || got.MaxTarget != 25 {
		t.Fatalf("got %+v", got)
	}
	if got.Rooms[0].Mode != api.Off {
		t.Errorf("mode = %q, want off", got.Rooms[0].Mode)
	}
	if got.Rooms[1].Mode != api.Unknown {
		t.Errorf("a room that hasn't reported has mode %q, want unknown", got.Rooms[1].Mode)
	}
	// Required by the contract: unknown values are null, not left out.
	if !strings.Contains(rec.Body.String(), `"target":null`) {
		t.Errorf("body %s has no null target", rec.Body.String())
	}
}

func TestSetTarget(t *testing.T) {
	f := &fakeRooms{}
	rec := post(t, handler(f), "/api/rooms/office/target", `{"target":20.5}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
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
		{"too large", `{"target":20,"x":"` + strings.Repeat("a", maxBodyBytes) + `"}`, nil, http.StatusBadRequest},
		{"unknown room", `{"target":20}`, controller.ErrUnknownRoom, http.StatusNotFound},
		{"out of range", `{"target":99}`, controller.ErrTargetOutOfRange, http.StatusBadRequest},
		{"broker down", `{"target":20}`, errors.New("down"), http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(t, handler(&fakeRooms{err: tt.err}), "/api/rooms/office/target", tt.body, nil)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			var got api.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Message == "" {
				t.Fatalf("body %q isn't an Error: %v", rec.Body.String(), err)
			}
		})
	}
}

func TestOtherWebsitesAreRefused(t *testing.T) {
	f := &fakeRooms{}
	rec := post(t, handler(f), "/api/rooms/office/target", `{"target":20}`, map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var got api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Message == "" {
		t.Fatalf("body %q isn't an Error: %v", rec.Body.String(), err)
	}
	if f.id != "" {
		t.Fatal("the target was changed")
	}
	rec = post(t, handler(f), "/api/rooms/office/target", `{"target":20}`, map[string]string{"Origin": "http://" + testHost})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same origin: status = %d, want 204", rec.Code)
	}
}

// With DNS rebinding the attacker's name points here, so Origin matches Host.
func TestDNSRebindingIsRefused(t *testing.T) {
	tests := []struct {
		host string
		want int
	}{
		{"evil.example", http.StatusForbidden},
		{"evil.example:30081", http.StatusForbidden},
		{"heating.example:30081", http.StatusNoContent},
		{"HEATING.example", http.StatusNoContent},
		{"localhost:8080", http.StatusNoContent},
		{"192.168.178.60:30081", http.StatusNoContent},
		{"[fd2a::1]:30081", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			f := &fakeRooms{}
			req := httptest.NewRequest(http.MethodPost, "/api/rooms/office/target", strings.NewReader(`{"target":20}`))
			req.Host = tt.host
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://"+tt.host)
			rec := httptest.NewRecorder()
			handler(f).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusForbidden && f.id != "" {
				t.Fatal("the target was changed")
			}
		})
	}
}
