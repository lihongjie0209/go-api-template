DROP INDEX IF EXISTS platform_configs_type_idx;
ALTER TABLE platform_configs DROP COLUMN IF EXISTS value_type;
