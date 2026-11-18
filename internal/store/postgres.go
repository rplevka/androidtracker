package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/roman/kidstracker/internal/domain"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Postgres struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, connectionString string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, connectionString)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		contents, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := p.pool.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

func (p *Postgres) CreateDevice(ctx context.Context, id, name, enrollmentHash string, config domain.Config) (domain.Device, error) {
	var d domain.Device
	err := p.pool.QueryRow(ctx, `
		INSERT INTO devices (id, name, enrollment_code_hash, enabled, location_interval_seconds,
			sync_interval_seconds, sleep_duration_seconds, gps_timeout_seconds, min_distance_meters, config_version)
		VALUES ($1, $2, decode($3, 'hex'), $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, name, false, created_at, enabled, location_interval_seconds,
			sync_interval_seconds, sleep_duration_seconds, gps_timeout_seconds, min_distance_meters, config_version`,
		id, name, enrollmentHash, config.Enabled, config.LocationIntervalSeconds,
		config.SyncIntervalSeconds, config.SleepDurationSeconds, config.GPSTimeoutSeconds,
		config.MinDistanceMeters, config.Version,
	).Scan(&d.ID, &d.Name, &d.Activated, &d.CreatedAt, &d.Config.Enabled,
		&d.Config.LocationIntervalSeconds, &d.Config.SyncIntervalSeconds, &d.Config.SleepDurationSeconds,
		&d.Config.GPSTimeoutSeconds, &d.Config.MinDistanceMeters, &d.Config.Version)
	return d, err
}

func (p *Postgres) Enroll(ctx context.Context, enrollmentHash, tokenHash string) (domain.Device, error) {
	var d domain.Device
	err := p.pool.QueryRow(ctx, `
		UPDATE devices SET device_token_hash = decode($2, 'hex'), enrollment_code_hash = NULL,
			activated_at = now(), last_seen = now(), updated_at = now()
		WHERE enrollment_code_hash = decode($1, 'hex') AND device_token_hash IS NULL
			AND enrollment_expires_at > now() AND revoked_at IS NULL
		RETURNING id, name, true, false, created_at, enabled, location_interval_seconds,
			sync_interval_seconds, sleep_duration_seconds, gps_timeout_seconds, min_distance_meters, config_version`,
		enrollmentHash, tokenHash,
	).Scan(&d.ID, &d.Name, &d.Activated, &d.Revoked, &d.CreatedAt, &d.Config.Enabled,
		&d.Config.LocationIntervalSeconds, &d.Config.SyncIntervalSeconds, &d.Config.SleepDurationSeconds,
		&d.Config.GPSTimeoutSeconds, &d.Config.MinDistanceMeters, &d.Config.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Device{}, ErrInvalidEnrollment
	}
	return d, err
}

func (p *Postgres) DeviceByToken(ctx context.Context, tokenHash string) (domain.Device, error) {
	var d domain.Device
	err := p.pool.QueryRow(ctx, `
		SELECT id, name, activated_at IS NOT NULL AND revoked_at IS NULL, revoked_at IS NOT NULL, created_at, last_seen, enabled,
			location_interval_seconds, sync_interval_seconds, sleep_duration_seconds, gps_timeout_seconds,
			min_distance_meters, config_version
		FROM devices WHERE device_token_hash = decode($1, 'hex')`, tokenHash,
	).Scan(&d.ID, &d.Name, &d.Activated, &d.Revoked, &d.CreatedAt, &d.LastSeen, &d.Config.Enabled,
		&d.Config.LocationIntervalSeconds, &d.Config.SyncIntervalSeconds, &d.Config.SleepDurationSeconds,
		&d.Config.GPSTimeoutSeconds, &d.Config.MinDistanceMeters, &d.Config.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Device{}, ErrNotFound
	}
	return d, err
}

func (p *Postgres) RecordHeartbeat(ctx context.Context, deviceID string, batteryPercent *int, charging *bool) error {
	result, err := p.pool.Exec(ctx, `UPDATE devices SET
		battery_percent = COALESCE($2, battery_percent), charging = COALESCE($3, charging),
		last_seen = now(), updated_at = now() WHERE id = $1`, deviceID, batteryPercent, charging)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) AcknowledgeConfig(ctx context.Context, deviceID string, version int64) (bool, error) {
	result, err := p.pool.Exec(ctx, `UPDATE devices SET applied_config_version = $2, applied_config_at = now(),
		applied_enabled = enabled, applied_location_interval_seconds = location_interval_seconds,
		applied_sync_interval_seconds = sync_interval_seconds, applied_sleep_duration_seconds = sleep_duration_seconds,
		applied_gps_timeout_seconds = gps_timeout_seconds, applied_min_distance_meters = min_distance_meters
		WHERE id = $1 AND config_version = $2`, deviceID, version)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() != 0 {
		return true, nil
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM devices WHERE id = $1)`, deviceID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, ErrNotFound
	}
	return false, nil
}

func (p *Postgres) RecordTelemetry(ctx context.Context, deviceID string, telemetry domain.Telemetry) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO device_telemetry (device_id, recorded_at, battery_percent, charging, screen_on,
			device_locked, device_idle, power_save_mode, standby_bucket, network_type, gps_enabled,
			battery_temperature_c, wake_delay_seconds, sync_duration_ms, queue_size, config_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (device_id, recorded_at) DO NOTHING`,
		deviceID, telemetry.RecordedAt, telemetry.BatteryPercent, telemetry.Charging, telemetry.ScreenOn,
		telemetry.DeviceLocked, telemetry.DeviceIdle, telemetry.PowerSaveMode, telemetry.StandbyBucket,
		telemetry.NetworkType, telemetry.GPSEnabled, telemetry.BatteryTemperatureC,
		telemetry.WakeDelaySeconds, telemetry.SyncDurationMS, telemetry.QueueSize, telemetry.ConfigVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE devices SET
		battery_percent = COALESCE($2, battery_percent), charging = COALESCE($3, charging),
		last_seen = now(), updated_at = now() WHERE id = $1`,
		deviceID, telemetry.BatteryPercent, telemetry.Charging); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) RecordLocations(ctx context.Context, deviceID string, locations []domain.Location) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, location := range locations {
		_, err := tx.Exec(ctx, `
			INSERT INTO locations (device_id, client_location_id, recorded_at, latitude, longitude,
				accuracy_meters, altitude_meters, speed_meters_per_second, bearing_degrees, battery_percent, charging,
				screen_on, device_locked, device_idle, power_save_mode, standby_bucket, network_type,
				gps_fix_duration_ms, location_source, point)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
				ST_SetSRID(ST_MakePoint($5, $4), 4326)::geography)
			ON CONFLICT (device_id, client_location_id) DO NOTHING`,
			deviceID, location.ID, location.RecordedAt, location.Latitude, location.Longitude,
			location.AccuracyM, location.AltitudeM, location.SpeedMPS, location.Bearing,
			location.BatteryPercent, location.Charging, location.ScreenOn, location.DeviceLocked,
			location.DeviceIdle, location.PowerSaveMode, location.StandbyBucket, location.NetworkType,
			location.GPSFixDurationMS, location.LocationSource)
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE devices SET last_seen = now(), updated_at = now() WHERE id = $1`, deviceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) GetConfig(ctx context.Context, deviceID string) (domain.Config, error) {
	var config domain.Config
	err := p.pool.QueryRow(ctx, `SELECT enabled, location_interval_seconds, sync_interval_seconds, sleep_duration_seconds,
		gps_timeout_seconds, min_distance_meters, config_version FROM devices WHERE id = $1`, deviceID).
		Scan(&config.Enabled, &config.LocationIntervalSeconds, &config.SyncIntervalSeconds, &config.SleepDurationSeconds,
			&config.GPSTimeoutSeconds, &config.MinDistanceMeters, &config.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Config{}, ErrNotFound
	}
	return config, err
}

func (p *Postgres) ListDevices(ctx context.Context) ([]domain.Device, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT d.id, d.name, d.activated_at IS NOT NULL AND d.revoked_at IS NULL, d.revoked_at IS NOT NULL, d.created_at, d.last_seen,
			d.battery_percent, d.charging, d.applied_config_version, d.applied_config_at,
			d.applied_enabled, d.applied_location_interval_seconds, d.applied_sync_interval_seconds,
			d.applied_sleep_duration_seconds, d.applied_gps_timeout_seconds, d.applied_min_distance_meters,
			d.enabled, d.location_interval_seconds, d.sync_interval_seconds, d.sleep_duration_seconds,
			d.gps_timeout_seconds, d.min_distance_meters, d.config_version,
			l.client_location_id, l.latitude, l.longitude, l.accuracy_meters,
			l.battery_percent, l.charging, l.location_source, l.recorded_at
		FROM devices d LEFT JOIN LATERAL (
			SELECT client_location_id, latitude, longitude, accuracy_meters, battery_percent, charging,
				location_source, recorded_at
			FROM locations WHERE device_id = d.id ORDER BY recorded_at DESC LIMIT 1
		) l ON true ORDER BY d.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := make([]domain.Device, 0)
	for rows.Next() {
		var d domain.Device
		var locationID *string
		var latitude, longitude, accuracy *float64
		var batteryPercent *int16
		var charging *bool
		var locationSource *string
		var deviceBattery *int16
		var deviceCharging *bool
		var appliedConfigVersion int64
		var appliedConfigAt *time.Time
		var appliedEnabled *bool
		var appliedLocationInterval, appliedSyncInterval, appliedSleepDuration, appliedGPSTimeout *int32
		var appliedMinDistance *float64
		var recordedAt *time.Time
		if err := rows.Scan(&d.ID, &d.Name, &d.Activated, &d.Revoked, &d.CreatedAt, &d.LastSeen,
			&deviceBattery, &deviceCharging,
			&appliedConfigVersion, &appliedConfigAt, &appliedEnabled, &appliedLocationInterval,
			&appliedSyncInterval, &appliedSleepDuration, &appliedGPSTimeout, &appliedMinDistance,
			&d.Config.Enabled, &d.Config.LocationIntervalSeconds, &d.Config.SyncIntervalSeconds, &d.Config.SleepDurationSeconds,
			&d.Config.GPSTimeoutSeconds, &d.Config.MinDistanceMeters, &d.Config.Version,
			&locationID, &latitude, &longitude, &accuracy, &batteryPercent, &charging, &locationSource, &recordedAt); err != nil {
			return nil, err
		}
		if deviceBattery != nil {
			value := int(*deviceBattery)
			d.BatteryPercent = &value
		}
		d.Charging = deviceCharging
		if appliedConfigVersion > 0 && appliedEnabled != nil && appliedLocationInterval != nil &&
			appliedSyncInterval != nil && appliedSleepDuration != nil && appliedGPSTimeout != nil && appliedMinDistance != nil {
			d.AppliedConfig = &domain.Config{
				Enabled:                 *appliedEnabled,
				LocationIntervalSeconds: int(*appliedLocationInterval),
				SyncIntervalSeconds:     int(*appliedSyncInterval),
				SleepDurationSeconds:    int(*appliedSleepDuration),
				GPSTimeoutSeconds:       int(*appliedGPSTimeout),
				MinDistanceMeters:       *appliedMinDistance,
				Version:                 appliedConfigVersion,
			}
			d.AppliedAt = appliedConfigAt
		}
		if locationID != nil {
			d.CurrentLocation = &domain.Location{ID: *locationID, Latitude: *latitude,
				Longitude: *longitude, AccuracyM: *accuracy, LocationSource: locationSource,
				RecordedAt: *recordedAt}
			if batteryPercent != nil {
				value := int(*batteryPercent)
				d.CurrentLocation.BatteryPercent = &value
				if d.BatteryPercent == nil {
					d.BatteryPercent = &value
				}
			}
			d.CurrentLocation.Charging = charging
			if d.Charging == nil {
				d.Charging = charging
			}
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (p *Postgres) UpdateConfig(ctx context.Context, deviceID string, config domain.Config) (domain.Device, error) {
	var d domain.Device
	err := p.pool.QueryRow(ctx, `UPDATE devices SET enabled = $2, location_interval_seconds = $3,
		sync_interval_seconds = $4, sleep_duration_seconds = $5, gps_timeout_seconds = $6, min_distance_meters = $7,
		config_version = config_version + 1, updated_at = now() WHERE id = $1
		RETURNING id, name, activated_at IS NOT NULL AND revoked_at IS NULL, revoked_at IS NOT NULL, created_at, last_seen, enabled,
		location_interval_seconds, sync_interval_seconds, sleep_duration_seconds, gps_timeout_seconds,
		min_distance_meters, config_version`, deviceID, config.Enabled,
		config.LocationIntervalSeconds, config.SyncIntervalSeconds, config.SleepDurationSeconds, config.GPSTimeoutSeconds,
		config.MinDistanceMeters).
		Scan(&d.ID, &d.Name, &d.Activated, &d.Revoked, &d.CreatedAt, &d.LastSeen, &d.Config.Enabled,
			&d.Config.LocationIntervalSeconds, &d.Config.SyncIntervalSeconds, &d.Config.SleepDurationSeconds,
			&d.Config.GPSTimeoutSeconds, &d.Config.MinDistanceMeters, &d.Config.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Device{}, ErrNotFound
	}
	return d, err
}

func (p *Postgres) RevokeDevice(ctx context.Context, deviceID string) error {
	result, err := p.pool.Exec(ctx, `UPDATE devices SET device_token_hash = NULL,
		enrollment_code_hash = NULL, revoked_at = now(), updated_at = now()
		WHERE id = $1 AND revoked_at IS NULL`, deviceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) DeleteDevice(ctx context.Context, deviceID string) error {
	result, err := p.pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, deviceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) LocationHistory(ctx context.Context, deviceID string, from, to time.Time, limit int) ([]domain.Location, error) {
	rows, err := p.pool.Query(ctx, `SELECT client_location_id, latitude, longitude, accuracy_meters,
		altitude_meters, speed_meters_per_second, bearing_degrees, battery_percent, charging,
		screen_on, device_locked, device_idle, power_save_mode, standby_bucket, network_type,
		gps_fix_duration_ms, location_source, recorded_at, received_at
		FROM locations WHERE device_id = $1 AND recorded_at >= $2 AND recorded_at <= $3
		ORDER BY recorded_at DESC LIMIT $4`, deviceID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locations := make([]domain.Location, 0)
	for rows.Next() {
		var location domain.Location
		if err := rows.Scan(&location.ID, &location.Latitude, &location.Longitude,
			&location.AccuracyM, &location.AltitudeM, &location.SpeedMPS,
			&location.Bearing, &location.BatteryPercent, &location.Charging,
			&location.ScreenOn, &location.DeviceLocked, &location.DeviceIdle, &location.PowerSaveMode,
			&location.StandbyBucket, &location.NetworkType, &location.GPSFixDurationMS,
			&location.LocationSource, &location.RecordedAt, &location.ReceivedAt); err != nil {
			return nil, err
		}
		locations = append(locations, location)
	}
	return locations, rows.Err()
}

func (p *Postgres) TelemetryHistory(ctx context.Context, deviceID string, from, to time.Time, limit int) ([]domain.Telemetry, error) {
	rows, err := p.pool.Query(ctx, `SELECT recorded_at, received_at, battery_percent, charging,
		screen_on, device_locked, device_idle, power_save_mode, standby_bucket, network_type,
		gps_enabled, battery_temperature_c, wake_delay_seconds, sync_duration_ms, queue_size, config_version
		FROM device_telemetry WHERE device_id = $1 AND recorded_at >= $2 AND recorded_at <= $3
		ORDER BY recorded_at DESC LIMIT $4`, deviceID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	telemetry := make([]domain.Telemetry, 0)
	for rows.Next() {
		var item domain.Telemetry
		if err := rows.Scan(&item.RecordedAt, &item.ReceivedAt, &item.BatteryPercent, &item.Charging,
			&item.ScreenOn, &item.DeviceLocked, &item.DeviceIdle, &item.PowerSaveMode,
			&item.StandbyBucket, &item.NetworkType, &item.GPSEnabled, &item.BatteryTemperatureC,
			&item.WakeDelaySeconds, &item.SyncDurationMS, &item.QueueSize, &item.ConfigVersion); err != nil {
			return nil, err
		}
		telemetry = append(telemetry, item)
	}
	return telemetry, rows.Err()
}

func (p *Postgres) GetMQTTSettings(ctx context.Context) (domain.MQTTRecord, error) {
	var record domain.MQTTRecord
	err := p.pool.QueryRow(ctx, `SELECT enabled, host, port, username, password_ciphertext, tls,
		discovery_enabled, discovery_prefix, base_topic, client_id, updated_at
		FROM mqtt_settings WHERE id = TRUE`).
		Scan(&record.Settings.Enabled, &record.Settings.Host, &record.Settings.Port,
			&record.Settings.Username, &record.PasswordCiphertext, &record.Settings.TLS,
			&record.Settings.DiscoveryEnabled, &record.Settings.DiscoveryPrefix,
			&record.Settings.BaseTopic, &record.Settings.ClientID, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MQTTRecord{Settings: domain.DefaultMQTTSettings()}, nil
	}
	return record, err
}

func (p *Postgres) SaveMQTTSettings(ctx context.Context, settings domain.MQTTSettings, passwordCiphertext []byte) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO mqtt_settings
		(id, enabled, host, port, username, password_ciphertext, tls, discovery_enabled,
			discovery_prefix, base_topic, client_id, updated_at)
		VALUES (TRUE, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled, host = EXCLUDED.host,
			port = EXCLUDED.port, username = EXCLUDED.username,
			password_ciphertext = EXCLUDED.password_ciphertext, tls = EXCLUDED.tls,
			discovery_enabled = EXCLUDED.discovery_enabled,
			discovery_prefix = EXCLUDED.discovery_prefix, base_topic = EXCLUDED.base_topic,
			client_id = EXCLUDED.client_id, updated_at = now()`,
		settings.Enabled, settings.Host, settings.Port, settings.Username, passwordCiphertext,
		settings.TLS, settings.DiscoveryEnabled, settings.DiscoveryPrefix, settings.BaseTopic, settings.ClientID)
	return err
}
