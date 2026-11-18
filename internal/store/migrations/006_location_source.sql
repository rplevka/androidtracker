ALTER TABLE locations
    ADD COLUMN IF NOT EXISTS location_source TEXT;
