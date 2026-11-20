package mqtt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/roman/kidstracker/internal/domain"
)

func testDevice() domain.Device {
	return domain.Device{ID: "abc123", Name: "Backpack", Config: domain.DefaultConfig()}
}

func TestDiscoveryPublishesGPSTrackerAndSensors(t *testing.T) {
	settings := domain.DefaultMQTTSettings()
	settings.BaseTopic = "kidstracker"
	messages := discoveryMessages(settings, testDevice())
	if len(messages) < 20 {
		t.Fatalf("expected a full set of entities, got %d", len(messages))
	}
	var tracker map[string]any
	for topic, payload := range messages {
		if strings.HasPrefix(topic, "homeassistant/device_tracker/") {
			if err := json.Unmarshal([]byte(payload), &tracker); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if tracker == nil {
		t.Fatal("device_tracker discovery missing")
	}
	if tracker["source_type"] != "gps" {
		t.Fatalf("source_type = %v, want gps", tracker["source_type"])
	}
	if tracker["json_attributes_topic"] != "kidstracker/abc123/location" {
		t.Fatalf("json_attributes_topic = %v", tracker["json_attributes_topic"])
	}
	if tracker["availability_topic"] != "kidstracker/status" {
		t.Fatalf("availability_topic = %v", tracker["availability_topic"])
	}
	var sourceSensor map[string]any
	for topic, payload := range messages {
		if strings.HasSuffix(topic, "_location_source/config") {
			if err := json.Unmarshal([]byte(payload), &sourceSensor); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if sourceSensor == nil {
		t.Fatal("location source sensor discovery missing")
	}
	if sourceSensor["state_topic"] != "kidstracker/abc123/location" ||
		sourceSensor["value_template"] != "{{ value_json.location_source }}" {
		t.Fatalf("unexpected location source sensor discovery: %#v", sourceSensor)
	}
	device, ok := tracker["device"].(map[string]any)
	if !ok || device["name"] != "Backpack" {
		t.Fatalf("device metadata missing: %#v", tracker["device"])
	}
}

func TestDiscoveryCanBeDisabledIndependentlyFromState(t *testing.T) {
	settings := domain.DefaultMQTTSettings()
	settings.DiscoveryEnabled = false
	device := testDevice()
	if messages := discoveryMessages(settings, device); len(messages) == 0 {
		t.Fatal("payload builder should still be deterministic")
	}
	topics := TopicsFor(settings, device.ID)
	if topics.Location != "kidstracker/abc123/location" {
		t.Fatalf("location topic = %q", topics.Location)
	}
}

func TestDiscoveryPublishesZoneStateAndPerZonePresence(t *testing.T) {
	settings := domain.DefaultMQTTSettings()
	settings.BaseTopic = "kidstracker"
	device := testDevice()
	device.Zones = []domain.Zone{{ID: "zone123", Name: "School", Kind: domain.ZoneKindSchool}}
	messages := discoveryMessages(settings, device)
	topics := TopicsFor(settings, device.ID)
	var zoneSensor, zonePresence map[string]any
	for topic, payload := range messages {
		if strings.HasSuffix(topic, "_zone/config") {
			if err := json.Unmarshal([]byte(payload), &zoneSensor); err != nil {
				t.Fatal(err)
			}
		}
		if strings.HasSuffix(topic, "_zone_zone123/config") {
			if err := json.Unmarshal([]byte(payload), &zonePresence); err != nil {
				t.Fatal(err)
			}
		}
	}
	if zoneSensor == nil || zonePresence == nil {
		t.Fatalf("zone discovery entities missing: %#v", messages)
	}
	if zoneSensor["state_topic"] != topics.Zone || zoneSensor["value_template"] != "{{ value_json.zone }}" {
		t.Fatalf("unexpected zone sensor: %#v", zoneSensor)
	}
	if zonePresence["state_topic"] != topics.Zone || zonePresence["device_class"] != "presence" ||
		!strings.Contains(zonePresence["value_template"].(string), "zone123") {
		t.Fatalf("unexpected zone presence sensor: %#v", zonePresence)
	}
}

func TestLocationAndTelemetryPayloadsContainMetrics(t *testing.T) {
	accuracy := 8.5
	battery := 74
	charging := false
	screen := false
	temperature := 29.5
	wake := 12
	sync := int64(180)
	queue := 3
	source := "network"
	state := locationState(domain.Location{
		Latitude: 51.5, Longitude: -0.12, AccuracyM: accuracy, BatteryPercent: &battery,
		Charging: &charging, ScreenOn: &screen, LocationSource: &source, RecordedAt: time.Now().UTC(),
	})
	if state["latitude"] != 51.5 || state["longitude"] != -0.12 || state["gps_accuracy"] != accuracy ||
		state["location_source"] != source {
		t.Fatalf("location state missing coordinates or source: %#v", state)
	}
	telemetry := telemetryState(domain.Telemetry{
		RecordedAt: time.Now().UTC(), ScreenOn: &screen, BatteryTemperatureC: &temperature,
		WakeDelaySeconds: &wake, SyncDurationMS: &sync, QueueSize: &queue,
	})
	if telemetry["battery_temperature_c"] != temperature || telemetry["wake_delay_seconds"] != wake {
		t.Fatalf("telemetry state missing metrics: %#v", telemetry)
	}
}

func TestTopicSanitization(t *testing.T) {
	topics := TopicsFor(domain.MQTTSettings{BaseTopic: "/my tracker/", DiscoveryPrefix: "ha/!"}, "a/b")
	if topics.Base != "my_tracker/a_b" {
		t.Fatalf("base = %q", topics.Base)
	}
	if topics.DiscoveryPath != "ha/homeassistant" {
		t.Fatalf("discovery path = %q", topics.DiscoveryPath)
	}
	if topics.ConfigSet != "my_tracker/a_b/config/set" {
		t.Fatalf("config set topic = %q", topics.ConfigSet)
	}
}

func TestDiscoveryPublishesWritableConfigControls(t *testing.T) {
	settings := domain.DefaultMQTTSettings()
	settings.BaseTopic = "kidstracker"
	messages := discoveryMessages(settings, testDevice())
	var switchConfig, numberConfig map[string]any
	for topic, payload := range messages {
		if strings.HasSuffix(topic, "_tracking_control/config") {
			if err := json.Unmarshal([]byte(payload), &switchConfig); err != nil {
				t.Fatal(err)
			}
		}
		if strings.HasSuffix(topic, "_location_interval_set/config") {
			if err := json.Unmarshal([]byte(payload), &numberConfig); err != nil {
				t.Fatal(err)
			}
		}
	}
	if switchConfig == nil || numberConfig == nil {
		t.Fatal("writable config discovery entities are missing")
	}
	if switchConfig["command_topic"] != "kidstracker/abc123/config/set" ||
		switchConfig["state_topic"] != "kidstracker/abc123/config" ||
		switchConfig["payload_on"] != `{"enabled":true}` ||
		switchConfig["payload_off"] != `{"enabled":false}` {
		t.Fatalf("unexpected tracking control discovery: %#v", switchConfig)
	}
	if numberConfig["command_topic"] != "kidstracker/abc123/config/set" ||
		numberConfig["state_topic"] != "kidstracker/abc123/config" ||
		numberConfig["command_template"] != `{"location_interval_seconds": {{ value | int }}}` ||
		numberConfig["min"] != float64(5) || numberConfig["max"] != float64(86400) {
		t.Fatalf("unexpected location interval control discovery: %#v", numberConfig)
	}
}
