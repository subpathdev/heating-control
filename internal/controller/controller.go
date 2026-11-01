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
//   - It can go off on its own and return by itself, so "off" doesn't mean
//     this program turned it off.
package controller

import (
	"errors"
	"fmt"
	"math"
	"sync"

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
}

// Controller is safe for concurrent use.
type Controller struct {
	mu         sync.Mutex
	thermostat Thermostat
	min, max   float64
	rooms      []*room
}

// New returns a Controller for the configured rooms.
func New(cfg *config.Config, thermostat Thermostat) *Controller {
	c := &Controller{thermostat: thermostat, min: cfg.MinTarget, max: cfg.MaxTarget}
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

// SetTarget changes a room's target, or holds it while the thermostat is off.
func (c *Controller) SetTarget(id string, target float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	r := c.find(id)
	if r == nil {
		return fmt.Errorf("%w: %q", ErrUnknownRoom, id)
	}
	// NaN fails both comparisons, so test for the valid range.
	if !(target >= c.min && target <= c.max) {
		return fmt.Errorf("%w: %v is not within %v..%v", ErrTargetOutOfRange, target, c.min, c.max)
	}
	target = math.Round(target*2) / 2

	if r.mode == ModeOff {
		r.pending = &target
		return nil
	}
	if err := c.thermostat.SetTarget(r.cfg.TRV, target); err != nil {
		return err
	}
	r.target = &target
	r.pending = nil
	return nil
}

// HandleThermostatState records a thermostat's report.
func (c *Controller) HandleThermostatState(trv string, s ThermostatState) error {
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
		return nil
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
	if s.Setpoint != nil && r.mode != ModeOff && !r.isFrost(*s.Setpoint) {
		// Someone may have turned the knob, so the thermostat wins.
		r.target = s.Setpoint
	}

	if r.pending != nil && r.mode != ModeUnknown && r.mode != ModeOff {
		if err := c.thermostat.SetTarget(r.cfg.TRV, *r.pending); err != nil {
			return err
		}
		r.target = r.pending
		r.pending = nil
	}
	return nil
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

// isFrost reports whether a setpoint is the frost protection value. The
// default of 7 °C applies until the thermostat has reported its own.
func (r *room) isFrost(setpoint float64) bool {
	frost := 7.0
	if r.frost != nil {
		frost = *r.frost
	}
	return setpoint == frost
}
