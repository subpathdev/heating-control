// Package mqtt connects the controller to Zigbee2MQTT over MQTT.
package mqtt

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/subpathdev/heating-control/internal/config"
	"github.com/subpathdev/heating-control/internal/controller"
	"github.com/subpathdev/heating-control/internal/health"
)

const publishTimeout = 5 * time.Second

// Sink receives what the devices report.
type Sink interface {
	HandleThermostatState(trv string, s controller.ThermostatState) error
	HandleRoomTemperature(sensor string, t float64)
}

// Client implements controller.Thermostat.
type Client struct {
	cfg    *config.Config
	base   string
	client paho.Client
}

// New returns a Client. Nothing connects until Start.
func New(cfg *config.Config) *Client {
	return &Client{cfg: cfg, base: cfg.BaseTopic}
}

// Start connects in the background, keeps reconnecting and delivers device
// reports to sink. Because state messages aren't retained, it asks each
// thermostat for its state after every connect.
func (c *Client) Start(sink Sink) {
	opts := paho.NewClientOptions().
		AddBroker(c.cfg.Broker).
		SetClientID("heating-control").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(func(client paho.Client) { c.onConnect(sink, client) }).
		SetConnectionLostHandler(func(_ paho.Client, err error) { log.Printf("mqtt connection lost: %v", err) })
	c.client = paho.NewClient(opts)
	// With SetConnectRetry this token only finishes once connected, so don't wait.
	c.client.Connect()
}

// Close disconnects from the broker.
func (c *Client) Close() {
	c.client.Disconnect(250)
}

// Checker returns the health check for the broker connection. Call it after
// Start.
func (c *Client) Checker() health.Checker {
	return connectionChecker{conn: c.client}
}

// SetTarget implements controller.Thermostat.
func (c *Client) SetTarget(trv string, target float64) error {
	return c.publish(trv, "set", map[string]any{"occupied_heating_setpoint": target})
}

func (c *Client) publish(device, verb string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return waitForToken(c.client.Publish(fmt.Sprintf("%s/%s/%s", c.base, device, verb), 1, false, data), publishTimeout)
}

func (c *Client) onConnect(sink Sink, client paho.Client) {
	log.Printf("mqtt connected")
	for _, r := range c.cfg.Rooms {
		trv := r.TRV
		logSubscribeFailure(trv, client.Subscribe(c.base+"/"+trv, 0, func(_ paho.Client, m paho.Message) {
			s, err := parseThermostat(m.Payload())
			if err != nil {
				log.Printf("%s: %v", trv, err)
				return
			}
			if err := sink.HandleThermostatState(trv, s); err != nil {
				log.Printf("%s: %v", trv, err)
			}
		}))
		if r.Sensor != "" {
			sensor := r.Sensor
			logSubscribeFailure(sensor, client.Subscribe(c.base+"/"+sensor, 0, func(_ paho.Client, m paho.Message) {
				t, err := parseTemperature(m.Payload())
				if err != nil {
					log.Printf("%s: %v", sensor, err)
					return
				}
				if t != nil {
					sink.HandleRoomTemperature(sensor, *t)
				}
			}))
		}
		// Publishing from this handler must not wait for the token, or it blocks
		// the client's own message loop.
		go func() {
			if err := c.publish(trv, "get", map[string]string{
				"system_mode":                  "",
				"occupied_heating_setpoint":    "",
				"running_state":                "",
				"frost_protection_temperature": "",
			}); err != nil {
				log.Printf("%s: asking for state: %v", trv, err)
			}
		}()
	}
}

// logSubscribeFailure reports a failed subscription, which would otherwise
// leave a room silently out of date. It doesn't wait in the connect handler.
func logSubscribeFailure(device string, token waitToken) {
	go func() {
		if err := waitForToken(token, publishTimeout); err != nil {
			log.Printf("%s: subscribing: %v", device, err)
		}
	}()
}

type waitToken interface {
	WaitTimeout(time.Duration) bool
	Error() error
}

func waitForToken(token waitToken, timeout time.Duration) error {
	if !token.WaitTimeout(timeout) {
		return errors.New("timed out")
	}
	return token.Error()
}

func parseThermostat(payload []byte) (controller.ThermostatState, error) {
	var m struct {
		SystemMode      *string  `json:"system_mode"`
		Setpoint        *float64 `json:"occupied_heating_setpoint"`
		LocalTemp       *float64 `json:"local_temperature"`
		RunningState    *string  `json:"running_state"`
		FrostProtection *float64 `json:"frost_protection_temperature"`
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		return controller.ThermostatState{}, err
	}
	s := controller.ThermostatState{
		Setpoint:         m.Setpoint,
		LocalTemperature: m.LocalTemp,
		FrostProtection:  m.FrostProtection,
	}
	if m.SystemMode != nil {
		mode, err := controller.ParseMode(*m.SystemMode)
		if err != nil {
			return controller.ThermostatState{}, err
		}
		s.Mode = &mode
	}
	if m.RunningState != nil {
		heating := *m.RunningState == "heat"
		s.Heating = &heating
	}
	return s, nil
}

func parseTemperature(payload []byte) (*float64, error) {
	var m struct {
		Temperature *float64 `json:"temperature"`
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, err
	}
	return m.Temperature, nil
}
