package mqtt

import (
	"context"
	"errors"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

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

// fakePaho embeds the interface, so only what publish uses is implemented.
type fakePaho struct {
	paho.Client
	open    bool
	topics  []string
	payload []byte
	qos     byte
}

func (f *fakePaho) IsConnectionOpen() bool { return f.open }

func (f *fakePaho) Publish(topic string, qos byte, _ bool, payload any) paho.Token {
	f.topics = append(f.topics, topic)
	f.qos = qos
	f.payload, _ = payload.([]byte)
	return fakePahoToken{}
}

type fakePahoToken struct{ paho.Token }

func (fakePahoToken) WaitTimeout(time.Duration) bool { return true }
func (fakePahoToken) Error() error                   { return nil }

func TestApplyFailsFastWhileDisconnected(t *testing.T) {
	f := &fakePaho{}
	c := &Client{base: "zigbee2mqtt", client: f}
	target := 20.0

	if err := c.Apply("trv-office", controller.Command{Target: &target}); !errors.Is(err, errNotConnected) {
		t.Fatalf("err = %v, want errNotConnected", err)
	}
	if len(f.topics) != 0 {
		t.Fatalf("handed %v to paho, which would send it after reconnecting", f.topics)
	}

	f.open = true
	if err := c.Apply("trv-office", controller.Command{Target: &target}); err != nil {
		t.Fatal(err)
	}
	if len(f.topics) != 1 || f.topics[0] != "zigbee2mqtt/trv-office/set" {
		t.Fatalf("published %v", f.topics)
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

func TestParseThermostatKeepsFieldsWithAnUnknownMode(t *testing.T) {
	s, err := parseThermostat([]byte(`{"system_mode":"Off","occupied_heating_setpoint":20}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode == nil || *s.Mode != controller.ModeUnknown {
		t.Fatalf("mode = %v, want unknown", s.Mode)
	}
	if s.Setpoint == nil || *s.Setpoint != 20 {
		t.Fatalf("setpoint = %v, want 20", s.Setpoint)
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

func TestApplyPayload(t *testing.T) {
	heat := controller.ModeHeat
	target := 20.5
	tests := []struct {
		name string
		cmd  controller.Command
		want string
	}{
		{"target", controller.Command{Target: &target}, `{"occupied_heating_setpoint":20.5}`},
		{"mode", controller.Command{Mode: &heat}, `{"system_mode":"heat"}`},
		{"both", controller.Command{Mode: &heat, Target: &target}, `{"occupied_heating_setpoint":20.5,"system_mode":"heat"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakePaho{open: true}
			c := &Client{base: "zigbee2mqtt", client: f}
			if err := c.Apply("trv-office", tt.cmd); err != nil {
				t.Fatal(err)
			}
			if got := string(f.payload); got != tt.want {
				t.Fatalf("payload = %s, want %s", got, tt.want)
			}
			if f.qos != 0 {
				t.Fatalf("qos = %d: paho would resend it after a reconnect", f.qos)
			}
		})
	}
}
