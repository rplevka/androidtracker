package mqtt

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/roman/kidstracker/internal/domain"
)

type job struct {
	deviceID string
}

type Manager struct {
	ctx    context.Context
	logger *slog.Logger

	mu              sync.RWMutex
	generation      uint64
	settings        domain.MQTTSettings
	password        string
	client          paho.Client
	connected       bool
	lastError       string
	lastConnectedAt *time.Time
	devices         map[string]domain.Device
	locations       map[string]domain.Location
	telemetry       map[string]domain.Telemetry
	configs         map[string]domain.Config
	discovered      map[string]bool
	out             chan job
}

func NewManager(ctx context.Context, logger *slog.Logger) *Manager {
	manager := &Manager{
		ctx:        ctx,
		logger:     logger,
		devices:    make(map[string]domain.Device),
		locations:  make(map[string]domain.Location),
		telemetry:  make(map[string]domain.Telemetry),
		configs:    make(map[string]domain.Config),
		discovered: make(map[string]bool),
		out:        make(chan job, 256),
	}
	go manager.run()
	return manager
}

func (m *Manager) Configure(settings domain.MQTTSettings, password string) error {
	m.mu.Lock()
	m.generation++
	generation := m.generation
	oldClient := m.client
	m.client = nil
	m.settings = settings
	m.password = password
	m.connected = false
	m.lastError = ""
	m.discovered = make(map[string]bool)
	m.mu.Unlock()
	if oldClient != nil {
		oldClient.Disconnect(250)
	}
	if !settings.Enabled {
		return nil
	}
	options, err := m.clientOptions(settings, password, generation)
	if err != nil {
		m.setError(err)
		return err
	}
	client := paho.NewClient(options)
	m.mu.Lock()
	if m.generation != generation {
		m.mu.Unlock()
		client.Disconnect(0)
		return nil
	}
	m.client = client
	m.mu.Unlock()
	go m.connectLoop(client, generation)
	return nil
}

func (m *Manager) clientOptions(settings domain.MQTTSettings, password string, generation uint64) (*paho.ClientOptions, error) {
	if strings.TrimSpace(settings.Host) == "" || settings.Port < 1 || settings.Port > 65535 {
		return nil, fmt.Errorf("invalid MQTT broker host or port")
	}
	scheme := "tcp"
	if settings.TLS {
		scheme = "ssl"
	}
	clientID := strings.TrimSpace(settings.ClientID)
	if clientID == "" {
		randomID := make([]byte, 4)
		if _, err := rand.Read(randomID); err != nil {
			return nil, fmt.Errorf("generate MQTT client ID: %w", err)
		}
		clientID = "kidstracker-backend-" + hex.EncodeToString(randomID)
	}
	options := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("%s://%s:%d", scheme, settings.Host, settings.Port)).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectTimeout(10*time.Second).
		SetConnectRetryInterval(5*time.Second).
		SetMaxReconnectInterval(15*time.Second).
		SetKeepAlive(30*time.Second).
		SetPingTimeout(10*time.Second).
		SetCleanSession(true).
		SetOrderMatters(false).
		SetWill(normalizeBaseTopic(settings.BaseTopic)+"/status", "offline", 1, true).
		SetOnConnectHandler(func(client paho.Client) {
			m.onConnected(client, generation)
		}).
		SetConnectionLostHandler(func(client paho.Client, err error) {
			m.onConnectionLost(client, generation, err)
		}).
		SetReconnectingHandler(func(client paho.Client, _ *paho.ClientOptions) {
			m.onReconnecting(client, generation)
		})
	if settings.Username != "" {
		options.SetUsername(settings.Username)
	}
	if password != "" {
		options.SetPassword(password)
	}
	if settings.TLS {
		options.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: settings.Host})
	}
	return options, nil
}

func (m *Manager) connectLoop(client paho.Client, generation uint64) {
	if !m.isCurrent(client, generation) {
		return
	}
	token := client.Connect()
	if token.WaitTimeout(15 * time.Second) {
		if token.Error() != nil {
			m.setError(token.Error())
			m.logger.Warn("MQTT initial connection failed", "error", token.Error())
		}
		return
	}
	m.setError(fmt.Errorf("MQTT initial connection is still pending; Paho will keep retrying"))
	m.logger.Warn("MQTT initial connection is still pending; Paho will keep retrying")
}

func (m *Manager) onReconnecting(client paho.Client, generation uint64) {
	m.mu.Lock()
	if m.generation != generation || m.client != client {
		m.mu.Unlock()
		return
	}
	m.connected = false
	if m.lastError == "" {
		m.lastError = "MQTT broker reconnecting"
	}
	m.mu.Unlock()
	m.logger.Warn("MQTT reconnecting")
}

func (m *Manager) isCurrent(client paho.Client, generation uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.generation == generation && m.client == client
}

func (m *Manager) onConnected(client paho.Client, generation uint64) {
	m.mu.Lock()
	if m.generation != generation || m.client != client {
		m.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	m.connected = true
	m.lastError = ""
	m.lastConnectedAt = &now
	m.discovered = make(map[string]bool)
	m.mu.Unlock()
	m.logger.Info("MQTT connected", "host", m.currentSettings().Host, "port", m.currentSettings().Port)
	m.publish([]string{normalizeBaseTopic(m.currentSettings().BaseTopic) + "/status"}, []byte("online"), true)
	m.enqueueAll()
}

func (m *Manager) onConnectionLost(client paho.Client, generation uint64, err error) {
	m.mu.Lock()
	if m.generation != generation || m.client != client {
		m.mu.Unlock()
		return
	}
	if client.IsConnectionOpen() {
		// A fast reconnect already restored the connection; ignore the stale callback.
		m.mu.Unlock()
		return
	}
	m.connected = false
	if err != nil {
		m.lastError = err.Error()
	}
	m.discovered = make(map[string]bool)
	m.mu.Unlock()
	if err != nil {
		m.logger.Warn("MQTT connection lost; Paho will reconnect", "error", err)
	} else {
		m.logger.Warn("MQTT connection lost; Paho will reconnect")
	}
}

func (m *Manager) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.lastError = err.Error()
	}
}

func (m *Manager) Status() domain.MQTTStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := domain.MQTTStatus{Connected: m.connected, LastError: m.lastError}
	if m.lastConnectedAt != nil {
		value := *m.lastConnectedAt
		status.LastConnectedAt = &value
	}
	return status
}

func (m *Manager) currentSettings() domain.MQTTSettings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

func (m *Manager) PublishDevice(device domain.Device) {
	m.mu.Lock()
	m.devices[device.ID] = device
	m.configs[device.ID] = device.Config
	m.mu.Unlock()
	m.enqueue(device.ID)
}

func (m *Manager) PublishLocation(device domain.Device, location domain.Location) {
	m.mu.Lock()
	m.devices[device.ID] = device
	m.locations[device.ID] = location
	m.mu.Unlock()
	m.enqueue(device.ID)
}

func (m *Manager) PublishTelemetry(device domain.Device, telemetry domain.Telemetry) {
	m.mu.Lock()
	m.devices[device.ID] = device
	m.telemetry[device.ID] = telemetry
	m.mu.Unlock()
	m.enqueue(device.ID)
}

func (m *Manager) PublishConfig(device domain.Device) {
	m.mu.Lock()
	m.devices[device.ID] = device
	m.configs[device.ID] = device.Config
	m.mu.Unlock()
	m.enqueue(device.ID)
}

func (m *Manager) RemoveDevice(deviceID string) {
	m.mu.Lock()
	device, exists := m.devices[deviceID]
	settings := m.settings
	if !exists {
		device = domain.Device{ID: deviceID, Name: deviceID}
	}
	m.mu.Unlock()
	if settings.Enabled && settings.DiscoveryEnabled {
		for topic := range discoveryMessages(settings, device) {
			m.publish([]string{topic}, nil, true)
		}
	}
	m.mu.Lock()
	delete(m.devices, deviceID)
	delete(m.locations, deviceID)
	delete(m.telemetry, deviceID)
	delete(m.configs, deviceID)
	delete(m.discovered, deviceID)
	m.mu.Unlock()
}

func (m *Manager) enqueue(deviceID string) {
	select {
	case m.out <- job{deviceID: deviceID}:
	default:
	}
}

func (m *Manager) enqueueAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.devices))
	for id := range m.devices {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		m.enqueue(id)
	}
}

func (m *Manager) run() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case item := <-m.out:
			m.publishDevice(item.deviceID)
		}
	}
}

func (m *Manager) publishDevice(deviceID string) {
	m.mu.RLock()
	device, hasDevice := m.devices[deviceID]
	location, hasLocation := m.locations[deviceID]
	telemetry, hasTelemetry := m.telemetry[deviceID]
	config, hasConfig := m.configs[deviceID]
	settings := m.settings
	client := m.client
	connected := m.connected
	alreadyDiscovered := m.discovered[deviceID]
	m.mu.RUnlock()
	if !hasDevice || client == nil || !connected || !settings.Enabled {
		return
	}
	topics := TopicsFor(settings, device.ID)
	if settings.DiscoveryEnabled && !alreadyDiscovered {
		discovered := true
		for topic, payload := range discoveryMessages(settings, device) {
			if !m.publishClient(client, topic, []byte(payload), true) {
				discovered = false
			}
		}
		if discovered {
			m.mu.Lock()
			m.discovered[deviceID] = true
			m.mu.Unlock()
		}
	}
	if hasLocation {
		if payload, err := json.Marshal(locationState(location)); err == nil {
			m.publishClient(client, topics.Location, payload, true)
		}
	}
	if hasTelemetry {
		if payload, err := json.Marshal(telemetryState(telemetry)); err == nil {
			m.publishClient(client, topics.Telemetry, payload, true)
		}
	}
	if hasConfig {
		if payload, err := json.Marshal(configState(config)); err == nil {
			m.publishClient(client, topics.Config, payload, true)
		}
	}
}

func (m *Manager) publish(topics []string, payload []byte, retain bool) {
	m.mu.RLock()
	client := m.client
	connected := m.connected
	settings := m.settings
	m.mu.RUnlock()
	if client == nil || !connected || !settings.Enabled {
		return
	}
	for _, topic := range topics {
		m.publishClient(client, topic, payload, retain)
	}
}

func (m *Manager) publishClient(client paho.Client, topic string, payload []byte, retain bool) bool {
	token := client.Publish(topic, 1, retain, payload)
	if !token.WaitTimeout(2 * time.Second) {
		m.setError(fmt.Errorf("MQTT publish timed out: %s", topic))
		return false
	}
	if token.Error() != nil {
		m.setError(token.Error())
		return false
	}
	return true
}
