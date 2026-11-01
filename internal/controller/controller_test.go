package controller

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/subpathdev/heating-control/internal/config"
)

type fakeThermostat struct {
	sent []Command
	err  error
}

func (f *fakeThermostat) Apply(_ string, cmd Command) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, cmd)
	return nil
}

func (f *fakeThermostat) kinds() []string {
	out := []string{}
	for _, c := range f.sent {
		out = append(out, c.Kind())
	}
	return out
}

// harness drives a Controller by hand: time and ticks only move when told to.
type harness struct {
	t   *testing.T
	c   *Controller
	f   *fakeThermostat
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, f: &fakeThermostat{}, now: time.Unix(1000, 0)}
	h.c = New(&config.Config{
		MinTarget: 10,
		MaxTarget: 25,
		Rooms:     []config.Room{{ID: "office", Label: "Office", TRV: "trv-office", Sensor: "sensor-office"}},
	}, h.f)
	h.c.now = func() time.Time { return h.now }
	return h
}

func ptr[T any](v T) *T { return &v }

func state(mode Mode, setpoint float64) ThermostatState {
	return ThermostatState{Mode: &mode, Setpoint: &setpoint}
}

// report delivers a report and lets the controller act on it.
func (h *harness) report(mode Mode, setpoint float64) {
	h.t.Helper()
	h.c.HandleThermostatState("trv-office", state(mode, setpoint))
	h.c.tick()
}

func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	h.now = h.now.Add(d)
	h.c.tick()
}

func (h *harness) status() RoomStatus {
	h.t.Helper()
	return h.c.Rooms()[0]
}

func (h *harness) target() float64 {
	h.t.Helper()
	got := h.status().Target
	if got == nil {
		h.t.Fatal("target is unknown")
	}
	return *got
}

func (h *harness) wantKinds(want ...string) {
	h.t.Helper()
	got := h.f.kinds()
	if len(got) != len(want) {
		h.t.Fatalf("sent %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			h.t.Fatalf("sent %v, want %v", got, want)
		}
	}
}

func (h *harness) setTarget(target float64) {
	h.t.Helper()
	if err := h.c.SetTarget("office", target); err != nil {
		h.t.Fatal(err)
	}
	h.c.tick()
}

func (h *harness) startAiring(d time.Duration) {
	h.t.Helper()
	if err := h.c.StartAiring("office", d); err != nil {
		h.t.Fatal(err)
	}
	h.c.tick()
}

func TestSetTargetIsSentAndConfirmed(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)

	h.setTarget(20.5)
	h.wantKinds("set_target")
	if got := *h.f.sent[0].Target; got != 20.5 {
		t.Fatalf("sent %v, want 20.5", got)
	}
	if got := h.status().TargetState; got != TargetSending {
		t.Fatalf("state = %s, want sending", got)
	}
	h.report("heat", 20.5)
	if got := h.status().TargetState; got != TargetApplied {
		t.Fatalf("state = %s, want applied", got)
	}
}

func TestSetTargetRoundsToHalfDegree(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.setTarget(20.3)
	if got := *h.f.sent[0].Target; got != 20.5 {
		t.Fatalf("sent %v, want 20.5", got)
	}
}

func TestSetTargetRejects(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	for _, bad := range []float64{9.5, 25.5, math.NaN(), math.Inf(1)} {
		if err := h.c.SetTarget("office", bad); !errors.Is(err, ErrTargetOutOfRange) {
			t.Errorf("SetTarget(%v) = %v, want ErrTargetOutOfRange", bad, err)
		}
	}
	if err := h.c.SetTarget("attic", 20); !errors.Is(err, ErrUnknownRoom) {
		t.Errorf("unknown room: %v", err)
	}
	h.c.tick()
	h.wantKinds()
}

func TestUnknownRoomForAiring(t *testing.T) {
	h := newHarness(t)
	if err := h.c.StartAiring("attic", time.Minute); !errors.Is(err, ErrUnknownRoom) {
		t.Errorf("start: %v", err)
	}
	if err := h.c.StopAiring("attic"); !errors.Is(err, ErrUnknownRoom) {
		t.Errorf("stop: %v", err)
	}
}

// Writing a setpoint to a TRVZB that is off switches it to heat, which would
// heat with the window open.
func TestTargetIsHeldWhileOff(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.report("off", 7)

	h.setTarget(22)
	h.wantKinds()
	if got := h.status().TargetState; got != TargetHeld {
		t.Fatalf("state = %s, want held", got)
	}
	if got := h.target(); got != 22 {
		t.Fatalf("page shows %v, want the held 22", got)
	}

	// It restores its own setpoint first, which would overwrite an earlier write.
	h.report("heat", 7)
	h.wantKinds()
	h.report("heat", 20)
	h.wantKinds("set_target")
	if got := *h.f.sent[0].Target; got != 22 {
		t.Fatalf("sent %v, want 22", got)
	}
}

func TestTargetIsHeldWhileModeIsUnknown(t *testing.T) {
	h := newHarness(t)
	h.setTarget(22)
	h.wantKinds()
	if got := h.status().TargetState; got != TargetHeld {
		t.Fatalf("state = %s, want held", got)
	}
}

// Recorded sequence from a real TRVZB going off and back to heat.
func TestFrostSetpointIsNeverATarget(t *testing.T) {
	h := newHarness(t)
	for _, s := range []ThermostatState{
		state("heat", 20),
		state("off", 20),
		state("off", 7),
		state("heat", 7),
		state("heat", 20),
	} {
		h.c.HandleThermostatState("trv-office", s)
		if got := h.target(); got != 20 {
			t.Fatalf("after %s/%v target = %v, want 20", *s.Mode, *s.Setpoint, got)
		}
	}
}

func TestReportedFrostTemperatureIsUsed(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.c.HandleThermostatState("trv-office", ThermostatState{FrostProtection: ptr(5.0)})
	h.report("heat", 5)
	if got := h.target(); got != 20 {
		t.Fatalf("target = %v, want 20", got)
	}
	h.report("heat", 7)
	if got := h.target(); got != 7 {
		t.Fatalf("target = %v, want 7", got)
	}
}

func TestKnobChangeUpdatesTarget(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.report("heat", 18)
	if got := h.target(); got != 18 {
		t.Fatalf("target = %v, want 18", got)
	}
	h.wantKinds()
}

func TestStaleReportDoesntOverwriteANewTarget(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.setTarget(21)
	h.report("heat", 20)
	if got := h.target(); got != 21 {
		t.Fatalf("target = %v, want 21", got)
	}
	// Once confirmed, any change is a real one.
	h.report("heat", 21)
	h.report("heat", 18)
	if got := h.target(); got != 18 {
		t.Fatalf("after the knob target = %v, want 18", got)
	}
}

func TestLostTargetIsRetriedThenGivenUp(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.setTarget(21)

	h.advance(retryInterval - time.Second)
	h.wantKinds("set_target")
	h.advance(time.Second)
	h.wantKinds("set_target", "set_target")

	for i := 0; i < maxAttempts; i++ {
		h.advance(retryInterval)
	}
	if got := len(h.f.sent); got != maxAttempts {
		t.Fatalf("sent %d, want %d", got, maxAttempts)
	}
	s := h.status()
	if s.TargetState != TargetFailed {
		t.Fatalf("state = %s, want failed", s.TargetState)
	}
	if got := h.target(); got != 20 {
		t.Fatalf("target = %v, want the thermostat's 20", got)
	}
	// The next change starts afresh.
	h.setTarget(22)
	if got := h.status().TargetState; got != TargetSending {
		t.Fatalf("state = %s, want sending", got)
	}
}

// Quick +0.5, -0.5: a report of the old value is older than the second command.
func TestEqualReportDoesntConfirmATargetNotYetSent(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.setTarget(20.5)

	if err := h.c.SetTarget("office", 20); err != nil {
		t.Fatal(err)
	}
	h.c.HandleThermostatState("trv-office", state("heat", 20))
	h.c.tick()
	h.wantKinds("set_target", "set_target")
	if got := *h.f.sent[1].Target; got != 20 {
		t.Fatalf("sent %v, want 20", got)
	}
}

// With the knob at the frost value it never reports another setpoint.
func TestHeldTargetGoesOutOnceTheSettleTimeIsOver(t *testing.T) {
	h := newHarness(t)
	h.report("off", 7)
	h.setTarget(22)
	h.report("heat", 7)
	h.wantKinds()

	h.advance(settleTime)
	h.wantKinds("set_target")
}

func TestTargetDoesntGiveUpWhileOffAndNeverBecomesFrost(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.setTarget(21)
	for i := 0; i < maxAttempts-1; i++ {
		h.advance(retryInterval)
	}
	h.report("off", 7)
	h.advance(time.Hour)
	s := h.status()
	if s.TargetState != TargetHeld || *s.Target != 21 {
		t.Fatalf("status %s/%v, want held 21", s.TargetState, *s.Target)
	}
}

func TestFailedPublishDoesntCountAsAnAttempt(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.f.err = errors.New("not connected")
	h.setTarget(21)
	for i := 0; i < maxAttempts+2; i++ {
		h.advance(retryInterval)
	}
	if got := h.status().TargetState; got != TargetSending {
		t.Fatalf("state = %s, want still sending during an outage", got)
	}
	h.f.err = nil
	h.advance(retryInterval)
	h.wantKinds("set_target")
}

func TestFailedPublishWaitsBeforeTryingAgain(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	counting := &countingThermostat{err: errors.New("not connected")}
	h.c.thermostat = counting
	h.setTarget(21)
	h.advance(retryInterval - time.Second)
	if counting.calls != 1 {
		t.Fatalf("tried %d times, want 1", counting.calls)
	}
	h.advance(time.Second)
	if counting.calls != 2 {
		t.Fatalf("tried %d times, want 2", counting.calls)
	}
}

type countingThermostat struct {
	calls int
	err   error
}

func (c *countingThermostat) Apply(string, Command) error {
	c.calls++
	return c.err
}

func TestAiringTurnsOffAndRestores(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)

	h.startAiring(15 * time.Minute)
	h.wantKinds("off")
	if got := h.status().AiringState; got != AiringStarting {
		t.Fatalf("state = %s, want starting", got)
	}
	h.report("off", 20)
	h.report("off", 7)
	s := h.status()
	if s.AiringState != AiringActive || s.AiringUntil == nil {
		t.Fatalf("status = %+v", s)
	}
	if got := h.target(); got != 20 {
		t.Fatalf("target = %v during airing, want 20", got)
	}

	h.advance(15*time.Minute - time.Second)
	h.wantKinds("off")
	h.advance(time.Second)
	h.wantKinds("off", "restore")
	cmd := h.f.sent[1]
	if *cmd.Mode != ModeHeat || *cmd.Target != 20 {
		t.Fatalf("restore sent %v/%v, want heat/20", *cmd.Mode, *cmd.Target)
	}
	if got := h.status().AiringState; got != AiringRestoring {
		t.Fatalf("state = %s, want restoring", got)
	}

	h.report("heat", 7)
	if got := h.status().AiringState; got != AiringRestoring {
		t.Fatalf("after the transient report state = %s, want restoring", got)
	}
	h.report("heat", 20)
	if got := h.status().AiringState; got != AiringNone {
		t.Fatalf("state = %s, want none", got)
	}
	h.advance(time.Hour)
	h.wantKinds("off", "restore")
}

func TestStopAiringRestoresNow(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(time.Hour)
	h.report("off", 7)

	if err := h.c.StopAiring("office"); err != nil {
		t.Fatal(err)
	}
	h.c.tick()
	h.wantKinds("off", "restore")
}

func TestStopAiringTurnsAThermostatThatIsOffBackOn(t *testing.T) {
	h := newHarness(t)
	h.report("auto", 20)
	h.report("off", 7)

	if err := h.c.StopAiring("office"); err != nil {
		t.Fatal(err)
	}
	h.c.tick()
	h.wantKinds("restore")
	cmd := h.f.sent[0]
	if *cmd.Mode != ModeAuto || *cmd.Target != 20 {
		t.Fatalf("restored %s/%v, want auto/20", *cmd.Mode, *cmd.Target)
	}
	h.report("auto", 20)
	if got := h.status().AiringState; got != AiringNone {
		t.Fatalf("state = %s, want none", got)
	}
}

// A report sent before the off took effect mustn't end the retries: if the restore
// was lost and the off arrives, the room would stay off for good.
func TestReportFromBeforeOffDoesntConfirmARestore(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(time.Hour)
	if err := h.c.StopAiring("office"); err != nil {
		t.Fatal(err)
	}
	h.c.tick()
	h.wantKinds("off", "restore")

	h.report("heat", 20)
	if got := h.status().AiringState; got != AiringRestoring {
		t.Fatalf("state = %s, want still restoring", got)
	}

	// The off landed and the restore was lost, so the retry fixes it.
	h.report("off", 7)
	h.advance(retryInterval)
	h.wantKinds("off", "restore", "restore")
	h.report("heat", 20)
	if got := h.status().AiringState; got != AiringNone {
		t.Fatalf("state = %s, want none", got)
	}
}

// The off itself was lost: the thermostat stays on, and the wait runs out.
func TestRestoreIsConfirmedOnceTheWaitRunsOut(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(time.Hour)
	if err := h.c.StopAiring("office"); err != nil {
		t.Fatal(err)
	}
	h.c.tick()

	h.advance(retryInterval)
	h.report("heat", 20)
	if got := h.status().AiringState; got != AiringNone {
		t.Fatalf("state = %s, want none", got)
	}
}

func TestStopAiringWithoutAiringDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	if err := h.c.StopAiring("office"); err != nil {
		t.Fatal(err)
	}
	h.c.tick()
	h.wantKinds()
}

func TestAiringAgainSetsANewEndTime(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(15 * time.Minute)
	h.report("off", 7)

	h.startAiring(time.Hour)
	h.advance(30 * time.Minute)
	h.wantKinds("off")
	h.advance(30 * time.Minute)
	h.wantKinds("off", "restore")
}

func TestTargetChangedDuringAiringIsAppliedAtTheEnd(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(15 * time.Minute)
	h.report("off", 7)

	h.setTarget(22)
	h.wantKinds("off")
	s := h.status()
	if s.TargetState != TargetHeld || *s.Target != 22 {
		t.Fatalf("status = %+v", s)
	}

	h.advance(15 * time.Minute)
	h.wantKinds("off", "restore")
	if got := *h.f.sent[1].Target; got != 22 {
		t.Fatalf("restored %v, want 22", got)
	}
	h.report("heat", 22)
	if got := h.status().TargetState; got != TargetApplied {
		t.Fatalf("state = %s, want applied", got)
	}
}

func TestAiringRestoresTheModeFromBefore(t *testing.T) {
	h := newHarness(t)
	h.report("auto", 20)
	h.startAiring(time.Minute)
	h.report("off", 7)
	h.advance(time.Minute)
	if got := *h.f.sent[1].Mode; got != ModeAuto {
		t.Fatalf("restored %s, want auto", got)
	}
}

// The thermostat is already in its own pause, so off isn't the mode to restore.
func TestAiringDuringAThermostatPauseRestoresHeat(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.report("off", 7)

	h.startAiring(time.Hour)
	h.wantKinds()
	if got := h.status().AiringState; got != AiringActive {
		t.Fatalf("state = %s, want active", got)
	}

	// Its pause ends, and off goes out again.
	h.report("heat", 7)
	h.wantKinds("off")
	h.report("off", 7)

	h.advance(time.Hour)
	if got := *h.f.sent[1].Mode; got != ModeHeat {
		t.Fatalf("restored %s, want heat", got)
	}
}

func TestAiringWithUnknownModeSendsOffAndRestoresHeat(t *testing.T) {
	h := newHarness(t)
	h.startAiring(time.Minute)
	h.wantKinds("off")
	h.report("off", 7)
	h.advance(time.Minute)
	if got := *h.f.sent[1].Mode; got != ModeHeat {
		t.Fatalf("restored %s, want heat", got)
	}
	if got := h.f.sent[1].Target; got != nil {
		t.Fatalf("restored target %v, want none known", *got)
	}
	h.report("heat", 19)
	if got := h.status().AiringState; got != AiringNone {
		t.Fatalf("state = %s, want none", got)
	}
}

func TestSecondFlipDuringAiringEndsIt(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(time.Hour)
	h.report("off", 7)

	h.report("heat", 20)
	h.wantKinds("off", "off")
	h.report("off", 7)

	h.report("heat", 21)
	h.wantKinds("off", "off")
	s := h.status()
	if s.AiringState != AiringNone || s.AiringUntil != nil {
		t.Fatalf("status = %+v, want airing over", s)
	}
	if got := h.target(); got != 21 {
		t.Fatalf("target = %v, want the knob's 21", got)
	}
	h.advance(2 * time.Hour)
	h.wantKinds("off", "off")
}

func TestReportBeforeOffTookEffectIsNotAFlip(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(time.Hour)
	h.report("heat", 20)
	h.report("heat", 20)
	h.wantKinds("off")
	if got := h.status().AiringUntil; got == nil {
		t.Fatal("airing ended")
	}
}

func TestLostOffIsRetriedAndKeepsGoing(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(24 * time.Hour)

	for i := 0; i < maxAttempts; i++ {
		h.advance(retryInterval)
	}
	sent := len(h.f.sent)
	if sent != maxAttempts {
		t.Fatalf("sent %d, want %d", sent, maxAttempts)
	}
	if !h.status().CommandFailed {
		t.Fatal("the failure isn't visible")
	}
	h.advance(retryInterval)
	if len(h.f.sent) != sent {
		t.Fatal("kept retrying quickly")
	}
	h.advance(slowRetryInterval)
	if len(h.f.sent) != sent+1 {
		t.Fatal("stopped retrying")
	}
	h.report("off", 7)
	if h.status().CommandFailed {
		t.Fatal("still shown as failed once confirmed")
	}
}

func TestLostRestoreKeepsGoing(t *testing.T) {
	h := newHarness(t)
	h.report("heat", 20)
	h.startAiring(time.Minute)
	h.report("off", 7)
	h.advance(time.Minute)

	for i := 0; i < maxAttempts; i++ {
		h.advance(retryInterval)
	}
	if !h.status().CommandFailed {
		t.Fatal("the failure isn't visible")
	}
	sent := len(h.f.sent)
	h.advance(slowRetryInterval)
	if len(h.f.sent) != sent+1 {
		t.Fatal("stopped retrying")
	}
	h.report("heat", 20)
	s := h.status()
	if s.CommandFailed || s.AiringState != AiringNone {
		t.Fatalf("status = %+v", s)
	}
}

// The thermostat can go off by itself, so only a tap turns it back on.
func TestRoomThatIsOffIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	h.report("off", 7)
	h.advance(time.Hour)
	h.wantKinds()
	if got := h.status().AiringState; got != AiringNone {
		t.Fatalf("state = %s, want none", got)
	}
}

func TestSensorTemperatureWinsOverLocal(t *testing.T) {
	h := newHarness(t)
	h.c.HandleThermostatState("trv-office", ThermostatState{LocalTemperature: ptr(22.0)})
	if got := h.status().Temperature; got == nil || *got != 22 {
		t.Fatalf("temperature = %v, want the local 22", got)
	}
	h.c.HandleRoomTemperature("sensor-office", 21)
	if got := h.status().Temperature; got == nil || *got != 21 {
		t.Fatalf("temperature = %v, want the sensor's 21", got)
	}
}

func TestLastSeenIsSet(t *testing.T) {
	h := newHarness(t)
	if h.status().LastSeen != nil {
		t.Fatal("lastSeen before any report")
	}
	h.report("heat", 20)
	if got := h.status().LastSeen; got == nil || !got.Equal(h.now) {
		t.Fatalf("lastSeen = %v", got)
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

// Run is the only place that publishes, so reports and reads never wait for it.
func TestRunDoesntBlockReportsDuringAPublish(t *testing.T) {
	block := &blockingThermostat{entered: make(chan struct{}), release: make(chan struct{})}
	c := New(&config.Config{
		MinTarget: 10,
		MaxTarget: 25,
		Rooms:     []config.Room{{ID: "office", Label: "Office", TRV: "trv-office"}},
	}, block)
	c.HandleThermostatState("trv-office", state("heat", 20))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()
	if err := c.SetTarget("office", 21); err != nil {
		t.Fatal(err)
	}
	<-block.entered

	finished := make(chan struct{})
	go func() {
		c.HandleThermostatState("trv-office", state("heat", 20))
		c.Rooms()
		if err := c.SetTarget("office", 22); err != nil {
			t.Error(err)
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("waited for the publish")
	}
	close(block.release)
	cancel()
	<-done
}

type blockingThermostat struct {
	once             sync.Once
	entered, release chan struct{}
}

func (b *blockingThermostat) Apply(string, Command) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}
