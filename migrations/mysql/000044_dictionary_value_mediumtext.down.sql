-- Strict mode makes narrowing fail instead of silently truncating stored values.
SET @dictionary_previous_sql_mode=@@SESSION.sql_mode;
SET SESSION sql_mode='STRICT_ALL_TABLES';
ALTER TABLE dictionary_items MODIFY COLUMN value TEXT NOT NULL;
SET SESSION sql_mode=@dictionary_previous_sql_mode;
