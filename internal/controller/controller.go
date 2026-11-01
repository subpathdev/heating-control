// Package controller keeps what each room should be doing apart from what its
// thermostat reports, and sends commands until the two match.
//
// HTTP handlers and device reports only change state under mu and wake Run,
// which is the only place that talks to a thermostat or the Store. So nothing
// that delivers a report ever waits for a publish: the MQTT client couldn't read
// the broker's acknowledgement while it does.
//
// Four behaviours of the Sonoff TRVZB shape this code. They were measured on a
// real device (firmware 1.3.0):
//   - In system_mode off it reports its frost protection temperature as the
//     setpoint, and briefly keeps reporting it after going back to heat. That
//     value is never a target the user chose.
//   - Writing a setpoint while it is off switches it back to heat, so a target
//     changed during that time is held until it can be sent.
//   - After a write it can still send a few reports with the old setpoint.
//   - It can go off on its own and return by itself, so "off" doesn't mean this
//     program turned it off. Only rooms with a saved record are ever restored.
package controller

import (
	"context"
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

// TargetState says how far a room's target has got.
type TargetState string

// The states of a room's target.
const (
	TargetApplied TargetState = "applied"
	TargetSending TargetState = "sending"
	TargetHeld    TargetState = "held"
	TargetFailed  TargetState = "failed"
)

// AiringState says where a room is in airing.
type AiringState string

// The states of airing.
const (
	AiringNone      AiringState = "none"
	AiringStarting  AiringState = "starting"
	AiringActive    AiringState = "active"
	AiringRestoring AiringState = "restoring"
)

// ErrUnknownRoom is returned for a room that isn't configured.
var ErrUnknownRoom = errors.New("unknown room")

// ErrTargetOutOfRange is returned for a target outside the allowed range.
var ErrTargetOutOfRange = errors.New("target out of range")

// tickInterval is how often Run checks timeouts and retries without being woken.
const tickInterval = 5 * time.Second

// Thermostat sends commands to a physical thermostat.
type Thermostat interface {
	Apply(trv string, cmd Command) error
}

// Store saves what must survive a restart.
type Store interface {
	Load() (map[string]RoomRecord, error)
	Save(records map[string]RoomRecord) error
}

// RoomRecord is a room's saved state.
type RoomRecord struct {
	Target      *float64
	TargetDirty bool
	AiringUntil *time.Time
	RestoreMode Mode
	Restoring   bool
	LastOnMode  Mode
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
	ID            string
	Label         string
	Temperature   *float64
	Target        *float64
	TargetState   TargetState
	Mode          Mode
	Heating       bool
	AiringState   AiringState
	AiringUntil   *time.Time
	LastSeen      *time.Time
	CommandFailed bool
}

// Controller is safe for concurrent use.
type Controller struct {
	thermostat Thermostat
	store      Store
	now        func() time.Time
	min, max   float64
	wake       chan struct{}

	// mu guards the rooms' state and is never held during a publish or save.
	// rooms itself and each room's cfg don't change after New.
	mu      sync.Mutex
	rooms   []*room
	changed bool
}

// New returns a Controller for the configured rooms, with what store has saved.
// A store that can't be read is logged, and the rooms start empty.
func New(cfg *config.Config, thermostat Thermostat, store Store) *Controller {
	c := &Controller{
		thermostat: thermostat,
		store:      store,
		now:        time.Now,
		min:        cfg.MinTarget,
		max:        cfg.MaxTarget,
		wake:       make(chan struct{}, 1),
	}
	records, err := store.Load()
	if err != nil {
		log.Printf("loading saved state: %v", err)
	}
	for _, r := range cfg.Rooms {
		c.rooms = append(c.rooms, newRoom(r, records[r.ID]))
	}
	return c
}

// Run sends commands and saves state until ctx is done.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		c.tick()
		select {
		case <-ctx.Done():
			c.save()
			return
		case <-ticker.C:
		case <-c.wake:
		}
	}
}

// tick moves every room on by one step.
func (c *Controller) tick() {
	for _, r := range c.rooms {
		c.mu.Lock()
		now := c.now()
		if r.advance(now) {
			c.changed = true
		}
		cmd, send := r.decide(now)
		if send {
			r.attempts++
			r.lastSent = now
			r.sentTarget = cmd.Target
		}
		c.mu.Unlock()

		if !send {
			continue
		}
		// Saved first: after a crash a room that is off with no record would stay
		// off, because only rooms with a record get restored.
		c.save()
		if err := c.thermostat.Apply(r.cfg.TRV, cmd); err != nil {
			log.Printf("%s: %s: %v", r.cfg.TRV, cmd.Kind(), err)
			// Never reached the thermostat, so it says nothing about it.
			c.mu.Lock()
			r.attempts--
			r.publishFailed = true
			r.sentTarget = nil
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		r.publishFailed = false
		c.mu.Unlock()
	}
	c.save()
}

// save writes the rooms if they changed. A failed save tries again next tick.
func (c *Controller) save() {
	c.mu.Lock()
	if !c.changed {
		c.mu.Unlock()
		return
	}
	records := make(map[string]RoomRecord, len(c.rooms))
	for _, r := range c.rooms {
		records[r.cfg.ID] = r.record()
	}
	c.changed = false
	c.mu.Unlock()

	if err := c.store.Save(records); err != nil {
		log.Printf("saving state: %v", err)
		c.mu.Lock()
		c.changed = true
		c.mu.Unlock()
	}
}

func (c *Controller) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
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
		out = append(out, r.status())
	}
	return out
}

// SetTarget accepts a new target for a room. Run delivers it when it can.
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

	c.mu.Lock()
	r.setTarget(target)
	c.changed = true
	c.mu.Unlock()
	c.signal()
	return nil
}

// StartAiring stops heating a room for d. Called during airing, it sets a new
// end time.
func (c *Controller) StartAiring(id string, d time.Duration) error {
	r := c.find(id)
	if r == nil {
		return fmt.Errorf("%w: %q", ErrUnknownRoom, id)
	}
	c.mu.Lock()
	r.startAiring(c.now().Add(d))
	c.changed = true
	c.mu.Unlock()
	c.signal()
	return nil
}

// StopAiring ends airing now. If the room isn't airing but its thermostat is
// off, it turns it back on. Otherwise it does nothing.
func (c *Controller) StopAiring(id string) error {
	r := c.find(id)
	if r == nil {
		return fmt.Errorf("%w: %q", ErrUnknownRoom, id)
	}
	c.mu.Lock()
	if r.heatAgain(c.now()) {
		c.changed = true
	}
	c.mu.Unlock()
	c.signal()
	return nil
}

// HandleThermostatState records a thermostat's report.
func (c *Controller) HandleThermostatState(trv string, s ThermostatState) {
	c.mu.Lock()
	for _, r := range c.rooms {
		if r.cfg.TRV == trv {
			if r.report(s, c.now()) {
				c.changed = true
			}
			break
		}
	}
	c.mu.Unlock()
	c.signal()
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
