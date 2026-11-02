-- Log retention uses partitions; outbox retention uses independent time ranges.
CREATE INDEX event_outbox_published_cleanup_idx ON event_outbox (published_at, id)
    WHERE published_at IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX event_outbox_dead_cleanup_idx ON event_outbox (dead_at, id)
    WHERE dead_at IS NOT NULL AND deleted_at IS NULL;
