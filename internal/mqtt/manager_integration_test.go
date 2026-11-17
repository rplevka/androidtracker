package mqtt

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/roman/kidstracker/internal/domain"
)

func TestManagerPublishesDiscoveryAndStates(t *testing.T) {
	broker := os.Getenv("MQTT_TEST_BROKER")
	if broker == "" {
		t.Skip("MQTT_TEST_BROKER is not set")
	}
	host, portText, ok := strings.Cut(broker, ":")
	if !ok {
		t.Fatalf("MQTT_TEST_BROKER must be host:port")
	}
	port := 0
	for _, char := range portText {
		if char < '0' || char > '9' {
			t.Fatalf("MQTT_TEST_BROKER has invalid port")
		}
		port = port*10 + int(char-'0')
	}

	var mu sync.Mutex
	messages := make(map[string]string)
	subscriber := paho.NewClient(paho.NewClientOptions().
		AddBroker("tcp://" + broker).
		SetClientID("kidstracker-integration-subscriber").
		SetCleanSession(true))
	token := subscriber.Connect()
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatalf("subscribe connect failed: %v", token.Error())
	}
	defer subscriber.Disconnect(250)
	token = subscriber.Subscribe("#", 1, func(_ paho.Client, message paho.Message) {
		mu.Lock()
		messages[message.Topic()] = string(message.Payload())
		mu.Unlock()
	})
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatalf("subscribe failed: %v", token.Error())
	}

	base := "kidstracker-integration"
	discoveryPrefix := "homeassistant-integration"
	manager := NewManager(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer manager.Configure(domain.MQTTSettings{}, "")
	if err := manager.Configure(domain.MQTTSettings{
		Enabled: true, Host: host, Port: port, DiscoveryEnabled: true,
		DiscoveryPrefix: discoveryPrefix, BaseTopic: base,
	}, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !manager.Status().Connected && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !manager.Status().Connected {
		t.Fatalf("manager did not connect: %#v", manager.Status())
	}

	device := domain.Device{ID: "integration-device", Name: "Integration tracker", Config: domain.DefaultConfig()}
	battery, temperature, wake, queue := 70, 28.5, 4, 1
	sync := int64(120)
	manager.PublishDevice(device)
	manager.PublishLocation(device, domain.Location{
		ID: "fix", Latitude: 51.5, Longitude: -0.12, AccuracyM: 7,
		BatteryPercent: &battery, RecordedAt: time.Now().UTC(),
	})
	manager.PublishTelemetry(device, domain.Telemetry{
		RecordedAt: time.Now().UTC(), BatteryTemperatureC: &temperature,
		WakeDelaySeconds: &wake, SyncDurationMS: &sync, QueueSize: &queue,
	})

	expected := []string{
		discoveryPrefix + "/device_tracker/kidstracker_" + device.ID + "_tracker/config",
		base + "/" + device.ID + "/location",
		base + "/" + device.ID + "/telemetry",
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		seen := 0
		for _, topic := range expected {
			if _, exists := messages[topic]; exists {
				seen++
			}
		}
		mu.Unlock()
		if seen == len(expected) {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			keys := make([]string, 0, len(messages))
			for topic := range messages {
				keys = append(keys, topic)
			}
			mu.Unlock()
			t.Fatalf("missing expected MQTT messages; seen %v", keys)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
