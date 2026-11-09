-- Each extension storage space's encoded size, stored once per write (#2375, docs/extensions/extensions.md "Bounds and
-- failure semantics"). Every storage write checks the aggregate budget under one global lock; summing
-- octet_length(state::text) there turned every active and retained document (up to 12 MiB each) back into text.
-- state_bytes is the same number, kept by Postgres on insert and update, so the budget and the retained list read it
-- instead. The 12 MiB document bound moves onto it: checking both would convert each written document twice. One
-- statement, so the table is rewritten once.
ALTER TABLE extension_storage_spaces
    DROP CONSTRAINT extension_storage_spaces_state_check,
    ADD COLUMN state_bytes bigint GENERATED ALWAYS AS (octet_length(state::text)) STORED,
    ADD CONSTRAINT extension_storage_spaces_state_bytes_check CHECK (state_bytes <= 12582912);
