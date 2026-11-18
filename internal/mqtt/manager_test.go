package mqtt

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/roman/kidstracker/internal/domain"
)

func TestClientOptionsUseBoundedPahoReconnect(t *testing.T) {
	manager := &Manager{}
	options, err := manager.clientOptions(domain.MQTTSettings{Host: "localhost", Port: 1883}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !options.AutoReconnect {
		t.Fatal("Paho auto-reconnect is disabled")
	}
	if !options.ConnectRetry {
		t.Fatal("initial connection retry is disabled")
	}
	if options.MaxReconnectInterval != 15*time.Second {
		t.Fatalf("MaxReconnectInterval = %s, want 15s", options.MaxReconnectInterval)
	}
	if options.ConnectRetryInterval != 5*time.Second {
		t.Fatalf("ConnectRetryInterval = %s, want 5s", options.ConnectRetryInterval)
	}
}

type fakeCommandMessage struct {
	topic    string
	payload  []byte
	retained bool
}

func (m fakeCommandMessage) Duplicate() bool   { return false }
func (m fakeCommandMessage) Topic() string     { return m.topic }
func (m fakeCommandMessage) MessageID() uint16 { return 0 }
func (m fakeCommandMessage) Payload() []byte   { return m.payload }
func (m fakeCommandMessage) Qos() byte         { return 0 }
func (m fakeCommandMessage) Retained() bool    { return m.retained }
func (m fakeCommandMessage) Ack()              {}

type fakeConfigController struct {
	mu       sync.Mutex
	calls    int
	deviceID string
	patch    domain.ConfigPatch
	device   domain.Device
	err      error
}

func (f *fakeConfigController) ApplyConfigPatch(_ context.Context, deviceID string, patch domain.ConfigPatch) (domain.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.deviceID = deviceID
	f.patch = patch
	if f.err != nil {
		return domain.Device{}, f.err
	}
	device := f.device
	device.ID = deviceID
	device.Config = patch.Apply(device.Config)
	device.Config.Version++
	f.device = device
	return device, nil
}

func (f *fakeConfigController) snapshot() (int, string, domain.ConfigPatch, domain.Device) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.deviceID, f.patch, f.device
}

func newCommandTestManager() *Manager {
	return &Manager{
		ctx:           context.Background(),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		devices:       make(map[string]domain.Device),
		configs:       make(map[string]domain.Config),
		commandTopics: map[string]string{"kidstracker/abc123/config/set": "abc123"},
		out:           make(chan job, 1),
		commands:      make(chan commandJob, 1),
	}
}

func TestManagerHandlesConfigCommandMessages(t *testing.T) {
	manager := newCommandTestManager()
	manager.handleCommandMessage(fakeCommandMessage{
		topic:   "kidstracker/abc123/config/set",
		payload: []byte(`{"sync_interval_seconds":300}`),
	})
	select {
	case command := <-manager.commands:
		if command.deviceID != "abc123" || command.patch.SyncIntervalSeconds == nil ||
			*command.patch.SyncIntervalSeconds != 300 {
			t.Fatalf("unexpected command: %#v", command)
		}
	default:
		t.Fatal("config command was not queued")
	}

	manager.handleCommandMessage(fakeCommandMessage{
		topic:    "kidstracker/abc123/config/set",
		payload:  []byte(`{"sync_interval_seconds":600}`),
		retained: true,
	})
	select {
	case command := <-manager.commands:
		t.Fatalf("retained command should be ignored: %#v", command)
	default:
	}
}

func TestManagerAppliesConfigCommandAndRepublishesState(t *testing.T) {
	manager := newCommandTestManager()
	controller := &fakeConfigController{device: domain.Device{ID: "abc123", Name: "Test", Config: domain.DefaultConfig()}}
	manager.SetConfigController(controller)
	syncSeconds := 300
	manager.applyCommand(commandJob{
		deviceID: "abc123",
		patch:    domain.ConfigPatch{SyncIntervalSeconds: &syncSeconds},
	})
	calls, deviceID, patch, device := controller.snapshot()
	if calls != 1 || deviceID != "abc123" || patch.SyncIntervalSeconds == nil || *patch.SyncIntervalSeconds != 300 {
		t.Fatalf("controller was not called correctly: calls=%d id=%s patch=%#v device=%#v", calls, deviceID, patch, device)
	}
	manager.mu.RLock()
	config := manager.configs["abc123"]
	manager.mu.RUnlock()
	if config.SyncIntervalSeconds != 300 || config.LocationIntervalSeconds != domain.DefaultConfig().LocationIntervalSeconds ||
		config.Version != device.Config.Version {
		t.Fatalf("manager did not retain the updated config: %#v", config)
	}
	select {
	case queued := <-manager.out:
		if queued.deviceID != "abc123" {
			t.Fatalf("wrong device queued: %#v", queued)
		}
	default:
		t.Fatal("updated config state was not queued for publishing")
	}
}
