CREATE TABLE IF NOT EXISTS zones (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    kind TEXT NOT NULL DEFAULT 'other' CHECK (kind IN ('home', 'school', 'work', 'other')),
    color TEXT NOT NULL DEFAULT '#0d7b74',
    hysteresis_m DOUBLE PRECISION NOT NULL DEFAULT 25 CHECK (hysteresis_m BETWEEN 0 AND 1000),
    dwell_seconds INTEGER NOT NULL DEFAULT 0 CHECK (dwell_seconds BETWEEN 0 AND 86400),
    geom GEOGRAPHY(MULTIPOLYGON, 4326) NOT NULL,
    device_geom GEOGRAPHY(MULTIPOLYGON, 4326) NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS zones_name_uidx ON zones (lower(name));
CREATE INDEX IF NOT EXISTS zones_geom_gix ON zones USING GIST (geom);

CREATE TABLE IF NOT EXISTS device_zones (
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    zone_id TEXT NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    priority INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, zone_id)
);

CREATE INDEX IF NOT EXISTS device_zones_zone_idx ON device_zones (zone_id);

CREATE TABLE IF NOT EXISTS device_zone_state (
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    zone_id TEXT NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'unknown' CHECK (state IN ('inside', 'outside', 'unknown')),
    since TIMESTAMPTZ NOT NULL DEFAULT now(),
    candidate_state TEXT CHECK (candidate_state IN ('inside', 'outside')),
    candidate_since TIMESTAMPTZ,
    last_location_id BIGINT REFERENCES locations(id) ON DELETE SET NULL,
    last_accuracy_m DOUBLE PRECISION,
    last_event TEXT CHECK (last_event IN ('enter', 'exit')),
    last_event_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, zone_id)
);

CREATE TABLE IF NOT EXISTS zone_events (
    id BIGSERIAL PRIMARY KEY,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    zone_id TEXT NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    event TEXT NOT NULL CHECK (event IN ('enter', 'exit')),
    occurred_at TIMESTAMPTZ NOT NULL,
    location_id BIGINT REFERENCES locations(id) ON DELETE SET NULL,
    accuracy_m DOUBLE PRECISION,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS zone_events_device_time_idx ON zone_events (device_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS zone_events_zone_time_idx ON zone_events (zone_id, occurred_at DESC);

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS applied_zones_etag TEXT,
    ADD COLUMN IF NOT EXISTS applied_zones_at TIMESTAMPTZ;
