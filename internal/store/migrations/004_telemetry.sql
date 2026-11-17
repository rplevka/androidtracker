ALTER TABLE locations
    ADD COLUMN IF NOT EXISTS screen_on BOOLEAN,
    ADD COLUMN IF NOT EXISTS device_locked BOOLEAN,
    ADD COLUMN IF NOT EXISTS device_idle BOOLEAN,
    ADD COLUMN IF NOT EXISTS power_save_mode BOOLEAN,
    ADD COLUMN IF NOT EXISTS standby_bucket TEXT,
    ADD COLUMN IF NOT EXISTS network_type TEXT,
    ADD COLUMN IF NOT EXISTS gps_fix_duration_ms BIGINT;

CREATE TABLE IF NOT EXISTS device_telemetry (
    id BIGSERIAL PRIMARY KEY,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    recorded_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    battery_percent SMALLINT CHECK (battery_percent BETWEEN 0 AND 100),
    charging BOOLEAN,
    screen_on BOOLEAN,
    device_locked BOOLEAN,
    device_idle BOOLEAN,
    power_save_mode BOOLEAN,
    standby_bucket TEXT,
    network_type TEXT,
    gps_enabled BOOLEAN,
    battery_temperature_c DOUBLE PRECISION,
    wake_delay_seconds INTEGER,
    sync_duration_ms BIGINT,
    queue_size INTEGER,
    config_version BIGINT,
    UNIQUE (device_id, recorded_at)
);

CREATE INDEX IF NOT EXISTS device_telemetry_device_recorded_at_idx
    ON device_telemetry (device_id, recorded_at DESC);
