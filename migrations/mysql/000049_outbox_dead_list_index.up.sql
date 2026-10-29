CREATE INDEX event_outbox_dead_list_idx ON event_outbox (deleted_at, published_at, dead_at DESC, id);
