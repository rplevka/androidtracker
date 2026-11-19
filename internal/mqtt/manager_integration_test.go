package mqtt

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/roman/kidstracker/internal/domain"
)

func mqttTestBroker(t *testing.T) (string, int) {
	t.Helper()
	broker := os.Getenv("MQTT_TEST_BROKER")
	if broker == "" {
		t.Skip("MQTT_TEST_BROKER is not set")
	}
	host, portText, err := net.SplitHostPort(broker)
	if err != nil {
		t.Fatalf("MQTT_TEST_BROKER must be host:port: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("MQTT_TEST_BROKER has invalid port")
	}
	return host, port
}

func TestManagerPublishesDiscoveryAndStates(t *testing.T) {
	host, port := mqttTestBroker(t)
	broker := net.JoinHostPort(host, strconv.Itoa(port))

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

	controller := &fakeConfigController{device: device}
	manager.SetConfigController(controller)
	token = subscriber.Publish(base+"/"+device.ID+"/config/set", 1, false, `{"sync_interval_seconds":300}`)
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatalf("config command publish failed: %v", token.Error())
	}
	waitForPayload(t, &mu, messages, base+"/"+device.ID+"/config", `"sync_interval_seconds":300`, 5*time.Second)
	calls, deviceID, patch, _ := controller.snapshot()
	if calls != 1 || deviceID != device.ID || patch.SyncIntervalSeconds == nil || *patch.SyncIntervalSeconds != 300 {
		t.Fatalf("config command was not applied: calls=%d id=%s patch=%#v", calls, deviceID, patch)
	}
}

func TestManagerReconnectsAfterBrokerOutage(t *testing.T) {
	brokerHost, brokerPort := mqttTestBroker(t)
	broker := net.JoinHostPort(brokerHost, strconv.Itoa(brokerPort))
	proxy := newTCPProxy(t, broker)

	var mu sync.Mutex
	messages := make(map[string]string)
	subscriber := paho.NewClient(paho.NewClientOptions().
		AddBroker("tcp://" + broker).
		SetClientID("kidstracker-reconnect-subscriber-" + strconv.FormatInt(time.Now().UnixNano(), 10)).
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

	proxyHost, proxyPortText, err := net.SplitHostPort(proxy.Addr())
	if err != nil {
		t.Fatal(err)
	}
	proxyPort, err := strconv.Atoi(proxyPortText)
	if err != nil {
		t.Fatal(err)
	}

	base := "kidstracker-reconnect"
	manager := NewManager(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer manager.Configure(domain.MQTTSettings{}, "")
	if err := manager.Configure(domain.MQTTSettings{
		Enabled: true, Host: proxyHost, Port: proxyPort, DiscoveryEnabled: true,
		DiscoveryPrefix: "homeassistant-reconnect", BaseTopic: base,
	}, ""); err != nil {
		t.Fatal(err)
	}
	waitForConnectionState(t, manager, true, time.Time{}, 10*time.Second)

	device := domain.Device{ID: "reconnect-device", Name: "Reconnect tracker", Config: domain.DefaultConfig()}
	topic := base + "/" + device.ID + "/location"
	manager.PublishLocation(device, reconnectLocation(51.5))
	waitForPayload(t, &mu, messages, topic, `"latitude":51.5`, 5*time.Second)
	// Let the QoS 1 publish receive its PUBACK before breaking the connection.
	// Paho v1.5.1 has a known benign race when resuming an in-flight packet
	// during reconnect, which the race detector can flag (paho issue #703).
	time.Sleep(250 * time.Millisecond)

	proxy.SetBlocked(true)
	waitForConnectionState(t, manager, false, time.Time{}, 10*time.Second)
	time.Sleep(2 * time.Second)

	outageAt := time.Now().UTC()
	proxy.SetBlocked(false)
	waitForConnectionState(t, manager, true, outageAt, 20*time.Second)

	manager.PublishLocation(device, reconnectLocation(48.2))
	waitForPayload(t, &mu, messages, topic, `"latitude":48.2`, 5*time.Second)
	time.Sleep(250 * time.Millisecond)
	if manager.Status().LastError != "" {
		t.Fatalf("unexpected MQTT error after reconnect: %s", manager.Status().LastError)
	}
}

func reconnectLocation(latitude float64) domain.Location {
	return domain.Location{
		ID: "fix", Latitude: latitude, Longitude: 16.6, AccuracyM: 5,
		RecordedAt: time.Now().UTC(),
	}
}

func waitForConnectionState(t *testing.T, manager *Manager, connected bool, after time.Time, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := manager.Status()
		connectedAfter := true
		if !after.IsZero() {
			connectedAfter = status.LastConnectedAt != nil && status.LastConnectedAt.After(after)
		}
		if status.Connected == connected && connectedAfter {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("MQTT did not reach connected=%t after %s: %#v", connected, timeout, manager.Status())
}

func waitForPayload(t *testing.T, mu *sync.Mutex, messages map[string]string, topic, contains string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		payload, exists := messages[topic]
		mu.Unlock()
		if exists && strings.Contains(payload, contains) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	payload := messages[topic]
	mu.Unlock()
	t.Fatalf("did not receive %q on %s: %q", contains, topic, payload)
}

type tcpProxy struct {
	target   string
	listener net.Listener

	mu      sync.Mutex
	blocked bool
	conns   map[net.Conn]struct{}
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &tcpProxy{target: target, listener: listener, conns: make(map[net.Conn]struct{})}
	go proxy.serve()
	t.Cleanup(proxy.Close)
	return proxy
}

func (p *tcpProxy) Addr() string {
	return p.listener.Addr().String()
}

func (p *tcpProxy) SetBlocked(blocked bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = blocked
	if blocked {
		for conn := range p.conns {
			conn.Close()
		}
		p.conns = make(map[net.Conn]struct{})
	}
}

func (p *tcpProxy) Close() {
	p.listener.Close()
	p.SetBlocked(true)
}

func (p *tcpProxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		blocked := p.blocked
		if !blocked {
			p.conns[client] = struct{}{}
		}
		p.mu.Unlock()
		if blocked {
			client.Close()
			continue
		}
		go p.handle(client)
	}
}

func (p *tcpProxy) handle(client net.Conn) {
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		client.Close()
		p.remove(client)
		return
	}
	closeBoth := func() {
		client.Close()
		upstream.Close()
	}
	go func() {
		_, _ = io.Copy(upstream, client)
		closeBoth()
	}()
	_, _ = io.Copy(client, upstream)
	closeBoth()
	p.remove(client)
}

func (p *tcpProxy) remove(conn net.Conn) {
	p.mu.Lock()
	delete(p.conns, conn)
	p.mu.Unlock()
}
