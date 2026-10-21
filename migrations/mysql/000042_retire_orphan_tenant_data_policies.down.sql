-- Intentionally irreversible: restoring policies for deleted tenants could
-- reactivate obsolete authorization. See docs/design/historical-tenant-policy-cleanup.md.
SELECT 1;
