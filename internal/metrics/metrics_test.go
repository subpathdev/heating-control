package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/subpathdev/heating-control/internal/controller"
)

type fakeChecker struct{ err error }

func (fakeChecker) Name() string                  { return "fake" }
func (f fakeChecker) Check(context.Context) error { return f.err }

type fakeThermostat struct{ err error }

func (f fakeThermostat) Apply(string, controller.Command) error { return f.err }

type fakeRooms []controller.RoomStatus

func (f fakeRooms) Rooms() []controller.RoomStatus { return f }

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	return rec.Body.String()
}

func wantLine(t *testing.T, body, line string) {
	t.Helper()
	if !strings.Contains(body, line+"\n") {
		t.Errorf("missing %q", line)
	}
}

func TestBuildInfoAndRuntime(t *testing.T) {
	body := scrape(t, New("1.2.3"))
	wantLine(t, body, `heating_control_build_info{version="1.2.3"} 1`)
	if !strings.Contains(body, "go_goroutines") {
		t.Error("missing the Go runtime metrics")
	}
}

func TestCheckUp(t *testing.T) {
	m := New("dev")
	m.AddChecker(fakeChecker{})
	wantLine(t, scrape(t, m), `heating_control_check_up{check="fake"} 1`)

	m = New("dev")
	m.AddChecker(fakeChecker{err: errors.New("down")})
	wantLine(t, scrape(t, m), `heating_control_check_up{check="fake"} 0`)
}

func TestCommandsAreCounted(t *testing.T) {
	m := New("dev")
	failed := errors.New("down")
	target := 20.0

	if err := m.Thermostat(fakeThermostat{}).Apply("trv-office", controller.Command{Target: &target}); err != nil {
		t.Fatal(err)
	}
	if err := m.Thermostat(fakeThermostat{err: failed}).Apply("trv-office", controller.Command{Target: &target}); !errors.Is(err, failed) {
		t.Fatalf("the error must be passed on, got %v", err)
	}

	body := scrape(t, m)
	wantLine(t, body, `heating_control_commands_total{command="set_target",result="ok",trv="trv-office"} 1`)
	wantLine(t, body, `heating_control_commands_total{command="set_target",result="error",trv="trv-office"} 1`)
}

func TestRoomTargets(t *testing.T) {
	target := 20.5
	m := New("dev")
	m.AddRooms(fakeRooms{
		{ID: "office", Target: &target, AiringState: controller.AiringActive},
		{ID: "bedroom", AiringState: controller.AiringNone, CommandFailed: true},
	})

	body := scrape(t, m)
	wantLine(t, body, `heating_control_room_target_celsius{room="office"} 20.5`)
	wantLine(t, body, `heating_control_room_airing{room="office"} 1`)
	wantLine(t, body, `heating_control_room_airing{room="bedroom"} 0`)
	wantLine(t, body, `heating_control_room_command_failed{room="bedroom"} 1`)
	if strings.Contains(body, `heating_control_room_target_celsius{room="bedroom"}`) {
		t.Error("a room with an unknown target must have no series")
	}
}
