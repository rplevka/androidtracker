package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/roman/kidstracker/internal/domain"
)

func TestPostgresPostGISRoundTrip(t *testing.T) {
	connectionString := os.Getenv("DATABASE_URL")
	if connectionString == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, err := Open(ctx, connectionString)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	postgres := repository
	id := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	defer func() { _, _ = postgres.pool.Exec(context.Background(), "DELETE FROM devices WHERE id = $1", id) }()
	config := domain.DefaultConfig()
	codeSum := sha256.Sum256([]byte("enrollment-" + id))
	tokenSum := sha256.Sum256([]byte("token-" + id))
	codeHash, tokenHash := hex.EncodeToString(codeSum[:]), hex.EncodeToString(tokenSum[:])
	device, err := repository.CreateDevice(ctx, id, "Integration test", codeHash, config)
	if err != nil {
		t.Fatal(err)
	}
	if !device.Config.Enabled || device.Config.Version != 1 {
		t.Fatalf("unexpected initial device config: %#v", device.Config)
	}
	device, err = repository.Enroll(ctx, codeHash, tokenHash)
	if err != nil || !device.Activated {
		t.Fatalf("enroll: device=%#v err=%v", device, err)
	}
	if _, err := repository.DeviceByToken(ctx, tokenHash); err != nil {
		t.Fatalf("device token lookup: %v", err)
	}
	batteryHeartbeat, chargingHeartbeat := 72, false
	if err := repository.RecordHeartbeat(ctx, id, &batteryHeartbeat, &chargingHeartbeat); err != nil {
		t.Fatal(err)
	}
	clientConfigVersion := device.Config.Version
	applied, err := repository.AcknowledgeConfig(ctx, id, clientConfigVersion)
	if err != nil || !applied {
		t.Fatalf("ack initial config: applied=%v err=%v", applied, err)
	}
	telemetryRecordedAt := time.Now().UTC()
	telemetryBattery, telemetryQueue := 72, 2
	telemetryTemp, telemetryWake := 29.5, 12
	telemetrySync := int64(150)
	screenOn, deviceLocked, deviceIdle, powerSave, gpsEnabled := false, true, true, false, true
	standbyBucket, networkType := "rare", "wifi"
	if err := repository.RecordTelemetry(ctx, id, domain.Telemetry{
		RecordedAt: telemetryRecordedAt, BatteryPercent: &telemetryBattery, Charging: &chargingHeartbeat,
		ScreenOn: &screenOn, DeviceLocked: &deviceLocked, DeviceIdle: &deviceIdle,
		PowerSaveMode: &powerSave, StandbyBucket: &standbyBucket, NetworkType: &networkType,
		GPSEnabled: &gpsEnabled, BatteryTemperatureC: &telemetryTemp, WakeDelaySeconds: &telemetryWake,
		SyncDurationMS: &telemetrySync, QueueSize: &telemetryQueue, ConfigVersion: &clientConfigVersion,
	}); err != nil {
		t.Fatal(err)
	}
	telemetryHistory, err := repository.TelemetryHistory(ctx, id,
		telemetryRecordedAt.Add(-time.Minute), telemetryRecordedAt.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(telemetryHistory) != 1 || telemetryHistory[0].ScreenOn == nil || *telemetryHistory[0].ScreenOn ||
		telemetryHistory[0].WakeDelaySeconds == nil || *telemetryHistory[0].WakeDelaySeconds != telemetryWake {
		t.Fatalf("telemetry round trip failed: %#v", telemetryHistory)
	}

	point := domain.Location{ID: "fix-1", Latitude: 51.5, Longitude: -0.12,
		AccuracyM: 9, RecordedAt: time.Now().UTC()}
	battery := 73
	charging := false
	gpsFixDuration := int64(2300)
	locationSource := "network"
	point.BatteryPercent = &battery
	point.Charging = &charging
	point.ScreenOn = &screenOn
	point.DeviceLocked = &deviceLocked
	point.DeviceIdle = &deviceIdle
	point.PowerSaveMode = &powerSave
	point.StandbyBucket = &standbyBucket
	point.NetworkType = &networkType
	point.GPSFixDurationMS = &gpsFixDuration
	point.LocationSource = &locationSource
	if err := repository.RecordLocations(ctx, id, []domain.Location{point, point}); err != nil {
		t.Fatal(err)
	}
	history, err := repository.LocationHistory(ctx, id, point.RecordedAt.Add(-time.Minute), point.RecordedAt.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != point.ID || history[0].BatteryPercent == nil || *history[0].BatteryPercent != battery ||
		history[0].GPSFixDurationMS == nil || *history[0].GPSFixDurationMS != gpsFixDuration ||
		history[0].LocationSource == nil || *history[0].LocationSource != locationSource ||
		history[0].ScreenOn == nil || *history[0].ScreenOn {
		t.Fatalf("expected duplicate retry to persist one point with telemetry, got %#v", history)
	}
	devices, err := repository.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range devices {
		if item.ID == id {
			found = item.CurrentLocation != nil && item.CurrentLocation.Latitude == point.Latitude &&
				item.CurrentLocation.BatteryPercent != nil && *item.CurrentLocation.BatteryPercent == battery &&
				item.CurrentLocation.Charging != nil && !*item.CurrentLocation.Charging &&
				item.CurrentLocation.LocationSource != nil && *item.CurrentLocation.LocationSource == locationSource &&
				item.BatteryPercent != nil && *item.BatteryPercent == batteryHeartbeat &&
				item.AppliedConfig != nil && item.AppliedConfig.Version == clientConfigVersion && item.AppliedAt != nil
		}
	}
	if !found {
		t.Fatal("current location not returned for device")
	}
	config.Enabled = false
	updated, err := repository.UpdateConfig(ctx, id, config)
	if err != nil || updated.Config.Enabled || updated.Config.Version != 2 {
		t.Fatalf("update config: device=%#v err=%v", updated, err)
	}
	loaded, err := repository.GetConfig(ctx, id)
	if err != nil || loaded.Enabled || loaded.Version != 2 {
		t.Fatalf("get config: config=%#v err=%v", loaded, err)
	}
	if applied, err := repository.AcknowledgeConfig(ctx, id, clientConfigVersion); err != nil || applied {
		t.Fatalf("stale config acknowledgement should stay pending: applied=%v err=%v", applied, err)
	}
	devices, err = repository.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range devices {
		if item.ID == id && (item.AppliedConfig == nil || item.AppliedConfig.Version != 1 || item.Config.Version != 2) {
			t.Fatalf("stale client config should remain pending: desired=%#v applied=%#v", item.Config, item.AppliedConfig)
		}
	}
	clientConfigVersion = 2
	if applied, err := repository.AcknowledgeConfig(ctx, id, clientConfigVersion); err != nil || !applied {
		t.Fatalf("current config acknowledgement: applied=%v err=%v", applied, err)
	}
	settings := domain.DefaultMQTTSettings()
	settings.Enabled = true
	settings.Host = "mqtt.test"
	settings.Port = 1884
	settings.Username = "ha"
	settings.DiscoveryPrefix = "ha"
	settings.BaseTopic = "kidstracker-test"
	if err := repository.SaveMQTTSettings(ctx, settings, []byte("encrypted-password")); err != nil {
		t.Fatal(err)
	}
	loadedSettings, err := repository.GetMQTTSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loadedSettings.Settings.Host != "mqtt.test" || loadedSettings.Settings.Port != 1884 ||
		loadedSettings.Settings.DiscoveryPrefix != "ha" || string(loadedSettings.PasswordCiphertext) != "encrypted-password" {
		t.Fatalf("MQTT settings round trip failed: %#v", loadedSettings)
	}
	if err := repository.RevokeDevice(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DeviceByToken(ctx, tokenHash); err != ErrNotFound {
		t.Fatalf("revoked token should no longer authenticate, got %v", err)
	}
	if err := repository.DeleteDevice(ctx, id); err != nil {
		t.Fatal(err)
	}
	var devicesRemaining, locationsRemaining int
	if err := postgres.pool.QueryRow(ctx, `SELECT count(*) FROM devices WHERE id = $1`, id).Scan(&devicesRemaining); err != nil {
		t.Fatal(err)
	}
	if err := postgres.pool.QueryRow(ctx, `SELECT count(*) FROM locations WHERE device_id = $1`, id).Scan(&locationsRemaining); err != nil {
		t.Fatal(err)
	}
	if devicesRemaining != 0 || locationsRemaining != 0 {
		t.Fatalf("device delete did not cascade: devices=%d locations=%d", devicesRemaining, locationsRemaining)
	}
}
