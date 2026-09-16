-- Intentionally irreversible: route authorization is now keyed by the PBAC
-- resource/action registry and restoring path-owned policy data would create a
-- second authorization source of truth.
SELECT 1;
