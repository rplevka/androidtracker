ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS applied_config_version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS applied_config_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS applied_enabled BOOLEAN,
    ADD COLUMN IF NOT EXISTS applied_location_interval_seconds INTEGER,
    ADD COLUMN IF NOT EXISTS applied_sync_interval_seconds INTEGER,
    ADD COLUMN IF NOT EXISTS applied_sleep_duration_seconds INTEGER,
    ADD COLUMN IF NOT EXISTS applied_gps_timeout_seconds INTEGER,
    ADD COLUMN IF NOT EXISTS applied_min_distance_meters DOUBLE PRECISION;
