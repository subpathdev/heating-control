package controller

import (
	"log"
	"time"

	"github.com/subpathdev/heating-control/internal/config"
)

const (
	// retryInterval is how long a thermostat gets to confirm a command. A real
	// TRVZB took about 2 s, including the reports it sent with the old value.
	retryInterval = 30 * time.Second
	// maxAttempts at retryInterval give about 5 minutes. Then a target gives up.
	maxAttempts = 10
	// slowRetryInterval keeps turning a room off or back on after that, because
	// giving up would leave it heating with the window open, or cold.
	slowRetryInterval = 5 * time.Minute
	// maxFlips is how often a thermostat may leave off during airing. The first
	// time could be its own pause ending, so off is sent again. The second time
	// someone at the radiator wants heat, so airing ends.
	maxFlips = 2
	// defaultFrost applies until the thermostat has reported its own.
	defaultFrost = 7.0
)

// Command is what to send to a thermostat. Nil fields stay as they are.
type Command struct {
	Mode   *Mode
	Target *float64
}

// Kind names the command for logs and metrics.
func (c Command) Kind() string {
	switch {
	case c.Mode != nil && *c.Mode == ModeOff:
		return "off"
	case c.Mode != nil:
		return "restore"
	default:
		return "set_target"
	}
}

type room struct {
	cfg config.Room

	// What the thermostat and sensor report.
	mode       Mode
	setpoint   *float64
	heating    bool
	localTemp  *float64
	sensorTemp *float64
	frost      *float64
	lastSeen   *time.Time
	lastOnMode Mode
	// settled is set once it reports its own setpoint after being off. Until
	// then a write could be overwritten by it restoring that setpoint.
	settled bool

	// What the room should be doing. Saved.
	target      *float64
	targetDirty bool
	airingUntil *time.Time
	restoreMode Mode
	restoring   bool

	// How delivery is going.
	attempts int
	lastSent time.Time
	// publishFailed means the last command never reached the broker, so it waits
	// for retryInterval without counting as an attempt.
	publishFailed bool
	offConfirmed  bool
	flips         int
	targetFailed  bool
}

func newRoom(cfg config.Room, rec RoomRecord) *room {
	return &room{
		cfg:         cfg,
		target:      rec.Target,
		targetDirty: rec.TargetDirty,
		airingUntil: rec.AiringUntil,
		restoreMode: rec.RestoreMode,
		restoring:   rec.Restoring,
	}
}

func (r *room) record() RoomRecord {
	return RoomRecord{
		Target:      r.target,
		TargetDirty: r.targetDirty,
		AiringUntil: r.airingUntil,
		RestoreMode: r.restoreMode,
		Restoring:   r.restoring,
	}
}

// decide returns the command the room needs now, if any. It changes nothing.
func (r *room) decide(now time.Time) (Command, bool) {
	var cmd Command
	switch {
	case r.airingUntil != nil:
		if r.mode == ModeOff {
			return cmd, false
		}
		off := ModeOff
		cmd.Mode = &off
	case r.restoring:
		mode := r.restoreMode
		cmd.Mode = &mode
		cmd.Target = r.target
	case r.targetDirty && r.canWrite() && r.settled:
		cmd.Target = r.target
	default:
		return cmd, false
	}
	return cmd, r.due(now)
}

func (r *room) due(now time.Time) bool {
	if r.attempts == 0 && !r.publishFailed {
		return true
	}
	wait := retryInterval
	if r.attempts >= maxAttempts {
		wait = slowRetryInterval
	}
	return !now.Before(r.lastSent.Add(wait))
}

// advance applies what time alone changes, and reports whether anything to save
// changed.
func (r *room) advance(now time.Time) bool {
	switch {
	case r.airingUntil != nil && !now.Before(*r.airingUntil):
		r.endAiring()
		return true
	case r.targetDirty && r.airingUntil == nil && !r.restoring && r.canWrite() &&
		r.attempts >= maxAttempts && !now.Before(r.lastSent.Add(retryInterval)):
		// A knob turn and a lost write look the same, so the thermostat wins and
		// the page says so.
		log.Printf("%s: target not confirmed after %d attempts", r.cfg.TRV, r.attempts)
		r.targetDirty = false
		r.targetFailed = true
		r.attempts = 0
		if r.setpoint != nil && !r.isFrost(*r.setpoint) {
			r.target = r.setpoint
		}
		return true
	}
	return false
}

// report records a thermostat's report and reports whether anything to save
// changed.
func (r *room) report(s ThermostatState, now time.Time) bool {
	r.lastSeen = &now
	if s.Heating != nil {
		r.heating = *s.Heating
	}
	if s.LocalTemperature != nil {
		r.localTemp = s.LocalTemperature
	}
	if s.FrostProtection != nil {
		r.frost = s.FrostProtection
	}
	if s.Setpoint != nil {
		r.setpoint = s.Setpoint
	}

	changed := false
	if s.Mode != nil {
		r.mode = *s.Mode
		if r.canWrite() {
			r.lastOnMode = r.mode
		}
		if r.mode == ModeOff {
			r.settled = false
		}
		changed = r.reportAiringMode()
	}
	if r.setpoint == nil {
		return changed
	}
	own := r.canWrite() && !r.isFrost(*r.setpoint)
	if own {
		r.settled = true
	}

	switch {
	case r.airingUntil != nil:
	case r.restoring:
		if r.mode == r.restoreMode && (r.target == nil || *r.setpoint == *r.target) {
			r.restoring = false
			r.targetDirty = false
			r.attempts = 0
			changed = true
		}
	case r.targetDirty:
		if *r.setpoint == *r.target {
			r.targetDirty = false
			r.attempts = 0
			changed = true
		}
	case own && (r.target == nil || *r.setpoint != *r.target):
		// Someone turned the knob.
		r.target = r.setpoint
		r.targetFailed = false
		changed = true
	}
	return changed
}

// reportAiringMode handles a mode report during airing.
func (r *room) reportAiringMode() bool {
	if r.airingUntil == nil {
		return false
	}
	if r.mode == ModeOff {
		if !r.offConfirmed {
			r.attempts = 0
		}
		r.offConfirmed = true
		return false
	}
	if r.mode == ModeUnknown || !r.offConfirmed {
		return false
	}
	r.offConfirmed = false
	r.attempts = 0
	r.flips++
	if r.flips < maxFlips {
		return false
	}
	// The thermostat is on because someone wants it on, so nothing to restore.
	log.Printf("%s: left off %d times, ending airing", r.cfg.TRV, r.flips)
	r.airingUntil = nil
	r.restoring = false
	r.flips = 0
	return true
}

func (r *room) setTarget(target float64) {
	r.target = &target
	r.targetDirty = true
	r.targetFailed = false
	// During airing this mustn't reset the count for the off command.
	if r.airingUntil == nil && !r.restoring {
		r.attempts = 0
		r.publishFailed = false
	}
}

func (r *room) startAiring(until time.Time) {
	if r.airingUntil == nil && !r.restoring {
		r.restoreMode = r.lastOnMode
		if r.restoreMode == ModeUnknown {
			r.restoreMode = ModeHeat
		}
	}
	if r.airingUntil == nil {
		r.attempts = 0
		r.publishFailed = false
		r.flips = 0
		r.offConfirmed = r.mode == ModeOff
	}
	r.restoring = false
	r.airingUntil = &until
}

func (r *room) endAiring() {
	r.airingUntil = nil
	r.restoring = true
	r.attempts = 0
	r.publishFailed = false
	r.flips = 0
	r.offConfirmed = false
}

func (r *room) status() RoomStatus {
	s := RoomStatus{
		ID:          r.cfg.ID,
		Label:       r.cfg.Label,
		Temperature: r.sensorTemp,
		Target:      r.target,
		TargetState: r.targetState(),
		Mode:        r.mode,
		Heating:     r.heating,
		AiringState: AiringNone,
		AiringUntil: r.airingUntil,
		LastSeen:    r.lastSeen,
	}
	if s.Temperature == nil {
		s.Temperature = r.localTemp
	}
	switch {
	case r.airingUntil != nil && r.offConfirmed:
		s.AiringState = AiringActive
	case r.airingUntil != nil:
		s.AiringState = AiringStarting
		s.CommandFailed = r.attempts >= maxAttempts
	case r.restoring:
		s.AiringState = AiringRestoring
		s.CommandFailed = r.attempts >= maxAttempts
	}
	return s
}

func (r *room) targetState() TargetState {
	switch {
	case r.targetDirty && r.airingUntil == nil && !r.restoring && r.canWrite():
		return TargetSending
	case r.targetDirty:
		return TargetHeld
	case r.targetFailed:
		return TargetFailed
	default:
		return TargetApplied
	}
}

// canWrite reports whether a setpoint write is safe. Writing to a thermostat
// that is off, or may be, can switch it back on.
func (r *room) canWrite() bool {
	return r.mode != ModeOff && r.mode != ModeUnknown
}

func (r *room) isFrost(setpoint float64) bool {
	frost := defaultFrost
	if r.frost != nil {
		frost = *r.frost
	}
	return setpoint == frost
}
