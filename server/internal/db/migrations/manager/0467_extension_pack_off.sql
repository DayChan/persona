-- The members an extension pack switched off when the pack was switched off (docs/extensions/extensions.md "Dependencies,
-- exports and packs", #1830): switching the pack back on turns these on again, and not the ones the user turned off.
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS pack_off jsonb NOT NULL DEFAULT '[]';
