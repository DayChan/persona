-- Empty for existing installs: a mutable market must never retroactively grant
-- the sandbox exemption of an officially verified package.
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS market_key text NOT NULL DEFAULT '';
-- The previous version's own provenance, restored by a rollback. Empty for rows
-- upgraded before it was kept: a rollback then drops the verified key.
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS previous_origin text NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS previous_market_key text NOT NULL DEFAULT '';
