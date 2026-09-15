DROP INDEX IF EXISTS platform_configs_type_idx;
ALTER TABLE platform_configs DROP COLUMN value_type;
