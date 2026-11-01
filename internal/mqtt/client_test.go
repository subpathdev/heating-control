package mqtt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/subpathdev/heating-control/internal/controller"
)

type fakeConnection bool

func (f fakeConnection) IsConnectionOpen() bool { return bool(f) }

func TestConnectionChecker(t *testing.T) {
	if err := (connectionChecker{conn: fakeConnection(true)}).Check(context.Background()); err != nil {
		t.Fatalf("connected: %v", err)
	}
	if err := (connectionChecker{conn: fakeConnection(false)}).Check(context.Background()); err == nil {
		t.Fatal("disconnected: want an error")
	}
}

type fakeToken struct {
	done bool
	err  error
}

func (f fakeToken) WaitTimeout(time.Duration) bool { return f.done }
func (f fakeToken) Error() error                   { return f.err }

func TestWaitForToken(t *testing.T) {
	failed := errors.New("refused")
	if err := waitForToken(fakeToken{done: true}, time.Second); err != nil {
		t.Fatalf("success: %v", err)
	}
	if err := waitForToken(fakeToken{done: true, err: failed}, time.Second); !errors.Is(err, failed) {
		t.Fatalf("failure: %v", err)
	}
	if err := waitForToken(fakeToken{}, time.Second); err == nil {
		t.Fatal("timeout: want an error")
	}
}

func TestParseThermostat(t *testing.T) {
	s, err := parseThermostat([]byte(`{"system_mode":"off","occupied_heating_setpoint":7,"local_temperature":22,"running_state":"idle","frost_protection_temperature":7,"battery":96}`))
	if err != nil {
		t.Fatal(err)
	}
	if *s.Mode != controller.ModeOff || *s.Setpoint != 7 || *s.LocalTemperature != 22 || *s.Heating || *s.FrostProtection != 7 {
		t.Fatalf("parsed %+v", s)
	}
}

func TestParseThermostatRejectsUnknownMode(t *testing.T) {
	if _, err := parseThermostat([]byte(`{"system_mode":"Off"}`)); err == nil {
		t.Fatal("want an error")
	}
}

func TestParseThermostatHeating(t *testing.T) {
	s, err := parseThermostat([]byte(`{"running_state":"heat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Heating == nil || !*s.Heating {
		t.Fatalf("heating = %v, want true", s.Heating)
	}
}

func TestParseThermostatMissingFieldsAreNil(t *testing.T) {
	s, err := parseThermostat([]byte(`{"battery":96}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != nil || s.Setpoint != nil {
		t.Fatalf("parsed %+v", s)
	}
}

func TestParseTemperature(t *testing.T) {
	got, err := parseTemperature([]byte(`{"temperature":19.5,"humidity":50}`))
	if err != nil || got == nil || *got != 19.5 {
		t.Fatalf("got %v, %v", got, err)
	}
	got, err = parseTemperature([]byte(`{"humidity":50}`))
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}
