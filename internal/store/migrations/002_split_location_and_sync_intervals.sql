DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'devices'
          AND column_name = 'update_interval_seconds'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'devices'
          AND column_name = 'location_interval_seconds'
    ) THEN
        ALTER TABLE devices RENAME COLUMN update_interval_seconds TO location_interval_seconds;
    END IF;
END $$;

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS sync_interval_seconds INTEGER NOT NULL DEFAULT 900;
