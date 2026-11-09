-- The independent review of a supervised task's finished change (#2173, docs/assistant/unified-entry.md "Independent
-- review"): the latest run on the task's ledger, as JSON the assistant owns (its reviewer agent, the review copy it runs
-- in, the round it reviewed and what it found). NULL when no review ran.
ALTER TABLE todo_supervision ADD COLUMN IF NOT EXISTS auto_review jsonb;
