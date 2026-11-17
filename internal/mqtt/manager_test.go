package mqtt

import (
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
