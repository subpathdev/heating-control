// Package controller holds each room's state and decides what to send to its
// thermostat.
//
// Three behaviours of the Sonoff TRVZB shape this code. They were measured on
// a real device (firmware 1.3.0):
//   - In system_mode off it reports its frost protection temperature as the
//     setpoint, and briefly keeps reporting it after going back to heat. That
//     value is never a target the user chose.
//   - Writing a setpoint while it is off switches it back to heat, so a target
//     changed during that time is held until the thermostat heats again.
//   - After a write it can still send a few reports with the old setpoint.
//   - It can go off on its own and return by itself, so "off" doesn't mean
//     this program turned it off.
package controller

import (
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/subpathdev/heating-control/internal/config"
)

// Mode is a thermostat's system mode. The zero value means it hasn't reported.
type Mode string

// The modes a thermostat can report.
const (
	ModeUnknown Mode = ""
	ModeOff     Mode = "off"
	ModeAuto    Mode = "auto"
	ModeHeat    Mode = "heat"
)

// ParseMode converts a reported system mode. An unknown value is an error, so
// a typo or firmware change can't be mistaken for a thermostat that is heating.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeOff, ModeAuto, ModeHeat:
		return m, nil
	default:
		return ModeUnknown, fmt.Errorf("unknown system mode %q", s)
	}
}

// echoGrace is how long a thermostat gets to confirm a write. A real TRVZB took
// about 2 s, including the reports it sent with the old setpoint.
const echoGrace = 15 * time.Second

// ErrUnknownRoom is returned for a room that isn't configured.
var ErrUnknownRoom = errors.New("unknown room")

// ErrTargetOutOfRange is returned for a target outside the allowed range.
var ErrTargetOutOfRange = errors.New("target out of range")

// Thermostat sends commands to a physical thermostat.
type Thermostat interface {
	SetTarget(trv string, target float64) error
}

// ThermostatState is a thermostat's report. Fields it didn't include are nil.
type ThermostatState struct {
	Mode             *Mode
	Setpoint         *float64
	LocalTemperature *float64
	Heating          *bool
	FrostProtection  *float64
}

// RoomStatus is a room's current state.
type RoomStatus struct {
	ID          string
	Label       string
	Temperature *float64
	Target      *float64
	Mode        Mode
	Heating     bool
}

type room struct {
	cfg        config.Room
	sensorTemp *float64
	localTemp  *float64
	target     *float64
	pending    *float64
	mode       Mode
	heating    bool
	frost      *float64

	// expected is a target that was sent, and expectUntil how long reports with
	// another setpoint count as older than the write.
	expected    *float64
	expectUntil time.Time
	flushing    bool
}

// Controller is safe for concurrent use.
type Controller struct {
	// mu guards the rooms' state and is never held during a publish. rooms
	// itself and each room's cfg don't change after New.
	mu sync.Mutex
	// sendMu keeps commands in order. Device reports never take it.
	sendMu     sync.Mutex
	flushes    sync.WaitGroup
	thermostat Thermostat
	now        func() time.Time
	min, max   float64
	rooms      []*room
}

// New returns a Controller for the configured rooms.
func New(cfg *config.Config, thermostat Thermostat) *Controller {
	c := &Controller{thermostat: thermostat, now: time.Now, min: cfg.MinTarget, max: cfg.MaxTarget}
	for _, r := range cfg.Rooms {
		c.rooms = append(c.rooms, &room{cfg: r})
	}
	return c
}

// Limits returns the allowed target range.
func (c *Controller) Limits() (min, max float64) {
	return c.min, c.max
}

// Rooms returns the status of every room, in configured order.
func (c *Controller) Rooms() []RoomStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]RoomStatus, 0, len(c.rooms))
	for _, r := range c.rooms {
		s := RoomStatus{
			ID:          r.cfg.ID,
			Label:       r.cfg.Label,
			Temperature: r.sensorTemp,
			Target:      r.target,
			Mode:        r.mode,
			Heating:     r.heating,
		}
		if s.Temperature == nil {
			s.Temperature = r.localTemp
		}
		if r.pending != nil {
			s.Target = r.pending
		}
		out = append(out, s)
	}
	return out
}

// SetTarget changes a room's target. It holds the target while the thermostat
// is off or hasn't reported, because a write could switch it back on.
//
// The room's state is changed first and c.mu released before publishing: a
// device report needs c.mu, and the MQTT client can't read the broker's
// acknowledgement while it is delivering a report.
func (c *Controller) SetTarget(id string, target float64) error {
	r := c.find(id)
	if r == nil {
		return fmt.Errorf("%w: %q", ErrUnknownRoom, id)
	}
	// NaN fails both comparisons, so test for the valid range.
	if !(target >= c.min && target <= c.max) {
		return fmt.Errorf("%w: %v is not within %v..%v", ErrTargetOutOfRange, target, c.min, c.max)
	}
	target = math.Round(target*2) / 2

	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.mu.Lock()
	if !r.canWrite() {
		r.pending = &target
		c.mu.Unlock()
		return nil
	}
	previous := r.target
	r.pending = nil
	c.expect(r, target)
	c.mu.Unlock()

	if err := c.thermostat.SetTarget(r.cfg.TRV, target); err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		r.target = previous
		r.expected = nil
		return err
	}
	return nil
}

// HandleThermostatState records a thermostat's report. It's called while the
// MQTT client delivers a message, so it must not wait for a publish.
func (c *Controller) HandleThermostatState(trv string, s ThermostatState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var r *room
	for _, candidate := range c.rooms {
		if candidate.cfg.TRV == trv {
			r = candidate
			break
		}
	}
	if r == nil {
		return
	}

	if s.Mode != nil {
		r.mode = *s.Mode
	}
	if s.Heating != nil {
		r.heating = *s.Heating
	}
	if s.LocalTemperature != nil {
		r.localTemp = s.LocalTemperature
	}
	if s.FrostProtection != nil {
		r.frost = s.FrostProtection
	}
	if s.Setpoint == nil || r.mode == ModeOff || r.isFrost(*s.Setpoint) {
		return
	}

	// Reports sent before a write took effect still carry the old setpoint.
	// After the grace period the thermostat wins: someone may have turned the
	// knob, or the write was lost.
	if r.expected == nil || *s.Setpoint == *r.expected || !c.now().Before(r.expectUntil) {
		r.target = s.Setpoint
		r.expected = nil
	}

	// A thermostat back from off restores its own setpoint, so a held target
	// only goes out once that has happened, or the restore would overwrite it.
	if r.pending != nil && r.canWrite() && !r.flushing {
		r.flushing = true
		c.flushes.Add(1)
		go c.flush(r)
	}
}

// flush sends a held target. If that fails the target stays held, and the
// thermostat's next report tries again.
func (c *Controller) flush(r *room) {
	defer c.flushes.Done()

	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.mu.Lock()
	r.flushing = false
	if r.pending == nil || !r.canWrite() {
		c.mu.Unlock()
		return
	}
	target := *r.pending
	previous := r.target
	r.pending = nil
	c.expect(r, target)
	c.mu.Unlock()

	if err := c.thermostat.SetTarget(r.cfg.TRV, target); err != nil {
		log.Printf("%s: sending held target: %v", r.cfg.TRV, err)
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.pending == nil {
			r.pending = &target
		}
		r.target = previous
		r.expected = nil
	}
}

// expect records a target about to be sent. The caller holds c.mu.
func (c *Controller) expect(r *room, target float64) {
	r.target = &target
	r.expected = &target
	r.expectUntil = c.now().Add(echoGrace)
}

// Wait blocks until held targets being sent have finished.
func (c *Controller) Wait() {
	c.flushes.Wait()
}

// HandleRoomTemperature records a room sensor's report.
func (c *Controller) HandleRoomTemperature(sensor string, t float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, r := range c.rooms {
		if r.cfg.Sensor == sensor {
			r.sensorTemp = &t
		}
	}
}

func (c *Controller) find(id string) *room {
	for _, r := range c.rooms {
		if r.cfg.ID == id {
			return r
		}
	}
	return nil
}

// canWrite reports whether a setpoint write is safe. Writing to a thermostat
// that is off, or may be, can switch it back on.
func (r *room) canWrite() bool {
	return r.mode != ModeOff && r.mode != ModeUnknown
}

// isFrost reports whether a setpoint is the frost protection value. The
// default of 7 °C applies until the thermostat has reported its own.
func (r *room) isFrost(setpoint float64) bool {
	frost := 7.0
	if r.frost != nil {
		frost = *r.frost
	}
	return setpoint == frost
}
