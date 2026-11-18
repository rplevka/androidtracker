package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/roman/kidstracker/internal/domain"
	"github.com/roman/kidstracker/internal/secrets"
	"github.com/roman/kidstracker/internal/store"
	"github.com/skip2/go-qrcode"
)

type MQTTPublisher interface {
	Configure(domain.MQTTSettings, string) error
	Status() domain.MQTTStatus
	PublishDevice(domain.Device)
	PublishLocation(domain.Device, domain.Location)
	PublishTelemetry(domain.Device, domain.Telemetry)
	PublishConfig(domain.Device)
	RemoveDevice(string)
}

type Server struct {
	repo       store.Repository
	adminToken string
	mqtt       MQTTPublisher
	cipher     *secrets.Cipher
	mux        *http.ServeMux
}

func New(repo store.Repository, adminToken string, publishers ...MQTTPublisher) *Server {
	cipher, _ := secrets.New(adminToken)
	var publisher MQTTPublisher
	if len(publishers) > 0 {
		publisher = publishers[0]
	}
	s := &Server{repo: repo, adminToken: adminToken, mqtt: publisher, cipher: cipher, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("POST /v1/enroll", s.enroll)
	s.mux.HandleFunc("GET /v1/config", s.deviceAuth(s.getConfig))
	s.mux.HandleFunc("POST /v1/config/ack", s.deviceAuth(s.acknowledgeConfig))
	s.mux.HandleFunc("POST /v1/locations/batch", s.deviceAuth(s.recordLocations))
	s.mux.HandleFunc("GET /api/admin/devices", s.adminAuth(s.listDevices))
	s.mux.HandleFunc("POST /api/admin/devices", s.adminAuth(s.createDevice))
	s.mux.HandleFunc("DELETE /api/admin/devices/{id}", s.adminAuth(s.deleteDevice))
	s.mux.HandleFunc("POST /api/admin/enrollment-qr", s.adminAuth(s.createEnrollmentQR))
	s.mux.HandleFunc("POST /api/admin/devices/{id}/revoke", s.adminAuth(s.revokeDevice))
	s.mux.HandleFunc("PATCH /api/admin/devices/{id}/config", s.adminAuth(s.updateConfig))
	s.mux.HandleFunc("GET /api/admin/devices/{id}/locations", s.adminAuth(s.locationHistory))
	s.mux.HandleFunc("GET /api/admin/devices/{id}/telemetry", s.adminAuth(s.telemetryHistory))
	s.mux.HandleFunc("GET /api/admin/mqtt", s.adminAuth(s.getMQTTSettings))
	s.mux.HandleFunc("PUT /api/admin/mqtt", s.adminAuth(s.updateMQTTSettings))
	s.mux.Handle("GET /", http.FileServer(http.Dir("web")))
}

type contextKey string

const (
	deviceIDKey contextKey = "deviceID"
	deviceKey   contextKey = "device"
)

func (s *Server) deviceAuth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "device authentication required")
			return
		}
		device, err := s.repo.DeviceByToken(r.Context(), digest(token))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid device token")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not authenticate device")
			return
		}
		ctx := context.WithValue(r.Context(), deviceIDKey, device.ID)
		ctx = context.WithValue(ctx, deviceKey, device)
		r = r.WithContext(ctx)
		next(w, r)
	}
}

func deviceFromContext(r *http.Request) domain.Device {
	device, _ := r.Context().Value(deviceKey).(domain.Device)
	return device
}

func (s *Server) adminAuth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := bearerToken(r)
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.adminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "admin authentication required")
			return
		}
		next(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.repo.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if len(request.Name) == 0 || len(request.Name) > 120 {
		writeError(w, http.StatusBadRequest, "name must contain 1 to 120 characters")
		return
	}
	id, err := randomToken(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create device")
		return
	}
	code, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create enrollment code")
		return
	}
	device, err := s.repo.CreateDevice(r.Context(), id, request.Name, digest(code), domain.DefaultConfig())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create device")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"device": device, "enrollment_code": code})
}

func (s *Server) createEnrollmentQR(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !isHexToken(request.Text, 20, 256) {
		writeError(w, http.StatusBadRequest, "QR text must be a valid enrollment code")
		return
	}
	png, err := qrcode.Encode(request.Text, qrcode.Medium, 400)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate enrollment QR")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var request struct {
		EnrollmentCode string `json:"enrollment_code"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if len(request.EnrollmentCode) < 20 || len(request.EnrollmentCode) > 256 {
		writeError(w, http.StatusUnauthorized, "invalid enrollment code")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not enroll device")
		return
	}
	device, err := s.repo.Enroll(r.Context(), digest(request.EnrollmentCode), digest(token))
	if errors.Is(err, store.ErrInvalidEnrollment) {
		writeError(w, http.StatusUnauthorized, "invalid or already-used enrollment code")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not enroll device")
		return
	}
	if s.mqtt != nil {
		s.mqtt.PublishDevice(device)
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": device.ID, "access_token": token, "config": device.Config})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	device := deviceFromContext(r)
	deviceID := device.ID
	var batteryPercent *int
	if value := r.Header.Get("X-Battery-Percent"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid battery percentage")
			return
		}
		batteryPercent = &parsed
	}
	var charging *bool
	if value := r.Header.Get("X-Charging"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid charging state")
			return
		}
		charging = &parsed
	}
	if err := s.repo.RecordHeartbeat(r.Context(), deviceID, batteryPercent, charging); err != nil {
		writeError(w, http.StatusInternalServerError, "could not record device heartbeat")
		return
	}
	config, err := s.repo.GetConfig(r.Context(), deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load device configuration")
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (s *Server) acknowledgeConfig(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version   int64             `json:"version"`
		Telemetry *domain.Telemetry `json:"telemetry,omitempty"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Version < 1 {
		writeError(w, http.StatusBadRequest, "version must be positive")
		return
	}
	device := deviceFromContext(r)
	deviceID := device.ID
	if request.Telemetry != nil {
		telemetry := *request.Telemetry
		if telemetry.RecordedAt.IsZero() {
			telemetry.RecordedAt = time.Now().UTC()
		}
		if err := validateTelemetry(telemetry); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.repo.RecordTelemetry(r.Context(), deviceID, telemetry); err != nil {
			writeError(w, http.StatusInternalServerError, "could not store device telemetry")
			return
		}
		if s.mqtt != nil {
			s.mqtt.PublishTelemetry(device, telemetry)
		}
	}
	applied, err := s.repo.AcknowledgeConfig(r.Context(), deviceID, request.Version)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not acknowledge configuration")
		return
	}
	if current, err := s.repo.GetConfig(r.Context(), deviceID); err == nil {
		device.Config = current
		if s.mqtt != nil {
			s.mqtt.PublishConfig(device)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"applied": applied})
}

func validateTelemetry(telemetry domain.Telemetry) error {
	now := time.Now()
	if telemetry.RecordedAt.After(now.Add(5*time.Minute)) || telemetry.RecordedAt.Before(now.Add(-365*24*time.Hour)) {
		return errors.New("invalid telemetry timestamp")
	}
	if telemetry.BatteryPercent != nil && (*telemetry.BatteryPercent < 0 || *telemetry.BatteryPercent > 100) {
		return errors.New("invalid telemetry battery percentage")
	}
	if telemetry.BatteryTemperatureC != nil && (*telemetry.BatteryTemperatureC < -50 || *telemetry.BatteryTemperatureC > 150) {
		return errors.New("invalid telemetry battery temperature")
	}
	if telemetry.WakeDelaySeconds != nil && (*telemetry.WakeDelaySeconds < 0 || *telemetry.WakeDelaySeconds > 604800) {
		return errors.New("invalid telemetry wake delay")
	}
	if telemetry.SyncDurationMS != nil && (*telemetry.SyncDurationMS < 0 || *telemetry.SyncDurationMS > 600000) {
		return errors.New("invalid telemetry sync duration")
	}
	if telemetry.QueueSize != nil && (*telemetry.QueueSize < 0 || *telemetry.QueueSize > 500) {
		return errors.New("invalid telemetry queue size")
	}
	if telemetry.ConfigVersion != nil && *telemetry.ConfigVersion < 0 {
		return errors.New("invalid telemetry config version")
	}
	if telemetry.NetworkType != nil && len(*telemetry.NetworkType) > 64 {
		return errors.New("invalid telemetry network type")
	}
	if telemetry.StandbyBucket != nil && len(*telemetry.StandbyBucket) > 64 {
		return errors.New("invalid telemetry standby bucket")
	}
	return nil
}

func (s *Server) recordLocations(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Locations []domain.Location `json:"locations"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if len(request.Locations) == 0 || len(request.Locations) > 500 {
		writeError(w, http.StatusBadRequest, "locations must contain between 1 and 500 items")
		return
	}
	for index := range request.Locations {
		location := &request.Locations[index]
		if len(location.ID) == 0 || len(location.ID) > 128 || location.Latitude < -90 || location.Latitude > 90 ||
			location.Longitude < -180 || location.Longitude > 180 || location.AccuracyM < 0 || location.AccuracyM > 100000 ||
			location.RecordedAt.IsZero() || location.RecordedAt.After(time.Now().Add(5*time.Minute)) {
			writeError(w, http.StatusBadRequest, "invalid location record")
			return
		}
		if location.BatteryPercent != nil && (*location.BatteryPercent < 0 || *location.BatteryPercent > 100) {
			writeError(w, http.StatusBadRequest, "invalid battery percentage")
			return
		}
		if location.GPSFixDurationMS != nil && (*location.GPSFixDurationMS < 0 || *location.GPSFixDurationMS > 600000) {
			writeError(w, http.StatusBadRequest, "invalid GPS fix duration")
			return
		}
		if location.NetworkType != nil && len(*location.NetworkType) > 64 {
			writeError(w, http.StatusBadRequest, "invalid network type")
			return
		}
		if location.StandbyBucket != nil && len(*location.StandbyBucket) > 64 {
			writeError(w, http.StatusBadRequest, "invalid standby bucket")
			return
		}
		if location.LocationSource != nil {
			source := strings.ToLower(strings.TrimSpace(*location.LocationSource))
			if source == "" || len(source) > 32 || !validLocationSource(source) {
				writeError(w, http.StatusBadRequest, "invalid location source")
				return
			}
			*location.LocationSource = source
		}
	}
	device := deviceFromContext(r)
	if err := s.repo.RecordLocations(r.Context(), device.ID, request.Locations); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store locations")
		return
	}
	if s.mqtt != nil {
		s.mqtt.PublishLocation(device, request.Locations[len(request.Locations)-1])
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(request.Locations)})
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.repo.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load devices")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	var config domain.Config
	if !decodeJSON(w, r, &config) {
		return
	}
	if config.LocationIntervalSeconds < 5 || config.LocationIntervalSeconds > 86400 ||
		config.SyncIntervalSeconds < 5 || config.SyncIntervalSeconds > 86400 ||
		config.SleepDurationSeconds < 10 || config.SleepDurationSeconds > 604800 ||
		config.GPSTimeoutSeconds < 5 || config.GPSTimeoutSeconds > 180 ||
		config.MinDistanceMeters < 0 || config.MinDistanceMeters > 10000 {
		writeError(w, http.StatusBadRequest, "configuration values are outside supported ranges")
		return
	}
	device, err := s.repo.UpdateConfig(r.Context(), r.PathValue("id"), config)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update configuration")
		return
	}
	if s.mqtt != nil {
		s.mqtt.PublishConfig(device)
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": device})
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	err := s.repo.DeleteDevice(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete device")
		return
	}
	if s.mqtt != nil {
		s.mqtt.RemoveDevice(r.PathValue("id"))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.repo.RevokeDevice(r.Context(), r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found or already revoked")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not revoke device")
		return
	}
	if s.mqtt != nil {
		s.mqtt.RemoveDevice(r.PathValue("id"))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) locationHistory(w http.ResponseWriter, r *http.Request) {
	from := time.Now().Add(-7 * 24 * time.Hour)
	to := time.Now().Add(time.Minute)
	var err error
	if value := r.URL.Query().Get("from"); value != "" {
		from, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		to, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
	}
	if to.Before(from) {
		writeError(w, http.StatusBadRequest, "to must be after from")
		return
	}
	limit := 1000
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 5000 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 5000")
			return
		}
	}
	locations, err := s.repo.LocationHistory(r.Context(), r.PathValue("id"), from, to, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load location history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locations": locations})
}

func (s *Server) telemetryHistory(w http.ResponseWriter, r *http.Request) {
	from := time.Now().Add(-7 * 24 * time.Hour)
	to := time.Now().Add(time.Minute)
	var err error
	if value := r.URL.Query().Get("from"); value != "" {
		from, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		to, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
	}
	if to.Before(from) {
		writeError(w, http.StatusBadRequest, "to must be after from")
		return
	}
	limit := 1000
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 5000 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 5000")
			return
		}
	}
	telemetry, err := s.repo.TelemetryHistory(r.Context(), r.PathValue("id"), from, to, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load telemetry history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"telemetry": telemetry})
}

type mqttSettingsView struct {
	domain.MQTTSettings
	PasswordSet bool `json:"password_set"`
}

func (s *Server) getMQTTSettings(w http.ResponseWriter, r *http.Request) {
	record, err := s.repo.GetMQTTSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load MQTT settings")
		return
	}
	status := domain.MQTTStatus{}
	if s.mqtt != nil {
		status = s.mqtt.Status()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": mqttSettingsView{MQTTSettings: record.Settings, PasswordSet: len(record.PasswordCiphertext) > 0},
		"status":   status,
	})
}

func (s *Server) updateMQTTSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		domain.MQTTSettings
		Password *string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	settings := request.MQTTSettings
	settings.Host = strings.TrimSpace(settings.Host)
	settings.Username = strings.TrimSpace(settings.Username)
	settings.DiscoveryPrefix = strings.Trim(strings.TrimSpace(settings.DiscoveryPrefix), "/")
	settings.BaseTopic = strings.Trim(strings.TrimSpace(settings.BaseTopic), "/")
	settings.ClientID = strings.TrimSpace(settings.ClientID)
	if settings.DiscoveryPrefix == "" {
		settings.DiscoveryPrefix = "homeassistant"
	}
	if settings.BaseTopic == "" {
		settings.BaseTopic = "kidstracker"
	}
	if err := validateMQTTSettings(settings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	record, err := s.repo.GetMQTTSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load MQTT settings")
		return
	}
	passwordCiphertext := record.PasswordCiphertext
	if request.Password != nil {
		if len(*request.Password) > 512 {
			writeError(w, http.StatusBadRequest, "MQTT password is too long")
			return
		}
		if *request.Password == "" {
			passwordCiphertext = nil
		} else {
			if s.cipher == nil {
				writeError(w, http.StatusInternalServerError, "settings encryption is unavailable")
				return
			}
			passwordCiphertext, err = s.cipher.Encrypt([]byte(*request.Password))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not encrypt MQTT password")
				return
			}
		}
	}
	var password string
	if settings.Enabled && len(passwordCiphertext) > 0 {
		if s.cipher == nil {
			writeError(w, http.StatusInternalServerError, "settings encryption is unavailable")
			return
		}
		plaintext, err := s.cipher.Decrypt(passwordCiphertext)
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not decrypt the stored MQTT password; re-enter it")
			return
		}
		password = string(plaintext)
	}
	if err := s.repo.SaveMQTTSettings(r.Context(), settings, passwordCiphertext); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save MQTT settings")
		return
	}
	status := domain.MQTTStatus{}
	if s.mqtt != nil {
		configureErr := s.mqtt.Configure(settings, password)
		if configureErr != nil {
			status.LastError = configureErr.Error()
		}
		status = s.mqtt.Status()
		if status.LastError == "" && configureErr != nil {
			status.LastError = configureErr.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": mqttSettingsView{MQTTSettings: settings, PasswordSet: len(passwordCiphertext) > 0},
		"status":   status,
	})
}

func validateMQTTSettings(settings domain.MQTTSettings) error {
	if settings.Host == "" || strings.ContainsAny(settings.Host, "/ \t\r\n") || strings.Contains(settings.Host, "://") {
		return errors.New("MQTT host must be a hostname or IP without a scheme")
	}
	if settings.Port < 1 || settings.Port > 65535 {
		return errors.New("MQTT port must be between 1 and 65535")
	}
	if len(settings.Username) > 256 {
		return errors.New("MQTT username is too long")
	}
	if len(settings.ClientID) > 64 || !validTopicPart(settings.ClientID) {
		return errors.New("MQTT client ID may contain only letters, numbers, underscore and hyphen")
	}
	if !validTopicPath(settings.DiscoveryPrefix) || !validTopicPath(settings.BaseTopic) {
		return errors.New("MQTT topics may contain only letters, numbers, underscore, hyphen and slashes")
	}
	return nil
}

func validLocationSource(value string) bool {
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func validTopicPart(value string) bool {
	if value == "" {
		return true
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func validTopicPath(value string) bool {
	if value == "" {
		return true
	}
	for _, part := range strings.Split(value, "/") {
		if !validTopicPart(part) {
			return false
		}
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	const maxJSONBytes = 1 << 20
	compressedBody := http.MaxBytesReader(w, r.Body, maxJSONBytes)
	var reader io.Reader = compressedBody
	var gzipReader *gzip.Reader
	encoding := strings.TrimSpace(r.Header.Get("Content-Encoding"))
	if strings.EqualFold(encoding, "gzip") {
		var err error
		gzipReader, err = gzip.NewReader(compressedBody)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid gzip request body")
			return false
		}
		defer gzipReader.Close()
		reader = gzipReader
	} else if encoding != "" && !strings.EqualFold(encoding, "identity") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported content encoding")
		return false
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxJSONBytes+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "could not read request body")
		}
		return false
	}
	if len(body) > maxJSONBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request must contain a single JSON object")
		return false
	}
	return true
}

func bearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func isHexToken(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
