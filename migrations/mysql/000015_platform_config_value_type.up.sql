ALTER TABLE platform_configs
    ADD COLUMN value_type varchar(16) NOT NULL DEFAULT 'object';

CREATE INDEX platform_configs_type_idx
    ON platform_configs (value_type, category);
