ALTER TABLE identity_service_accounts
    ADD COLUMN credential_generation bigint NOT NULL DEFAULT 1;
