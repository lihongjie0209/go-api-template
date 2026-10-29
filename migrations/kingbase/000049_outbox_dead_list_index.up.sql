CREATE INDEX event_outbox_dead_list_idx ON event_outbox (dead_at DESC, id)
    WHERE dead_at IS NOT NULL AND published_at IS NULL AND deleted_at IS NULL;
