// Package metrics serves the program's own Prometheus metrics. Temperatures and
// thermostat fields are left out, because mqtt-exporter already has them.
package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/subpathdev/heating-control/internal/controller"
	"github.com/subpathdev/heating-control/internal/health"
)

const checkTimeout = 2 * time.Second

// Rooms provides the room states to report.
type Rooms interface {
	Rooms() []controller.RoomStatus
}

// Metrics owns the registry, so only what is added here gets exposed.
type Metrics struct {
	registry *prometheus.Registry
	commands *prometheus.CounterVec
}

// New returns Metrics with the Go runtime, process and build metrics.
func New(version string) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		commands: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "heating_control_commands_total",
			Help: "Commands sent to thermostats, by thermostat, command and result.",
		}, []string{"trv", "command", "result"}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.commands,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "heating_control_build_info",
			Help:        "Always 1, with the running version as a label.",
			ConstLabels: prometheus.Labels{"version": version},
		}, func() float64 { return 1 }),
	)
	return m
}

// AddChecker reports a health check as heating_control_check_up{check}.
func (m *Metrics) AddChecker(c health.Checker) {
	m.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "heating_control_check_up",
		Help:        "1 if the health check passes, otherwise 0.",
		ConstLabels: prometheus.Labels{"check": c.Name()},
	}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		if c.Check(ctx) != nil {
			return 0
		}
		return 1
	}))
}

// AddRooms reports each room's target. It's the target the user chose, unlike
// the setpoint a thermostat reports, which is its frost temperature while off.
func (m *Metrics) AddRooms(rooms Rooms) {
	m.registry.MustRegister(roomCollector{rooms: rooms})
}

// Thermostat returns next with its commands counted.
func (m *Metrics) Thermostat(next controller.Thermostat) controller.Thermostat {
	return countingThermostat{next: next, commands: m.commands}
}

// Handler serves the metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

type countingThermostat struct {
	next     controller.Thermostat
	commands *prometheus.CounterVec
}

func (t countingThermostat) Apply(trv string, cmd controller.Command) error {
	err := t.next.Apply(trv, cmd)
	result := "ok"
	if err != nil {
		result = "error"
	}
	t.commands.WithLabelValues(trv, cmd.Kind(), result).Inc()
	return err
}

var targetDesc = prometheus.NewDesc(
	"heating_control_room_target_celsius",
	"The target temperature chosen for a room. Missing until it is known.",
	[]string{"room"}, nil,
)

var airingDesc = prometheus.NewDesc(
	"heating_control_room_airing",
	"1 while a room is airing, including until the thermostat confirms it's back on.",
	[]string{"room"}, nil,
)

var commandFailedDesc = prometheus.NewDesc(
	"heating_control_room_command_failed",
	"1 while the thermostat hasn't confirmed a command to turn off or heat again.",
	[]string{"room"}, nil,
)

type roomCollector struct {
	rooms Rooms
}

func (c roomCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- targetDesc
	ch <- airingDesc
	ch <- commandFailedDesc
}

func (c roomCollector) Collect(ch chan<- prometheus.Metric) {
	for _, r := range c.rooms.Rooms() {
		if r.Target != nil {
			ch <- prometheus.MustNewConstMetric(targetDesc, prometheus.GaugeValue, *r.Target, r.ID)
		}
		ch <- prometheus.MustNewConstMetric(airingDesc, prometheus.GaugeValue, boolValue(r.AiringState != controller.AiringNone), r.ID)
		ch <- prometheus.MustNewConstMetric(commandFailedDesc, prometheus.GaugeValue, boolValue(r.CommandFailed), r.ID)
	}
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
