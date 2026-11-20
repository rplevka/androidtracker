package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	if err != nil {
		return domain.Device{}, err
	}
	zones, err := p.ListDeviceZones(ctx, d.ID, false)
	if err != nil {
		return domain.Device{}, err
	}
	d.Zones = zones
	d.ZonesETag = domain.ZonesETag(zones)
	return d, nil
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

func (p *Postgres) RecordLocations(ctx context.Context, deviceID string, locations []domain.Location) (*domain.ZoneState, error) {
	if len(locations) == 0 {
		return nil, nil
	}
	ordered := append([]domain.Location(nil), locations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].RecordedAt.Before(ordered[j].RecordedAt)
	})

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	type insertedLocation struct {
		id       int64
		location domain.Location
	}
	inserted := make([]insertedLocation, 0, len(ordered))
	for _, location := range ordered {
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO locations (device_id, client_location_id, recorded_at, latitude, longitude,
				accuracy_meters, altitude_meters, speed_meters_per_second, bearing_degrees, battery_percent, charging,
				screen_on, device_locked, device_idle, power_save_mode, standby_bucket, network_type,
				gps_fix_duration_ms, location_source, point)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
				ST_SetSRID(ST_MakePoint($5, $4), 4326)::geography)
			ON CONFLICT (device_id, client_location_id) DO NOTHING
			RETURNING id`,
			deviceID, location.ID, location.RecordedAt, location.Latitude, location.Longitude,
			location.AccuracyM, location.AltitudeM, location.SpeedMPS, location.Bearing,
			location.BatteryPercent, location.Charging, location.ScreenOn, location.DeviceLocked,
			location.DeviceIdle, location.PowerSaveMode, location.StandbyBucket, location.NetworkType,
			location.GPSFixDurationMS, location.LocationSource).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		inserted = append(inserted, insertedLocation{id: id, location: location})
	}
	if _, err := tx.Exec(ctx, `UPDATE devices SET last_seen = now(), updated_at = now() WHERE id = $1`, deviceID); err != nil {
		return nil, err
	}
	if len(inserted) == 0 {
		return nil, tx.Commit(ctx)
	}

	type zoneRow struct {
		id           string
		name         string
		kind         string
		color        string
		hysteresisM  float64
		dwellSeconds int
		priority     int
		version      int64
	}
	zoneRows := make([]zoneRow, 0)
	rows, err := tx.Query(ctx, `
		SELECT z.id, z.name, z.kind, z.color, z.hysteresis_m, z.dwell_seconds, dz.priority, z.version
		FROM zones z
		JOIN device_zones dz ON dz.zone_id = z.id
		WHERE dz.device_id = $1
		ORDER BY dz.priority DESC, lower(z.name)`, deviceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var zone zoneRow
		if err := rows.Scan(&zone.id, &zone.name, &zone.kind, &zone.color, &zone.hysteresisM,
			&zone.dwellSeconds, &zone.priority, &zone.version); err != nil {
			rows.Close()
			return nil, err
		}
		zoneRows = append(zoneRows, zone)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(zoneRows) == 0 {
		return nil, tx.Commit(ctx)
	}

	ids := make([]int64, len(inserted))
	latitudes := make([]float64, len(inserted))
	longitudes := make([]float64, len(inserted))
	for index, point := range inserted {
		ids[index] = point.id
		latitudes[index] = point.location.Latitude
		longitudes[index] = point.location.Longitude
	}
	type coverage struct {
		inside    bool
		outsideM  float64
		boundaryM float64
	}
	coverages := make(map[int64]map[string]coverage, len(inserted))
	rows, err = tx.Query(ctx, `
		WITH pts AS (
			SELECT * FROM unnest($2::bigint[], $3::float8[], $4::float8[]) AS t(id, lat, lon)
		)
		SELECT p.id, z.id,
			ST_Covers(z.geom, pt.geom) AS inside,
			ST_Distance(z.geom, pt.geom) AS outside_m,
			ST_Distance(ST_Boundary(z.geom::geometry)::geography, pt.geom) AS boundary_m
		FROM pts p
		CROSS JOIN LATERAL (
			SELECT ST_SetSRID(ST_MakePoint(p.lon, p.lat), 4326)::geography AS geom
		) pt
		JOIN device_zones dz ON dz.device_id = $1
		JOIN zones z ON z.id = dz.zone_id
		ORDER BY p.id, dz.priority DESC, lower(z.name)`, deviceID, ids, latitudes, longitudes)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pointID int64
		var zoneID string
		var item coverage
		if err := rows.Scan(&pointID, &zoneID, &item.inside, &item.outsideM, &item.boundaryM); err != nil {
			rows.Close()
			return nil, err
		}
		if coverages[pointID] == nil {
			coverages[pointID] = make(map[string]coverage)
		}
		coverages[pointID][zoneID] = item
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type stateRow struct {
		state          string
		since          time.Time
		candidate      string
		candidateSince *time.Time
		lastEvent      string
		lastEventAt    *time.Time
		lastLocationID *int64
		lastAccuracyM  float64
	}
	states := make(map[string]*stateRow, len(zoneRows))
	rows, err = tx.Query(ctx, `
		SELECT zone_id, state, since, COALESCE(candidate_state, ''), candidate_since,
			COALESCE(last_event, ''), last_event_at, last_location_id, COALESCE(last_accuracy_m, 0)
		FROM device_zone_state WHERE device_id = $1`, deviceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var zoneID string
		var state stateRow
		if err := rows.Scan(&zoneID, &state.state, &state.since, &state.candidate, &state.candidateSince,
			&state.lastEvent, &state.lastEventAt, &state.lastLocationID, &state.lastAccuracyM); err != nil {
			rows.Close()
			return nil, err
		}
		states[zoneID] = &state
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	events := make([]domain.ZoneEvent, 0)
	for _, point := range inserted {
		pointCoverages := coverages[point.id]
		for _, zone := range zoneRows {
			item, ok := pointCoverages[zone.id]
			if !ok {
				continue
			}
			state := states[zone.id]
			if state == nil {
				state = &stateRow{state: "unknown", since: point.location.RecordedAt}
				states[zone.id] = state
			}
			buffer := math.Max(zone.hysteresisM, math.Min(point.location.AccuracyM, 500))
			candidate := "uncertain"
			if item.inside {
				if item.boundaryM >= buffer {
					candidate = "inside"
				}
			} else if item.outsideM >= buffer {
				candidate = "outside"
			}
			switch {
			case candidate == "uncertain":
				// Keep the previous state and any pending candidate.
			case candidate == state.state:
				state.candidate = ""
				state.candidateSince = nil
			default:
				if state.candidate != candidate || state.candidateSince == nil {
					state.candidate = candidate
					since := point.location.RecordedAt
					state.candidateSince = &since
				}
				if state.candidateSince != nil &&
					point.location.RecordedAt.Sub(*state.candidateSince) >= time.Duration(zone.dwellSeconds)*time.Second {
					eventType := ""
					if state.state != "unknown" {
						if candidate == "inside" {
							eventType = "enter"
						} else {
							eventType = "exit"
						}
					}
					state.state = candidate
					state.since = point.location.RecordedAt
					state.candidate = ""
					state.candidateSince = nil
					if eventType != "" {
						occurredAt := point.location.RecordedAt
						locationID := point.id
						events = append(events, domain.ZoneEvent{
							ZoneID: zone.id, ZoneName: zone.name, Event: eventType,
							OccurredAt: occurredAt, LocationID: &locationID,
							AccuracyM: point.location.AccuracyM,
						})
						state.lastEvent = eventType
						state.lastEventAt = &occurredAt
					}
				}
			}
			locationID := point.id
			state.lastLocationID = &locationID
			state.lastAccuracyM = point.location.AccuracyM
		}
	}

	for _, zone := range zoneRows {
		state := states[zone.id]
		if state == nil {
			continue
		}
		var candidate any
		if state.candidate != "" {
			candidate = state.candidate
		}
		var lastEvent any
		if state.lastEvent != "" {
			lastEvent = state.lastEvent
		}
		if state.since.IsZero() {
			state.since = inserted[len(inserted)-1].location.RecordedAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO device_zone_state (device_id, zone_id, state, since, candidate_state, candidate_since,
				last_location_id, last_accuracy_m, last_event, last_event_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
			ON CONFLICT (device_id, zone_id) DO UPDATE SET
				state = EXCLUDED.state, since = EXCLUDED.since,
				candidate_state = EXCLUDED.candidate_state, candidate_since = EXCLUDED.candidate_since,
				last_location_id = EXCLUDED.last_location_id, last_accuracy_m = EXCLUDED.last_accuracy_m,
				last_event = EXCLUDED.last_event, last_event_at = EXCLUDED.last_event_at,
				updated_at = now()`,
			deviceID, zone.id, state.state, state.since, candidate, state.candidateSince,
			state.lastLocationID, state.lastAccuracyM, lastEvent, state.lastEventAt); err != nil {
			return nil, err
		}
	}
	for _, event := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO zone_events (device_id, zone_id, event, occurred_at, location_id, accuracy_m, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, '{}'::jsonb)`,
			deviceID, event.ZoneID, event.Event, event.OccurredAt, event.LocationID, event.AccuracyM); err != nil {
			return nil, err
		}
	}

	last := inserted[len(inserted)-1].location
	inside := make([]domain.ZoneRef, 0)
	insideIDs := make([]string, 0)
	anyUnknown := false
	anyKnown := false
	for _, zone := range zoneRows {
		state := states[zone.id]
		if state == nil {
			anyUnknown = true
			continue
		}
		switch state.state {
		case "inside":
			anyKnown = true
			inside = append(inside, domain.ZoneRef{
				ID: zone.id, Name: zone.name, Kind: zone.kind, Color: zone.color, Priority: zone.priority,
			})
			insideIDs = append(insideIDs, zone.id)
		case "outside":
			anyKnown = true
		default:
			anyUnknown = true
		}
	}
	snapshot := &domain.ZoneState{
		Zone: "outside", ZoneIDs: insideIDs, Zones: inside, State: "outside",
		RecordedAt: last.RecordedAt, AccuracyM: last.AccuracyM, Source: last.LocationSource,
	}
	if len(inside) > 0 {
		snapshot.Zone = inside[0].Name
		snapshot.ZoneID = inside[0].ID
		snapshot.State = "inside"
	} else if anyUnknown || !anyKnown {
		snapshot.Zone = "unknown"
		snapshot.State = "unknown"
	}
	if len(events) > 0 {
		snapshot.Event = events[len(events)-1].Event
		eventAt := events[len(events)-1].OccurredAt
		snapshot.EventAt = &eventAt
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return snapshot, nil
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

func (p *Postgres) ListZones(ctx context.Context) ([]domain.Zone, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT z.id, z.name, z.kind, z.color, z.hysteresis_m, z.dwell_seconds, z.version,
			ST_AsGeoJSON(z.geom), ST_Area(z.geom), z.created_at, z.updated_at,
			COALESCE(array_agg(dz.device_id) FILTER (WHERE dz.device_id IS NOT NULL), '{}')
		FROM zones z
		LEFT JOIN device_zones dz ON dz.zone_id = z.id
		GROUP BY z.id
		ORDER BY lower(z.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	zones := make([]domain.Zone, 0)
	for rows.Next() {
		zone, err := scanZone(rows)
		if err != nil {
			return nil, err
		}
		zones = append(zones, zone)
	}
	return zones, rows.Err()
}

func (p *Postgres) GetZone(ctx context.Context, id string) (domain.Zone, error) {
	row := p.pool.QueryRow(ctx, `
		SELECT z.id, z.name, z.kind, z.color, z.hysteresis_m, z.dwell_seconds, z.version,
			ST_AsGeoJSON(z.geom), ST_Area(z.geom), z.created_at, z.updated_at,
			COALESCE(array_agg(dz.device_id) FILTER (WHERE dz.device_id IS NOT NULL), '{}')
		FROM zones z
		LEFT JOIN device_zones dz ON dz.zone_id = z.id
		WHERE z.id = $1
		GROUP BY z.id`, id)
	zone, err := scanZone(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Zone{}, ErrNotFound
	}
	return zone, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanZone(row rowScanner) (domain.Zone, error) {
	var zone domain.Zone
	var geometry string
	if err := row.Scan(&zone.ID, &zone.Name, &zone.Kind, &zone.Color, &zone.HysteresisM,
		&zone.DwellSeconds, &zone.Version, &geometry, &zone.AreaM2, &zone.CreatedAt,
		&zone.UpdatedAt, &zone.DeviceIDs); err != nil {
		return domain.Zone{}, err
	}
	zone.Geometry = json.RawMessage(geometry)
	return zone, nil
}

func (p *Postgres) CreateZone(ctx context.Context, id string, input domain.ZoneInput) (domain.Zone, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.Zone{}, err
	}
	defer tx.Rollback(ctx)
	if err := validateZoneGeometry(ctx, tx, input.Geometry); err != nil {
		return domain.Zone{}, err
	}
	deviceIDs := uniqueStrings(input.DeviceIDs)
	if len(deviceIDs) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM devices WHERE id = ANY($1)`, deviceIDs).Scan(&count); err != nil {
			return domain.Zone{}, err
		}
		if count != len(deviceIDs) {
			return domain.Zone{}, ErrNotFound
		}
	}
	var zone domain.Zone
	var geometry string
	err = tx.QueryRow(ctx, `
		WITH input AS (
			SELECT ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($7), 4326)) AS g
		), prepared AS (
			SELECT g,
				CASE WHEN ST_IsEmpty(ST_SimplifyPreserveTopology(g, 0.00005))
					THEN g ELSE ST_Multi(ST_SimplifyPreserveTopology(g, 0.00005)) END AS dg
			FROM input
		)
		INSERT INTO zones (id, name, kind, color, hysteresis_m, dwell_seconds, geom, device_geom)
		SELECT $1, $2, $3, $4, $5, $6, g::geography, dg::geography FROM prepared
		RETURNING id, name, kind, color, hysteresis_m, dwell_seconds, version,
			ST_AsGeoJSON(geom), ST_Area(geom), created_at, updated_at`,
		id, input.Name, input.Kind, input.Color, input.HysteresisM, input.DwellSeconds,
		string(input.Geometry)).Scan(&zone.ID, &zone.Name, &zone.Kind, &zone.Color,
		&zone.HysteresisM, &zone.DwellSeconds, &zone.Version, &geometry, &zone.AreaM2,
		&zone.CreatedAt, &zone.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Zone{}, ErrDuplicate
		}
		return domain.Zone{}, err
	}
	zone.Geometry = json.RawMessage(geometry)
	zone.DeviceIDs = deviceIDs
	for _, deviceID := range deviceIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO device_zones (device_id, zone_id) VALUES ($1, $2)`,
			deviceID, id); err != nil {
			return domain.Zone{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Zone{}, err
	}
	return zone, nil
}

func (p *Postgres) UpdateZone(ctx context.Context, id string, patch domain.ZonePatch) (domain.Zone, []string, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.Zone{}, nil, err
	}
	defer tx.Rollback(ctx)

	var current domain.Zone
	var geometry string
	err = tx.QueryRow(ctx, `
		SELECT id, name, kind, color, hysteresis_m, dwell_seconds, version,
			ST_AsGeoJSON(geom), ST_Area(geom), created_at, updated_at
		FROM zones WHERE id = $1 FOR UPDATE`, id).Scan(&current.ID, &current.Name, &current.Kind,
		&current.Color, &current.HysteresisM, &current.DwellSeconds, &current.Version,
		&geometry, &current.AreaM2, &current.CreatedAt, &current.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Zone{}, nil, ErrNotFound
	}
	if err != nil {
		return domain.Zone{}, nil, err
	}
	current.Geometry = json.RawMessage(geometry)

	oldDeviceIDs := make([]string, 0)
	rows, err := tx.Query(ctx, `SELECT device_id FROM device_zones WHERE zone_id = $1 ORDER BY device_id`, id)
	if err != nil {
		return domain.Zone{}, nil, err
	}
	for rows.Next() {
		var deviceID string
		if err := rows.Scan(&deviceID); err != nil {
			rows.Close()
			return domain.Zone{}, nil, err
		}
		oldDeviceIDs = append(oldDeviceIDs, deviceID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.Zone{}, nil, err
	}

	zoneChanged := false
	if patch.Name != nil && *patch.Name != current.Name {
		current.Name = *patch.Name
		zoneChanged = true
	}
	if patch.Kind != nil && *patch.Kind != current.Kind {
		current.Kind = *patch.Kind
		zoneChanged = true
	}
	if patch.Color != nil && *patch.Color != current.Color {
		current.Color = *patch.Color
		zoneChanged = true
	}
	if patch.HysteresisM != nil && *patch.HysteresisM != current.HysteresisM {
		current.HysteresisM = *patch.HysteresisM
		zoneChanged = true
	}
	if patch.DwellSeconds != nil && *patch.DwellSeconds != current.DwellSeconds {
		current.DwellSeconds = *patch.DwellSeconds
		zoneChanged = true
	}
	if len(patch.Geometry) > 0 && string(patch.Geometry) != "null" {
		if err := validateZoneGeometry(ctx, tx, patch.Geometry); err != nil {
			return domain.Zone{}, nil, err
		}
		zoneChanged = true
	}

	newDeviceIDs := oldDeviceIDs
	if patch.DeviceIDs != nil {
		newDeviceIDs = uniqueStrings(*patch.DeviceIDs)
	}
	affected := uniqueStrings(append(append([]string{}, oldDeviceIDs...), newDeviceIDs...))

	if zoneChanged || patch.DeviceIDs != nil {
		if len(patch.Geometry) > 0 && string(patch.Geometry) != "null" {
			_, err = tx.Exec(ctx, `
				WITH input AS (
					SELECT ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($7), 4326)) AS g
				), prepared AS (
					SELECT g,
						CASE WHEN ST_IsEmpty(ST_SimplifyPreserveTopology(g, 0.00005))
							THEN g ELSE ST_Multi(ST_SimplifyPreserveTopology(g, 0.00005)) END AS dg
					FROM input
				)
				UPDATE zones SET name = $2, kind = $3, color = $4, hysteresis_m = $5,
					dwell_seconds = $6, geom = prepared.g::geography,
					device_geom = prepared.dg::geography,
					version = version + 1, updated_at = now()
				FROM prepared WHERE zones.id = $1`,
				id, current.Name, current.Kind, current.Color, current.HysteresisM,
				current.DwellSeconds, string(patch.Geometry))
		} else {
			_, err = tx.Exec(ctx, `UPDATE zones SET name = $2, kind = $3, color = $4,
				hysteresis_m = $5, dwell_seconds = $6,
				version = version + CASE WHEN $7 THEN 1 ELSE 0 END, updated_at = now()
				WHERE id = $1`,
				id, current.Name, current.Kind, current.Color, current.HysteresisM,
				current.DwellSeconds, zoneChanged)
		}
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.Zone{}, nil, ErrDuplicate
			}
			return domain.Zone{}, nil, err
		}
	}
	if patch.DeviceIDs != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM device_zones WHERE zone_id = $1`, id); err != nil {
			return domain.Zone{}, nil, err
		}
		if len(newDeviceIDs) > 0 {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM devices WHERE id = ANY($1)`, newDeviceIDs).Scan(&count); err != nil {
				return domain.Zone{}, nil, err
			}
			if count != len(newDeviceIDs) {
				return domain.Zone{}, nil, ErrNotFound
			}
		}
		for _, deviceID := range newDeviceIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO device_zones (device_id, zone_id) VALUES ($1, $2)`,
				deviceID, id); err != nil {
				return domain.Zone{}, nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Zone{}, nil, err
	}
	updated, err := p.GetZone(ctx, id)
	if err != nil {
		return domain.Zone{}, nil, err
	}
	return updated, affected, nil
}

func (p *Postgres) DeleteZone(ctx context.Context, id string) ([]string, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	deviceIDs := make([]string, 0)
	rows, err := tx.Query(ctx, `SELECT device_id FROM device_zones WHERE zone_id = $1 ORDER BY device_id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var deviceID string
		if err := rows.Scan(&deviceID); err != nil {
			rows.Close()
			return nil, err
		}
		deviceIDs = append(deviceIDs, deviceID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result, err := tx.Exec(ctx, `DELETE FROM zones WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return deviceIDs, nil
}

func (p *Postgres) ListDeviceZones(ctx context.Context, deviceID string, withGeometry bool) ([]domain.Zone, error) {
	geometryColumn := "NULL::text"
	if withGeometry {
		geometryColumn = "ST_AsGeoJSON(z.device_geom)"
	}
	rows, err := p.pool.Query(ctx, fmt.Sprintf(`
		SELECT z.id, z.name, z.kind, z.color, z.hysteresis_m, z.dwell_seconds, z.version,
			dz.priority, %s, z.created_at, z.updated_at
		FROM zones z
		JOIN device_zones dz ON dz.zone_id = z.id
		WHERE dz.device_id = $1
		ORDER BY dz.priority DESC, lower(z.name)`, geometryColumn), deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	zones := make([]domain.Zone, 0)
	for rows.Next() {
		var zone domain.Zone
		var geometry *string
		if err := rows.Scan(&zone.ID, &zone.Name, &zone.Kind, &zone.Color, &zone.HysteresisM,
			&zone.DwellSeconds, &zone.Version, &zone.Priority, &geometry,
			&zone.CreatedAt, &zone.UpdatedAt); err != nil {
			return nil, err
		}
		if geometry != nil {
			zone.Geometry = json.RawMessage(*geometry)
		}
		zones = append(zones, zone)
	}
	return zones, rows.Err()
}

func (p *Postgres) ZoneEventsHistory(ctx context.Context, deviceID string, from, to time.Time, limit int) ([]domain.ZoneEvent, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT e.id, e.device_id, e.zone_id, COALESCE(z.name, ''), e.event, e.occurred_at,
			e.location_id, e.accuracy_m, e.metadata, e.created_at
		FROM zone_events e
		LEFT JOIN zones z ON z.id = e.zone_id
		WHERE e.device_id = $1 AND e.occurred_at >= $2 AND e.occurred_at <= $3
		ORDER BY e.occurred_at DESC LIMIT $4`, deviceID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.ZoneEvent, 0)
	for rows.Next() {
		var event domain.ZoneEvent
		var metadata []byte
		if err := rows.Scan(&event.ID, &event.DeviceID, &event.ZoneID, &event.ZoneName, &event.Event,
			&event.OccurredAt, &event.LocationID, &event.AccuracyM, &metadata, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Metadata = json.RawMessage(metadata)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (p *Postgres) AcknowledgeZones(ctx context.Context, deviceID, etag string) error {
	result, err := p.pool.Exec(ctx, `UPDATE devices SET applied_zones_etag = $2,
		applied_zones_at = now(), updated_at = now() WHERE id = $1`, deviceID, etag)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func validateZoneGeometry(ctx context.Context, tx pgx.Tx, geometry json.RawMessage) error {
	var valid, empty bool
	var geometryType string
	var area float64
	err := tx.QueryRow(ctx, `
		SELECT ST_IsValid(g), ST_IsEmpty(g), GeometryType(g), ST_Area(g::geography)
		FROM (SELECT ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($1), 4326)) AS g) s`,
		string(geometry)).Scan(&valid, &empty, &geometryType, &area)
	if err != nil {
		return ErrInvalidZoneGeometry
	}
	if !valid || empty || (geometryType != "POLYGON" && geometryType != "MULTIPOLYGON") || area < 1 {
		return ErrInvalidZoneGeometry
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (p *Postgres) allDeviceZones(ctx context.Context) (map[string][]domain.Zone, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT dz.device_id, z.id, z.name, z.kind, z.color, z.hysteresis_m,
			z.dwell_seconds, z.version, dz.priority, z.created_at, z.updated_at
		FROM device_zones dz
		JOIN zones z ON z.id = dz.zone_id
		ORDER BY dz.device_id, dz.priority DESC, lower(z.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]domain.Zone)
	for rows.Next() {
		var deviceID string
		var zone domain.Zone
		if err := rows.Scan(&deviceID, &zone.ID, &zone.Name, &zone.Kind, &zone.Color,
			&zone.HysteresisM, &zone.DwellSeconds, &zone.Version, &zone.Priority,
			&zone.CreatedAt, &zone.UpdatedAt); err != nil {
			return nil, err
		}
		result[deviceID] = append(result[deviceID], zone)
	}
	return result, rows.Err()
}

func (p *Postgres) ListDevices(ctx context.Context) ([]domain.Device, error) {
	zonesByDevice, err := p.allDeviceZones(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `
		SELECT d.id, d.name, d.activated_at IS NOT NULL AND d.revoked_at IS NULL, d.revoked_at IS NOT NULL, d.created_at, d.last_seen,
			d.battery_percent, d.charging, d.applied_config_version, d.applied_config_at,
			d.applied_enabled, d.applied_location_interval_seconds, d.applied_sync_interval_seconds,
			d.applied_sleep_duration_seconds, d.applied_gps_timeout_seconds, d.applied_min_distance_meters,
			d.applied_zones_etag, d.applied_zones_at,
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
		var appliedZonesETag *string
		var appliedZonesAt *time.Time
		var recordedAt *time.Time
		if err := rows.Scan(&d.ID, &d.Name, &d.Activated, &d.Revoked, &d.CreatedAt, &d.LastSeen,
			&deviceBattery, &deviceCharging,
			&appliedConfigVersion, &appliedConfigAt, &appliedEnabled, &appliedLocationInterval,
			&appliedSyncInterval, &appliedSleepDuration, &appliedGPSTimeout, &appliedMinDistance,
			&appliedZonesETag, &appliedZonesAt,
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
		d.AppliedZonesETag = appliedZonesETag
		d.AppliedZonesAt = appliedZonesAt
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
		d.Zones = zonesByDevice[d.ID]
		d.ZonesETag = domain.ZonesETag(d.Zones)
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
	if err != nil {
		return domain.Device{}, err
	}
	zones, err := p.ListDeviceZones(ctx, d.ID, false)
	if err != nil {
		return domain.Device{}, err
	}
	d.Zones = zones
	d.ZonesETag = domain.ZonesETag(zones)
	return d, nil
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

func (p *Postgres) CreateUser(ctx context.Context, id, username, displayName, passwordHash, role string) (domain.User, error) {
	var user domain.User
	err := p.pool.QueryRow(ctx, `INSERT INTO users (id, username, display_name, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, username, display_name, role, created_at, last_login_at`,
		id, username, displayName, passwordHash, role).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.CreatedAt, &user.LastLoginAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return domain.User{}, ErrDuplicate
	}
	return user, err
}

func (p *Postgres) UserByUsername(ctx context.Context, username string) (domain.User, error) {
	var user domain.User
	err := p.pool.QueryRow(ctx, `SELECT id, username, display_name, password_hash, role, created_at, last_login_at
		FROM users WHERE username = $1`, username).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Role, &user.CreatedAt, &user.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	return user, err
}

func (p *Postgres) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, username, display_name, role, created_at, last_login_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]domain.User, 0)
	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.CreatedAt, &user.LastLoginAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (p *Postgres) MarkUserLogin(ctx context.Context, userID string) error {
	result, err := p.pool.Exec(ctx, `UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) CreateSession(ctx context.Context, tokenHash []byte, userID *string, role, label string, expiresAt time.Time) error {
	if _, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, role, label, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, tokenHash, userID, role, label, expiresAt)
	return err
}

func (p *Postgres) SessionByToken(ctx context.Context, tokenHash []byte) (domain.Session, error) {
	var session domain.Session
	var username, displayName *string
	err := p.pool.QueryRow(ctx, `SELECT s.user_id, s.role, s.label, s.expires_at, u.username, u.display_name
		FROM sessions s LEFT JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash).
		Scan(&session.UserID, &session.Role, &session.Label, &session.ExpiresAt, &username, &displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, err
	}
	if session.UserID != nil {
		if username == nil || displayName == nil {
			return domain.Session{}, ErrNotFound
		}
		session.Username = *username
		session.DisplayName = *displayName
	}
	return session, nil
}

func (p *Postgres) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}
