package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roman/kidstracker/internal/domain"
	"github.com/roman/kidstracker/internal/store"
)

type fakeRepository struct {
	devices        []domain.Device
	config         domain.Config
	locations      []domain.Location
	lastID         string
	deletedID      string
	battery        *int
	charging       *bool
	appliedVersion *int64
	telemetry      []domain.Telemetry
	mqttRecord     domain.MQTTRecord
	users          map[string]domain.User
	sessions       map[string]domain.Session
}

func (f *fakeRepository) Ping(context.Context) error { return nil }
func (f *fakeRepository) CreateDevice(_ context.Context, id, name, _ string, config domain.Config) (domain.Device, error) {
	f.lastID, f.config = id, config
	return domain.Device{ID: id, Name: name, Config: config}, nil
}
func (f *fakeRepository) Enroll(_ context.Context, _, _ string) (domain.Device, error) {
	return domain.Device{ID: "device-1", Activated: true, Config: f.config}, nil
}
func (f *fakeRepository) DeviceByToken(_ context.Context, token string) (domain.Device, error) {
	if token != digest("device-token") {
		return domain.Device{}, store.ErrNotFound
	}
	return domain.Device{ID: "device-1"}, nil
}
func (f *fakeRepository) RecordHeartbeat(_ context.Context, _ string, battery *int, charging *bool) error {
	f.battery, f.charging = battery, charging
	return nil
}
func (f *fakeRepository) AcknowledgeConfig(_ context.Context, _ string, version int64) (bool, error) {
	f.appliedVersion = &version
	return true, nil
}
func (f *fakeRepository) RecordTelemetry(_ context.Context, _ string, telemetry domain.Telemetry) error {
	f.telemetry = append(f.telemetry, telemetry)
	return nil
}
func (f *fakeRepository) RecordLocations(_ context.Context, id string, locations []domain.Location) error {
	f.lastID, f.locations = id, locations
	return nil
}
func (f *fakeRepository) GetConfig(context.Context, string) (domain.Config, error) {
	return f.config, nil
}
func (f *fakeRepository) ListDevices(context.Context) ([]domain.Device, error) { return f.devices, nil }
func (f *fakeRepository) UpdateConfig(_ context.Context, id string, config domain.Config) (domain.Device, error) {
	config.Version++
	f.config = config
	return domain.Device{ID: id, Config: config}, nil
}
func (f *fakeRepository) RevokeDevice(context.Context, string) error { return nil }
func (f *fakeRepository) DeleteDevice(_ context.Context, id string) error {
	f.deletedID = id
	return nil
}
func (f *fakeRepository) LocationHistory(context.Context, string, time.Time, time.Time, int) ([]domain.Location, error) {
	return f.locations, nil
}
func (f *fakeRepository) TelemetryHistory(context.Context, string, time.Time, time.Time, int) ([]domain.Telemetry, error) {
	return f.telemetry, nil
}
func (f *fakeRepository) GetMQTTSettings(context.Context) (domain.MQTTRecord, error) {
	if f.mqttRecord.Settings.Host == "" {
		f.mqttRecord.Settings = domain.DefaultMQTTSettings()
	}
	return f.mqttRecord, nil
}
func (f *fakeRepository) SaveMQTTSettings(_ context.Context, settings domain.MQTTSettings, passwordCiphertext []byte) error {
	f.mqttRecord.Settings = settings
	f.mqttRecord.PasswordCiphertext = passwordCiphertext
	return nil
}
func (f *fakeRepository) CreateUser(_ context.Context, id, username, displayName, passwordHash, role string) (domain.User, error) {
	if f.users == nil {
		f.users = make(map[string]domain.User)
	}
	if _, exists := f.users[username]; exists {
		return domain.User{}, store.ErrDuplicate
	}
	user := domain.User{ID: id, Username: username, DisplayName: displayName,
		PasswordHash: passwordHash, Role: role, CreatedAt: time.Now().UTC()}
	f.users[username] = user
	return user, nil
}
func (f *fakeRepository) UserByUsername(_ context.Context, username string) (domain.User, error) {
	user, exists := f.users[username]
	if !exists {
		return domain.User{}, store.ErrNotFound
	}
	return user, nil
}
func (f *fakeRepository) ListUsers(context.Context) ([]domain.User, error) {
	users := make([]domain.User, 0, len(f.users))
	for _, user := range f.users {
		users = append(users, user)
	}
	return users, nil
}
func (f *fakeRepository) MarkUserLogin(_ context.Context, userID string) error {
	for username, user := range f.users {
		if user.ID == userID {
			now := time.Now().UTC()
			user.LastLoginAt = &now
			f.users[username] = user
			return nil
		}
	}
	return store.ErrNotFound
}
func (f *fakeRepository) CreateSession(_ context.Context, tokenHash []byte, userID *string, role, label string, expiresAt time.Time) error {
	if f.sessions == nil {
		f.sessions = make(map[string]domain.Session)
	}
	f.sessions[string(tokenHash)] = domain.Session{UserID: userID, Role: role, Label: label, ExpiresAt: expiresAt}
	return nil
}
func (f *fakeRepository) SessionByToken(_ context.Context, tokenHash []byte) (domain.Session, error) {
	session, exists := f.sessions[string(tokenHash)]
	if !exists || time.Now().After(session.ExpiresAt) {
		return domain.Session{}, store.ErrNotFound
	}
	if session.UserID != nil {
		for _, user := range f.users {
			if user.ID == *session.UserID {
				session.Username = user.Username
				session.DisplayName = user.DisplayName
				return session, nil
			}
		}
		return domain.Session{}, store.ErrNotFound
	}
	return session, nil
}
func (f *fakeRepository) DeleteSession(_ context.Context, tokenHash []byte) error {
	delete(f.sessions, string(tokenHash))
	return nil
}

type fakeMQTT struct {
	settings  domain.MQTTSettings
	password  string
	status    domain.MQTTStatus
	devices   []string
	locations []domain.Location
	telemetry []domain.Telemetry
	configs   []string
	removed   []string
}

func (f *fakeMQTT) Configure(settings domain.MQTTSettings, password string) error {
	f.settings, f.password = settings, password
	return nil
}
func (f *fakeMQTT) Status() domain.MQTTStatus { return f.status }
func (f *fakeMQTT) PublishDevice(device domain.Device) {
	f.devices = append(f.devices, device.ID)
}
func (f *fakeMQTT) PublishLocation(_ domain.Device, location domain.Location) {
	f.locations = append(f.locations, location)
}
func (f *fakeMQTT) PublishTelemetry(_ domain.Device, telemetry domain.Telemetry) {
	f.telemetry = append(f.telemetry, telemetry)
}
func (f *fakeMQTT) PublishConfig(device domain.Device) { f.configs = append(f.configs, device.ID) }
func (f *fakeMQTT) RemoveDevice(id string)             { f.removed = append(f.removed, id) }

func TestVersionEndpointIsPublic(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	request := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"version":`) ||
		!strings.Contains(recorder.Body.String(), `"commit":`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeviceRoutesRequireBearerToken(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/v1/config"}, {http.MethodPost, "/v1/locations/batch"}} {
		t.Run(route.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`)))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestLocationBatchValidatesAndStoresIdempotentIDs(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	body := `{"locations":[{"id":"sample-1","latitude":51.5,"longitude":-0.12,"accuracy_meters":8,"location_source":"gps","recorded_at":"2025-01-01T12:00:00Z"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/locations/batch", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.lastID != "device-1" || len(repo.locations) != 1 || repo.locations[0].ID != "sample-1" ||
		repo.locations[0].LocationSource == nil || *repo.locations[0].LocationSource != "gps" {
		t.Fatalf("location was not stored for authenticated device: %#v", repo)
	}
}

func TestGzippedLocationBatchIsDecompressedAndStored(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	body := `{"locations":[{"id":"compressed-1","latitude":51.5,"longitude":-0.12,"accuracy_meters":8,"recorded_at":"2025-01-01T12:00:00Z"}]}`
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/locations/batch", &compressed)
	request.Header.Set("Authorization", "Bearer device-token")
	request.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || len(repo.locations) != 1 || repo.locations[0].ID != "compressed-1" {
		t.Fatalf("status = %d, body = %s, locations = %#v", recorder.Code, recorder.Body.String(), repo.locations)
	}
}

func TestUnsupportedRequestContentEncodingIsRejected(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	request := httptest.NewRequest(http.MethodPost, "/v1/locations/batch", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer device-token")
	request.Header.Set("Content-Encoding", "br")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnsupportedMediaType)
	}
}

func TestGzipExpandedBodyIsSizeLimited(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte(" "), (1<<20)+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := New(&fakeRepository{}, "admin-secret")
	request := httptest.NewRequest(http.MethodPost, "/v1/locations/batch", &compressed)
	request.Header.Set("Authorization", "Bearer device-token")
	request.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestLocationBatchRejectsInvalidCoordinates(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	body := `{"locations":[{"id":"bad","latitude":91,"longitude":0,"accuracy_meters":1,"recorded_at":"2025-01-01T12:00:00Z"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/locations/batch", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestLocationBatchRejectsInvalidBatteryPercentage(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	body := `{"locations":[{"id":"bad-battery","latitude":51,"longitude":0,"accuracy_meters":10,"battery_percent":101,"recorded_at":"2025-01-01T12:00:00Z"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/locations/batch", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestLocationBatchRejectsInvalidLocationSource(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	body := `{"locations":[{"id":"bad-source","latitude":51,"longitude":0,"accuracy_meters":10,"location_source":"cell tower","recorded_at":"2025-01-01T12:00:00Z"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/locations/batch", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAdminEndpointRequiresAdminToken(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/devices", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestEnrollRejectsInvalidAndAlreadyUsedCodes(t *testing.T) {
	repo := &enrollmentRepository{fakeRepository: fakeRepository{config: domain.DefaultConfig()}}
	server := New(repo, "admin-secret")
	request := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"enrollment_code":"this-code-has-enough-length"}`))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

type enrollmentRepository struct{ fakeRepository }

func (f *enrollmentRepository) Enroll(context.Context, string, string) (domain.Device, error) {
	return domain.Device{}, store.ErrInvalidEnrollment
}

func TestConfigEndpointReturnsDeviceConfiguration(t *testing.T) {
	config := domain.DefaultConfig()
	repo := &fakeRepository{config: config}
	server := New(repo, "admin-secret")
	request := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	request.Header.Set("Authorization", "Bearer device-token")
	request.Header.Set("X-Battery-Percent", "73")
	request.Header.Set("X-Charging", "false")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var got domain.Config
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.LocationIntervalSeconds != config.LocationIntervalSeconds || got.SyncIntervalSeconds != config.SyncIntervalSeconds {
		t.Fatalf("intervals = %d/%d, want %d/%d", got.LocationIntervalSeconds, got.SyncIntervalSeconds,
			config.LocationIntervalSeconds, config.SyncIntervalSeconds)
	}
	if repo.battery == nil || *repo.battery != 73 || repo.charging == nil || *repo.charging {
		t.Fatalf("heartbeat not recorded: battery=%v charging=%v", repo.battery, repo.charging)
	}
}

func TestDeviceCanAcknowledgeCurrentConfiguration(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	request := httptest.NewRequest(http.MethodPost, "/v1/config/ack", strings.NewReader(`{"version":1}`))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"applied":true`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeviceCanAcknowledgeConfigWithTelemetry(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	recordedAt := time.Now().UTC().Format(time.RFC3339)
	body := `{"version":1,"telemetry":{"recorded_at":"` + recordedAt + `","battery_percent":74,"charging":false,"screen_on":false,"device_locked":true,"device_idle":true,"power_save_mode":false,"standby_bucket":"rare","network_type":"wifi","gps_enabled":true,"battery_temperature_c":29.5,"wake_delay_seconds":7,"sync_duration_ms":120,"queue_size":2,"config_version":1}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/config/ack", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(repo.telemetry) != 1 || repo.telemetry[0].BatteryPercent == nil || *repo.telemetry[0].BatteryPercent != 74 ||
		repo.telemetry[0].ScreenOn == nil || *repo.telemetry[0].ScreenOn ||
		repo.telemetry[0].WakeDelaySeconds == nil || *repo.telemetry[0].WakeDelaySeconds != 7 {
		t.Fatalf("telemetry not recorded: %#v", repo.telemetry)
	}
}

func TestTelemetryRejectsInvalidValues(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	body := `{"version":1,"telemetry":{"recorded_at":"` + time.Now().UTC().Format(time.RFC3339) + `","battery_percent":101}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/config/ack", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAdminCanReadTelemetryHistory(t *testing.T) {
	battery := 61
	screenOn := false
	repo := &fakeRepository{telemetry: []domain.Telemetry{{
		RecordedAt: time.Now().UTC(), BatteryPercent: &battery, ScreenOn: &screenOn,
	}}}
	server := New(repo, "admin-secret")
	request := httptest.NewRequest(http.MethodGet, "/api/admin/devices/device-1/telemetry?limit=10", nil)
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"battery_percent":61`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminCanSaveAndReadMQTTSettings(t *testing.T) {
	repo := &fakeRepository{}
	publisher := &fakeMQTT{status: domain.MQTTStatus{Connected: true}}
	server := New(repo, "admin-secret", publisher)
	body := `{"enabled":true,"host":"mqtt.local","port":1883,"username":"ha","password":"broker-secret","tls":false,"discovery_enabled":true,"discovery_prefix":"homeassistant","base_topic":"kidstracker","client_id":"kidstracker-test"}`
	request := httptest.NewRequest(http.MethodPut, "/api/admin/mqtt", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if publisher.settings.Host != "mqtt.local" || publisher.password != "broker-secret" || !strings.Contains(recorder.Body.String(), `"password_set":true`) {
		t.Fatalf("settings not applied: %s", recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/mqtt", nil)
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"password_set":true`) ||
		strings.Contains(recorder.Body.String(), "broker-secret") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestMQTTSettingsRejectInvalidBrokerHost(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret", &fakeMQTT{})
	request := httptest.NewRequest(http.MethodPut, "/api/admin/mqtt", strings.NewReader(`{"enabled":true,"host":"http://mqtt.local","port":1883,"discovery_enabled":true,"discovery_prefix":"homeassistant","base_topic":"kidstracker"}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestDeviceEventsArePublishedToMQTT(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	publisher := &fakeMQTT{}
	server := New(repo, "admin-secret", publisher)

	request := httptest.NewRequest(http.MethodPost, "/v1/enroll", strings.NewReader(`{"enrollment_code":"this-code-has-enough-length"}`))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || len(publisher.devices) != 1 {
		t.Fatalf("enroll publish failed: status=%d devices=%v", recorder.Code, publisher.devices)
	}

	body := `{"locations":[{"id":"mqtt-location","latitude":51.5,"longitude":-0.12,"accuracy_meters":8,"recorded_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`
	request = httptest.NewRequest(http.MethodPost, "/v1/locations/batch", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || len(publisher.locations) != 1 {
		t.Fatalf("location publish failed: status=%d locations=%v", recorder.Code, publisher.locations)
	}

	body = `{"version":1,"telemetry":{"recorded_at":"` + time.Now().UTC().Format(time.RFC3339) + `","screen_on":false,"device_idle":true}}`
	request = httptest.NewRequest(http.MethodPost, "/v1/config/ack", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-token")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || len(publisher.telemetry) != 1 {
		t.Fatalf("telemetry publish failed: status=%d telemetry=%v", recorder.Code, publisher.telemetry)
	}
}

func TestConfigUpdateRejectsOutOfRangeIntervals(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/devices/device-1/config", strings.NewReader(`{"enabled":true,"location_interval_seconds":4,"sync_interval_seconds":300,"sleep_duration_seconds":3600,"gps_timeout_seconds":60,"min_distance_meters":0,"version":1}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAdminCanDeleteDevice(t *testing.T) {
	repo := &fakeRepository{}
	server := New(repo, "admin-secret")
	request := httptest.NewRequest(http.MethodDelete, "/api/admin/devices/device-1", nil)
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || repo.deletedID != "device-1" {
		t.Fatalf("status = %d, deleted device = %q, body = %s", recorder.Code, repo.deletedID, recorder.Body.String())
	}
}

func TestAdminCanCreateDeviceAndReceivesOneTimeCode(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	request := httptest.NewRequest(http.MethodPost, "/api/admin/devices", strings.NewReader(`{"name":"Backpack tracker"}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Device         domain.Device `json:"device"`
		EnrollmentCode string        `json:"enrollment_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Device.Name != "Backpack tracker" || response.EnrollmentCode == "" || repo.lastID == "" {
		t.Fatalf("unexpected create response: %#v", response)
	}
}

func TestAdminCanGenerateEnrollmentQR(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	body := `{"text":"0123456789abcdef0123456789abcdef0123456789abcdef"}`
	request := httptest.NewRequest(http.MethodPost, "/api/admin/enrollment-qr", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("status = %d, content-type = %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	image, err := png.Decode(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil || image.Bounds().Dx() != 400 || image.Bounds().Dy() != 400 {
		t.Fatalf("response is not a 400x400 PNG: image=%v err=%v", image, err)
	}
}

func TestEnrollmentQRRejectsInvalidEnrollmentCode(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	body := `{"text":"not-a-token"}`
	request := httptest.NewRequest(http.MethodPost, "/api/admin/enrollment-qr", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAdminCanUpdateValidConfiguration(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	body := `{"enabled":false,"location_interval_seconds":300,"sync_interval_seconds":900,"sleep_duration_seconds":7200,"gps_timeout_seconds":45,"min_distance_meters":25,"version":1}`
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/devices/device-1/config", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.config.Enabled || repo.config.LocationIntervalSeconds != 300 || repo.config.SyncIntervalSeconds != 900 || repo.config.Version != 2 {
		t.Fatalf("configuration was not applied: %#v", repo.config)
	}
}

func TestAdminCanPartiallyUpdateConfiguration(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	body := `{"enabled":false,"sync_interval_seconds":300}`
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/devices/device-1/config", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.config.Enabled || repo.config.SyncIntervalSeconds != 300 || repo.config.LocationIntervalSeconds != 900 ||
		repo.config.GPSTimeoutSeconds != 60 || repo.config.Version != 2 {
		t.Fatalf("partial configuration was not merged correctly: %#v", repo.config)
	}
}

func TestConfigUpdateRejectsInvalidPartialValue(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	body := `{"gps_timeout_seconds":1}`
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/devices/device-1/config", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if repo.config.GPSTimeoutSeconds != 60 {
		t.Fatalf("invalid partial update changed the config: %#v", repo.config)
	}
}

func TestConfigUpdateAcceptsRequestedMinimumPollingIntervals(t *testing.T) {
	repo := &fakeRepository{config: domain.DefaultConfig()}
	server := New(repo, "admin-secret")
	body := `{"enabled":true,"location_interval_seconds":5,"sync_interval_seconds":5,"sleep_duration_seconds":10,"gps_timeout_seconds":5,"min_distance_meters":0,"version":1}`
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/devices/device-1/config", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.config.LocationIntervalSeconds != 5 || repo.config.SyncIntervalSeconds != 5 || repo.config.SleepDurationSeconds != 10 {
		t.Fatalf("minimum polling intervals were not applied: %#v", repo.config)
	}
}

func TestAdminCanRevokeDevice(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	request := httptest.NewRequest(http.MethodPost, "/api/admin/devices/device-1/revoke", nil)
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "revoked") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHistoryEndpointAcceptsTimeRangeAndLimit(t *testing.T) {
	repo := &fakeRepository{locations: []domain.Location{{ID: "point-1"}}}
	server := New(repo, "admin-secret")
	request := httptest.NewRequest(http.MethodGet, "/api/admin/devices/device-1/locations?from=2025-01-01T00:00:00Z&to=2025-01-02T00:00:00Z&limit=10", nil)
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "point-1") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestJSONRequestRejectsUnknownFields(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	request := httptest.NewRequest(http.MethodPost, "/api/admin/devices", strings.NewReader(`{"name":"Tracker","unexpected":true}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestRandomTokensAreDifferent(t *testing.T) {
	first, err := randomToken(16)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomToken(16)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("random token collision")
	}
}

type loginResponse struct {
	Token string       `json:"token"`
	User  authUserView `json:"user"`
}

func loginRequest(t *testing.T, server *Server, body string) loginResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response loginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Token == "" {
		t.Fatal("login response did not contain a token")
	}
	return response
}

func TestAdminKeyLoginCreatesSessionAndLogoutRevokesIt(t *testing.T) {
	server := New(&fakeRepository{}, "admin-secret")
	login := loginRequest(t, server, `{"admin_key":"admin-secret"}`)
	if login.User.Role != domain.RoleAdmin || login.User.AuthType != domain.AuthTypeAdminKey {
		t.Fatalf("unexpected admin login user: %#v", login.User)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"role":"admin"`) {
		t.Fatalf("me status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestAdminCanCreateUserAndUserCanReadButNotAdminister(t *testing.T) {
	repo := &fakeRepository{devices: []domain.Device{{ID: "device-1", Name: "Tracker"}}}
	server := New(repo, "admin-secret")
	admin := loginRequest(t, server, `{"admin_key":"admin-secret"}`)

	request := httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(
		`{"username":"viewer","display_name":"Viewer","password":"correct horse battery","role":"user"}`))
	request.Header.Set("Authorization", "Bearer "+admin.Token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create user status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	request.Header.Set("Authorization", "Bearer admin-secret")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"username":"viewer"`) {
		t.Fatalf("list users status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	viewer := loginRequest(t, server, `{"username":"viewer","password":"correct horse battery"}`)
	if viewer.User.Role != domain.RoleUser || viewer.User.AuthType != domain.AuthTypePassword {
		t.Fatalf("unexpected viewer login user: %#v", viewer.User)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/devices", nil)
	request.Header.Set("Authorization", "Bearer "+viewer.Token)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("viewer devices status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/mqtt", ""},
		{http.MethodGet, "/api/admin/users", ""},
		{http.MethodPost, "/api/admin/users", `{"username":"other","password":"another long password","role":"user"}`},
	} {
		request = httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		request.Header.Set("Authorization", "Bearer "+viewer.Token)
		recorder = httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want %d", route.method, route.path, recorder.Code, http.StatusForbidden)
		}
	}

	request = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"viewer","password":"wrong password"}`))
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(
		`{"username":"viewer","display_name":"Duplicate","password":"another long password","role":"user"}`))
	request.Header.Set("Authorization", "Bearer "+admin.Token)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate user status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}
