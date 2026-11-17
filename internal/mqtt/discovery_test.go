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

func TestLocationAndTelemetryPayloadsContainMetrics(t *testing.T) {
	accuracy := 8.5
	battery := 74
	charging := false
	screen := false
	temperature := 29.5
	wake := 12
	sync := int64(180)
	queue := 3
	state := locationState(domain.Location{
		Latitude: 51.5, Longitude: -0.12, AccuracyM: accuracy, BatteryPercent: &battery,
		Charging: &charging, ScreenOn: &screen, RecordedAt: time.Now().UTC(),
	})
	if state["latitude"] != 51.5 || state["longitude"] != -0.12 || state["gps_accuracy"] != accuracy {
		t.Fatalf("location state missing coordinates: %#v", state)
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
}
