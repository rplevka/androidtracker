package domain

import "time"

const (
	RoleAdmin = "admin"
	RoleUser  = "user"

	AuthTypeAdminKey = "admin_key"
	AuthTypePassword = "password"
)

type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	DisplayName  string     `json:"display_name"`
	PasswordHash string     `json:"-"`
	Role         string     `json:"role"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

type Session struct {
	UserID      *string
	Role        string
	Label       string
	Username    string
	DisplayName string
	ExpiresAt   time.Time
}

type Principal struct {
	Username    string
	DisplayName string
	Role        string
	AuthType    string
	ExpiresAt   *time.Time
}

type Config struct {
	Enabled                 bool    `json:"enabled"`
	LocationIntervalSeconds int     `json:"location_interval_seconds"`
	SyncIntervalSeconds     int     `json:"sync_interval_seconds"`
	SleepDurationSeconds    int     `json:"sleep_duration_seconds"`
	GPSTimeoutSeconds       int     `json:"gps_timeout_seconds"`
	MinDistanceMeters       float64 `json:"min_distance_meters"`
	Version                 int64   `json:"version"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:                 true,
		LocationIntervalSeconds: 900,
		SyncIntervalSeconds:     900,
		SleepDurationSeconds:    3600,
		GPSTimeoutSeconds:       60,
		MinDistanceMeters:       0,
		Version:                 1,
	}
}

// ConfigPatch contains optional config fields. Nil fields keep their current value.
type ConfigPatch struct {
	Enabled                 *bool    `json:"enabled,omitempty"`
	LocationIntervalSeconds *int     `json:"location_interval_seconds,omitempty"`
	SyncIntervalSeconds     *int     `json:"sync_interval_seconds,omitempty"`
	SleepDurationSeconds    *int     `json:"sleep_duration_seconds,omitempty"`
	GPSTimeoutSeconds       *int     `json:"gps_timeout_seconds,omitempty"`
	MinDistanceMeters       *float64 `json:"min_distance_meters,omitempty"`
	Version                 *int64   `json:"version,omitempty"`
}

func (p ConfigPatch) Apply(current Config) Config {
	if p.Enabled != nil {
		current.Enabled = *p.Enabled
	}
	if p.LocationIntervalSeconds != nil {
		current.LocationIntervalSeconds = *p.LocationIntervalSeconds
	}
	if p.SyncIntervalSeconds != nil {
		current.SyncIntervalSeconds = *p.SyncIntervalSeconds
	}
	if p.SleepDurationSeconds != nil {
		current.SleepDurationSeconds = *p.SleepDurationSeconds
	}
	if p.GPSTimeoutSeconds != nil {
		current.GPSTimeoutSeconds = *p.GPSTimeoutSeconds
	}
	if p.MinDistanceMeters != nil {
		current.MinDistanceMeters = *p.MinDistanceMeters
	}
	return current
}

type Location struct {
	ID               string    `json:"id"`
	Latitude         float64   `json:"latitude"`
	Longitude        float64   `json:"longitude"`
	AccuracyM        float64   `json:"accuracy_meters"`
	AltitudeM        *float64  `json:"altitude_meters,omitempty"`
	SpeedMPS         *float64  `json:"speed_meters_per_second,omitempty"`
	Bearing          *float64  `json:"bearing_degrees,omitempty"`
	BatteryPercent   *int      `json:"battery_percent,omitempty"`
	Charging         *bool     `json:"charging,omitempty"`
	ScreenOn         *bool     `json:"screen_on,omitempty"`
	DeviceLocked     *bool     `json:"device_locked,omitempty"`
	DeviceIdle       *bool     `json:"device_idle,omitempty"`
	PowerSaveMode    *bool     `json:"power_save_mode,omitempty"`
	StandbyBucket    *string   `json:"standby_bucket,omitempty"`
	NetworkType      *string   `json:"network_type,omitempty"`
	GPSFixDurationMS *int64    `json:"gps_fix_duration_ms,omitempty"`
	LocationSource   *string   `json:"location_source,omitempty"`
	RecordedAt       time.Time `json:"recorded_at"`
	ReceivedAt       time.Time `json:"received_at,omitempty"`
}

type Telemetry struct {
	RecordedAt          time.Time `json:"recorded_at"`
	BatteryPercent      *int      `json:"battery_percent,omitempty"`
	Charging            *bool     `json:"charging,omitempty"`
	ScreenOn            *bool     `json:"screen_on,omitempty"`
	DeviceLocked        *bool     `json:"device_locked,omitempty"`
	DeviceIdle          *bool     `json:"device_idle,omitempty"`
	PowerSaveMode       *bool     `json:"power_save_mode,omitempty"`
	StandbyBucket       *string   `json:"standby_bucket,omitempty"`
	NetworkType         *string   `json:"network_type,omitempty"`
	GPSEnabled          *bool     `json:"gps_enabled,omitempty"`
	BatteryTemperatureC *float64  `json:"battery_temperature_c,omitempty"`
	WakeDelaySeconds    *int      `json:"wake_delay_seconds,omitempty"`
	SyncDurationMS      *int64    `json:"sync_duration_ms,omitempty"`
	QueueSize           *int      `json:"queue_size,omitempty"`
	ConfigVersion       *int64    `json:"config_version,omitempty"`
	ReceivedAt          time.Time `json:"received_at,omitempty"`
}

type Device struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Activated       bool       `json:"activated"`
	Revoked         bool       `json:"revoked"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeen        *time.Time `json:"last_seen,omitempty"`
	BatteryPercent  *int       `json:"battery_percent,omitempty"`
	Charging        *bool      `json:"charging,omitempty"`
	Config          Config     `json:"config"`
	AppliedConfig   *Config    `json:"applied_config,omitempty"`
	AppliedAt       *time.Time `json:"applied_at,omitempty"`
	CurrentLocation *Location  `json:"current_location,omitempty"`
}

type MQTTSettings struct {
	Enabled          bool   `json:"enabled"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username"`
	Password         string `json:"-"`
	TLS              bool   `json:"tls"`
	DiscoveryEnabled bool   `json:"discovery_enabled"`
	DiscoveryPrefix  string `json:"discovery_prefix"`
	BaseTopic        string `json:"base_topic"`
	ClientID         string `json:"client_id"`
}

func DefaultMQTTSettings() MQTTSettings {
	return MQTTSettings{
		Host:             "localhost",
		Port:             1883,
		DiscoveryEnabled: true,
		DiscoveryPrefix:  "homeassistant",
		BaseTopic:        "kidstracker",
	}
}

type MQTTStatus struct {
	Connected       bool       `json:"connected"`
	LastError       string     `json:"last_error,omitempty"`
	LastConnectedAt *time.Time `json:"last_connected_at,omitempty"`
}

type MQTTRecord struct {
	Settings           MQTTSettings
	PasswordCiphertext []byte
	UpdatedAt          time.Time
}
