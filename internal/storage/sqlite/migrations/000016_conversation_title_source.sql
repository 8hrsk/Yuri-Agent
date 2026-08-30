-- Conversation titles have an explicit owner so an asynchronous generated
-- title can never overwrite a title renamed by the local owner.
ALTER TABLE conversations
    ADD COLUMN title_source TEXT NOT NULL DEFAULT 'default'
    CHECK (title_source IN ('default', 'generated', 'user'));

-- Rows written before title ownership existed are reconstructable from their
-- value: the neutral placeholder is still eligible for generation, while any
-- custom title was necessarily chosen by the owner.
UPDATE conversations
SET title_source = CASE
    WHEN trim(title) = 'Новый диалог' THEN 'default'
    ELSE 'user'
END
WHERE title_source = 'default';
