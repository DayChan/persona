ALTER TABLE plugins ADD COLUMN public_id text NOT NULL DEFAULT '';
ALTER TABLE plugins ADD COLUMN previous_id text NOT NULL DEFAULT '';

-- Names are reserved only while the actual installation owns its stable slot.
-- The original primary key and plugin_secrets foreign key never move.
CREATE TABLE plugin_identities (
    name text PRIMARY KEY,
    plugin text NOT NULL REFERENCES plugins(id) ON DELETE CASCADE
);
CREATE INDEX plugin_identities_plugin ON plugin_identities(plugin);
INSERT INTO plugin_identities (name, plugin) SELECT id, id FROM plugins;
CREATE UNIQUE INDEX plugins_public_id ON plugins(public_id) WHERE public_id <> '';
