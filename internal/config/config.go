// Package config loads the room list and broker settings.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Room links a thermostat to the sensor that shows the room's temperature.
type Room struct {
	ID     string `yaml:"id"`
	Label  string `yaml:"label"`
	TRV    string `yaml:"trv"`
	Sensor string `yaml:"sensor"`
}

// Config is the program's configuration file.
type Config struct {
	// StateFile is where airing and held targets are saved. Its folder must be
	// writable and survive restarts.
	StateFile string `yaml:"stateFile"`
	Broker    string `yaml:"broker"`
	// ClientID must be unique on the broker: a second client with the same ID
	// disconnects the first.
	ClientID  string  `yaml:"clientId"`
	BaseTopic string  `yaml:"baseTopic"`
	MinTarget float64 `yaml:"minTarget"`
	MaxTarget float64 `yaml:"maxTarget"`
	Rooms     []Room  `yaml:"rooms"`
	// AllowedHosts lists the host names the page is reached by. IP addresses and
	// localhost always work, so it's only needed for a DNS name.
	AllowedHosts []string `yaml:"allowedHosts"`
}

// Load reads and validates the file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

func parse(data []byte) (*Config, error) {
	cfg := Config{
		ClientID:  "heating-control",
		BaseTopic: "zigbee2mqtt",
		MinTarget: 10,
		MaxTarget: 25,
		StateFile: "/var/lib/heating-control/state.json",
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Broker == "" {
		return nil, fmt.Errorf("broker is required")
	}
	if cfg.MinTarget >= cfg.MaxTarget {
		return nil, fmt.Errorf("minTarget %v must be below maxTarget %v", cfg.MinTarget, cfg.MaxTarget)
	}
	if len(cfg.Rooms) == 0 {
		return nil, fmt.Errorf("at least one room is required")
	}
	seen := map[string]bool{}
	seenTRV := map[string]bool{}
	for _, r := range cfg.Rooms {
		if r.ID == "" || r.Label == "" || r.TRV == "" {
			return nil, fmt.Errorf("room %q needs id, label and trv", r.ID)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate room id %q", r.ID)
		}
		seen[r.ID] = true
		if seenTRV[r.TRV] {
			return nil, fmt.Errorf("thermostat %q is in more than one room", r.TRV)
		}
		seenTRV[r.TRV] = true
	}
	return &cfg, nil
}
