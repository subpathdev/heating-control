package config

import "testing"

func TestParseDefaults(t *testing.T) {
	cfg, err := parse([]byte(`
broker: tcp://mosquitto:1883
rooms:
  - {id: office, label: Office, trv: trv-office, sensor: temperature-sensor-office}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseTopic != "zigbee2mqtt" || cfg.MinTarget != 10 || cfg.MaxTarget != 25 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestParseRejects(t *testing.T) {
	tests := map[string]string{
		"no broker": `rooms: [{id: a, label: A, trv: t}]`,
		"no rooms":  `broker: x`,
		"no trv":    `{broker: x, rooms: [{id: a, label: A}]}`,
		"duplicate": `{broker: x, rooms: [{id: a, label: A, trv: t}, {id: a, label: B, trv: u}]}`,
		"bad range": `{broker: x, minTarget: 25, maxTarget: 10, rooms: [{id: a, label: A, trv: t}]}`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parse([]byte(in)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
