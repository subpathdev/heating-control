package mqtt

import "testing"

func TestParseThermostat(t *testing.T) {
	s, err := parseThermostat([]byte(`{"system_mode":"off","occupied_heating_setpoint":7,"local_temperature":22,"running_state":"idle","frost_protection_temperature":7,"battery":96}`))
	if err != nil {
		t.Fatal(err)
	}
	if *s.Mode != "off" || *s.Setpoint != 7 || *s.LocalTemperature != 22 || *s.Running != "idle" || *s.FrostProtection != 7 {
		t.Fatalf("parsed %+v", s)
	}
}

func TestParseThermostatMissingFieldsAreNil(t *testing.T) {
	s, err := parseThermostat([]byte(`{"battery":96}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != nil || s.Setpoint != nil {
		t.Fatalf("parsed %+v", s)
	}
}

func TestParseTemperature(t *testing.T) {
	got, err := parseTemperature([]byte(`{"temperature":19.5,"humidity":50}`))
	if err != nil || got == nil || *got != 19.5 {
		t.Fatalf("got %v, %v", got, err)
	}
	got, err = parseTemperature([]byte(`{"humidity":50}`))
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}
