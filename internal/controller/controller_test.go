package controller

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/subpathdev/heating-control/internal/config"
)

type fakeThermostat struct {
	targets []float64
	err     error
	// If set, SetTarget signals entered and waits for release.
	entered, release chan struct{}
}

func (f *fakeThermostat) SetTarget(_ string, target float64) error {
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	if f.err != nil {
		return f.err
	}
	f.targets = append(f.targets, target)
	return nil
}

func newController(t *testing.T) (*Controller, *fakeThermostat) {
	t.Helper()
	f := &fakeThermostat{}
	return New(&config.Config{
		MinTarget: 10,
		MaxTarget: 25,
		Rooms:     []config.Room{{ID: "office", Label: "Office", TRV: "trv-office", Sensor: "sensor-office"}},
	}, f), f
}

func ptr[T any](v T) *T { return &v }

func state(mode Mode, setpoint float64) ThermostatState {
	return ThermostatState{Mode: &mode, Setpoint: &setpoint}
}

func report(t *testing.T, c *Controller, s ThermostatState) {
	t.Helper()
	c.HandleThermostatState("trv-office", s)
	c.Wait()
}

func target(t *testing.T, c *Controller) float64 {
	t.Helper()
	got := c.Rooms()[0].Target
	if got == nil {
		t.Fatal("target is unknown")
	}
	return *got
}

func TestSetTargetWhileHeating(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("heat", 20))

	if err := c.SetTarget("office", 20.5); err != nil {
		t.Fatal(err)
	}
	if len(f.targets) != 1 || f.targets[0] != 20.5 {
		t.Fatalf("sent %v, want [20.5]", f.targets)
	}
	if got := target(t, c); got != 20.5 {
		t.Fatalf("target = %v, want 20.5", got)
	}
}

func TestSetTargetRoundsToHalfDegree(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("heat", 20))

	if err := c.SetTarget("office", 20.3); err != nil {
		t.Fatal(err)
	}
	if f.targets[0] != 20.5 {
		t.Fatalf("sent %v, want 20.5", f.targets[0])
	}
}

func TestSetTargetRejects(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("heat", 20))

	for _, bad := range []float64{9.5, 25.5, math.NaN(), math.Inf(1)} {
		if err := c.SetTarget("office", bad); !errors.Is(err, ErrTargetOutOfRange) {
			t.Errorf("SetTarget(%v) = %v, want ErrTargetOutOfRange", bad, err)
		}
	}
	if err := c.SetTarget("attic", 20); !errors.Is(err, ErrUnknownRoom) {
		t.Errorf("unknown room: %v", err)
	}
	if len(f.targets) != 0 {
		t.Fatalf("sent %v, want nothing", f.targets)
	}
}

func TestSetTargetFailureKeepsOldTarget(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("heat", 20))
	f.err = errors.New("broker down")

	if err := c.SetTarget("office", 21); err == nil {
		t.Fatal("want an error")
	}
	if got := target(t, c); got != 20 {
		t.Fatalf("target = %v, want 20", got)
	}
}

// Writing a setpoint to a TRVZB that is off switches it to heat, which would
// heat with the window open.
func TestSetTargetWhileOffIsHeld(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("heat", 20))
	report(t, c, state("off", 7))

	if err := c.SetTarget("office", 22); err != nil {
		t.Fatal(err)
	}
	if len(f.targets) != 0 {
		t.Fatalf("sent %v while off", f.targets)
	}
	if got := target(t, c); got != 22 {
		t.Fatalf("page shows %v, want the held 22", got)
	}

	report(t, c, state("heat", 20))
	if len(f.targets) != 1 || f.targets[0] != 22 {
		t.Fatalf("sent %v, want [22] once heating again", f.targets)
	}
}

// Recorded sequence from a real TRVZB going off and back to heat.
func TestFrostSetpointIsNeverATarget(t *testing.T) {
	c, _ := newController(t)
	for _, s := range []ThermostatState{
		state("heat", 20),
		state("off", 20),
		state("off", 7),
		state("heat", 7),
		state("heat", 20),
	} {
		report(t, c, s)
		if got := target(t, c); got != 20 {
			t.Fatalf("after %s/%v target = %v, want 20", *s.Mode, *s.Setpoint, got)
		}
	}
}

func TestReportedFrostTemperatureIsUsed(t *testing.T) {
	c, _ := newController(t)
	report(t, c, state("heat", 20))
	report(t, c, ThermostatState{FrostProtection: ptr(5.0)})

	report(t, c, state("heat", 5))
	if got := target(t, c); got != 20 {
		t.Fatalf("target = %v, want 20", got)
	}
	report(t, c, state("heat", 7))
	if got := target(t, c); got != 7 {
		t.Fatalf("target = %v, want 7", got)
	}
}

func TestKnobChangeUpdatesTarget(t *testing.T) {
	c, _ := newController(t)
	report(t, c, state("heat", 20))
	report(t, c, state("heat", 18))
	if got := target(t, c); got != 18 {
		t.Fatalf("target = %v, want 18", got)
	}
}

func TestSensorTemperatureWinsOverLocal(t *testing.T) {
	c, _ := newController(t)
	report(t, c, ThermostatState{LocalTemperature: ptr(22.0)})
	if got := c.Rooms()[0].Temperature; got == nil || *got != 22 {
		t.Fatalf("temperature = %v, want the local 22", got)
	}
	c.HandleRoomTemperature("sensor-office", 21)
	if got := c.Rooms()[0].Temperature; got == nil || *got != 21 {
		t.Fatalf("temperature = %v, want the sensor's 21", got)
	}
}

func TestParseMode(t *testing.T) {
	for _, valid := range []Mode{ModeOff, ModeAuto, ModeHeat} {
		if got, err := ParseMode(string(valid)); err != nil || got != valid {
			t.Errorf("ParseMode(%q) = %q, %v", valid, got, err)
		}
	}
	for _, invalid := range []string{"", "Off", "cool"} {
		if _, err := ParseMode(invalid); err == nil {
			t.Errorf("ParseMode(%q): want an error", invalid)
		}
	}
}

func TestPendingWaitsForAKnownMode(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("off", 7))
	if err := c.SetTarget("office", 22); err != nil {
		t.Fatal(err)
	}
	report(t, c, ThermostatState{LocalTemperature: ptr(20.0)})
	if len(f.targets) != 0 {
		t.Fatalf("sent %v while still off", f.targets)
	}
}

// The MQTT client can't read the broker's acknowledgement while it delivers a
// report, so a report must never wait for a publish.
func TestReportsDontWaitForAPublish(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("heat", 20))
	f.entered, f.release = make(chan struct{}), make(chan struct{})

	done := make(chan error)
	go func() { done <- c.SetTarget("office", 21) }()
	<-f.entered

	finished := make(chan struct{})
	go func() {
		c.HandleThermostatState("trv-office", state("heat", 20))
		c.Rooms()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a report or read waited for the publish")
	}

	close(f.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSendingAHeldTargetDoesntBlockTheReport(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("off", 7))
	if err := c.SetTarget("office", 22); err != nil {
		t.Fatal(err)
	}
	f.entered, f.release = make(chan struct{}), make(chan struct{})

	finished := make(chan struct{})
	go func() {
		c.HandleThermostatState("trv-office", state("heat", 20))
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the report waited for the publish")
	}
	<-f.entered
	close(f.release)
	c.Wait()
	if len(f.targets) != 1 || f.targets[0] != 22 {
		t.Fatalf("sent %v, want [22]", f.targets)
	}
}

func TestHeldTargetWaitsForTheThermostatToRestore(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("off", 7))
	if err := c.SetTarget("office", 22); err != nil {
		t.Fatal(err)
	}
	report(t, c, state("heat", 7))
	if len(f.targets) != 0 {
		t.Fatalf("sent %v before the thermostat restored its setpoint", f.targets)
	}
	report(t, c, state("heat", 20))
	if len(f.targets) != 1 || f.targets[0] != 22 {
		t.Fatalf("sent %v, want [22]", f.targets)
	}
}

func TestHeldTargetIsRetriedAfterAFailure(t *testing.T) {
	c, f := newController(t)
	report(t, c, state("off", 7))
	if err := c.SetTarget("office", 22); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("broker down")
	report(t, c, state("heat", 20))
	if got := target(t, c); got != 22 {
		t.Fatalf("page shows %v, want the held 22", got)
	}

	f.err = nil
	report(t, c, state("heat", 20))
	if len(f.targets) != 1 || f.targets[0] != 22 {
		t.Fatalf("sent %v, want [22] on the retry", f.targets)
	}
	report(t, c, state("heat", 20))
	if len(f.targets) != 1 {
		t.Fatalf("sent %v, want only one", f.targets)
	}
}

func TestUnknownModeHoldsTheTarget(t *testing.T) {
	c, f := newController(t)
	if err := c.SetTarget("office", 22); err != nil {
		t.Fatal(err)
	}
	if len(f.targets) != 0 {
		t.Fatalf("sent %v before the thermostat reported", f.targets)
	}
	if got := target(t, c); got != 22 {
		t.Fatalf("page shows %v, want the held 22", got)
	}
}

func TestStaleReportDoesntOverwriteANewTarget(t *testing.T) {
	c, _ := newController(t)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	report(t, c, state("heat", 20))
	if err := c.SetTarget("office", 21); err != nil {
		t.Fatal(err)
	}

	report(t, c, state("heat", 20))
	if got := target(t, c); got != 21 {
		t.Fatalf("after a stale report target = %v, want 21", got)
	}

	// Once confirmed, any change is a real one, even inside the grace period.
	report(t, c, state("heat", 21))
	report(t, c, state("heat", 18))
	if got := target(t, c); got != 18 {
		t.Fatalf("after the knob target = %v, want 18", got)
	}
}

func TestLostWriteIsShownAfterTheGracePeriod(t *testing.T) {
	c, _ := newController(t)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	report(t, c, state("heat", 20))
	if err := c.SetTarget("office", 21); err != nil {
		t.Fatal(err)
	}

	now = now.Add(echoGrace)
	report(t, c, state("heat", 20))
	if got := target(t, c); got != 20 {
		t.Fatalf("target = %v, want the thermostat's 20", got)
	}
}
