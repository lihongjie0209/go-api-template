DROP INDEX platform_configs_type_idx ON platform_configs;
ALTER TABLE platform_configs DROP COLUMN value_type;
