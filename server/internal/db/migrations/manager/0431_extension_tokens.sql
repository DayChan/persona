-- The record of the extensions' tokens a manager issued and revoked (docs/extensions/extensions.md "Permissions", #1156):
-- the token's SHA-256 (never the token: the check refuses anything but a hex digest), the manager and extension it was
-- for, what it opened (scopes, or all_scopes for one of every scope), when it was issued, and when and why it stopped.
-- The manager authorizes only from what it issued since it started: nothing here is read back as a credential, and a
-- start revokes the rows its last run left live. At most one live row per extension of a manager. Revoked rows are
-- dropped after 7 days.
CREATE TABLE IF NOT EXISTS extension_tokens (
	hash       text        PRIMARY KEY CHECK (hash ~ '^[0-9a-f]{64}$'),
	manager    text        NOT NULL,
	extension  text        NOT NULL,
	name       text        NOT NULL DEFAULT '',
	scopes     text[]      NOT NULL DEFAULT '{}',
	all_scopes boolean     NOT NULL DEFAULT false,
	issued_at  timestamptz NOT NULL DEFAULT now(),
	revoked_at timestamptz,
	reason     text        NOT NULL DEFAULT '',
	CHECK ((revoked_at IS NULL) = (reason = ''))
);
CREATE UNIQUE INDEX IF NOT EXISTS extension_tokens_live ON extension_tokens (manager, extension) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS extension_tokens_revoked ON extension_tokens (manager, revoked_at) WHERE revoked_at IS NOT NULL;
