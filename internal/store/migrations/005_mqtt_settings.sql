CREATE TABLE IF NOT EXISTS mqtt_settings (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    host TEXT NOT NULL DEFAULT 'localhost',
    port INTEGER NOT NULL DEFAULT 1883 CHECK (port BETWEEN 1 AND 65535),
    username TEXT NOT NULL DEFAULT '',
    password_ciphertext BYTEA,
    tls BOOLEAN NOT NULL DEFAULT FALSE,
    discovery_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    discovery_prefix TEXT NOT NULL DEFAULT 'homeassistant',
    base_topic TEXT NOT NULL DEFAULT 'kidstracker',
    client_id TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO mqtt_settings (id) VALUES (TRUE) ON CONFLICT (id) DO NOTHING;
