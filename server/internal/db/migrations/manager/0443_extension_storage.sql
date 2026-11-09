-- Manager-owned business data. Credentials remain in the existing sealed
-- credential tables and are never restored through these spaces.
CREATE TABLE extension_storage_spaces (
    id text PRIMARY KEY,
    plugin text REFERENCES plugins(id) ON DELETE CASCADE,
    extension_key text NOT NULL,
    installed_at timestamptz,
    runtime text NOT NULL DEFAULT '',
    retained boolean NOT NULL DEFAULT false,
    retired_at timestamptz,
    restore_request text NOT NULL DEFAULT '',
    restore_fingerprint text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 0,
    state jsonb NOT NULL,
    CHECK (octet_length(id) BETWEEN 20 AND 80),
    CHECK ((plugin IS NULL) = (installed_at IS NULL)),
    CHECK (octet_length(state::text) <= 12582912)
);
CREATE UNIQUE INDEX extension_storage_active_plugin ON extension_storage_spaces(plugin) WHERE plugin IS NOT NULL;
CREATE INDEX extension_storage_retained_key ON extension_storage_spaces(extension_key, retired_at) WHERE retained;
