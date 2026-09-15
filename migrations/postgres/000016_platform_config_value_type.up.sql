ALTER TABLE platform_configs
    ADD COLUMN value_type text NOT NULL DEFAULT 'object'
    CHECK (value_type IN ('string', 'number', 'boolean', 'object', 'array', 'null'));

CREATE INDEX platform_configs_type_idx
    ON platform_configs (value_type, category)
    WHERE deleted_at IS NULL;
