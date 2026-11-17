CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    enrollment_code_hash BYTEA,
    enrollment_expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '24 hours'),
    device_token_hash BYTEA UNIQUE,
    activated_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    last_seen TIMESTAMPTZ,
    battery_percent SMALLINT CHECK (battery_percent BETWEEN 0 AND 100),
    charging BOOLEAN,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    update_interval_seconds INTEGER NOT NULL DEFAULT 900,
    sleep_duration_seconds INTEGER NOT NULL DEFAULT 3600,
    gps_timeout_seconds INTEGER NOT NULL DEFAULT 60,
    min_distance_meters DOUBLE PRECISION NOT NULL DEFAULT 0,
    config_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS locations (
    id BIGSERIAL PRIMARY KEY,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    client_location_id TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    latitude DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    accuracy_meters DOUBLE PRECISION NOT NULL CHECK (accuracy_meters >= 0),
    altitude_meters DOUBLE PRECISION,
    speed_meters_per_second DOUBLE PRECISION,
    bearing_degrees DOUBLE PRECISION,
    battery_percent SMALLINT CHECK (battery_percent BETWEEN 0 AND 100),
    charging BOOLEAN,
    point GEOGRAPHY(POINT, 4326) NOT NULL,
    UNIQUE (device_id, client_location_id)
);

CREATE INDEX IF NOT EXISTS locations_device_recorded_at_idx ON locations (device_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS locations_point_gix ON locations USING GIST (point);
