-- Stage 8 identity expansion. Backstory is an owner-provided fictional
-- identity seed, not a factual-memory record or a source of permissions.
ALTER TABLE agent_profiles
    ADD COLUMN backstory TEXT NOT NULL DEFAULT ''
    CHECK (length(backstory) <= 12000);
