-- A receipt belongs to the open stretch in which it was registered.
ALTER TABLE todos ADD COLUMN reopen_generation bigint NOT NULL DEFAULT 0;

-- Also fence a reopen written by an older binary after a rollback.
CREATE FUNCTION advance_todo_reopen_generation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.reopen_generation := OLD.reopen_generation + 1;
    RETURN NEW;
END;
$$;

CREATE TRIGGER todos_reopen_generation BEFORE UPDATE OF done_at ON todos
    FOR EACH ROW WHEN (OLD.done_at IS NOT NULL AND NEW.done_at IS NULL)
    EXECUTE FUNCTION advance_todo_reopen_generation();
