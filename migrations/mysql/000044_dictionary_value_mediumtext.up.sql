-- Dictionary values permit 1 MiB in the API; MySQL TEXT only holds 64 KiB.
ALTER TABLE dictionary_items MODIFY COLUMN value MEDIUMTEXT NOT NULL;
