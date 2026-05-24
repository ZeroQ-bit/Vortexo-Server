-- Align older user_history tables with the current user_store queries.
-- Early migrations used item_type/item_id; current code stores title/type/stream_id.

ALTER TABLE user_history
    ADD COLUMN IF NOT EXISTS stream_id INTEGER;

ALTER TABLE user_history
    ADD COLUMN IF NOT EXISTS title VARCHAR(500) DEFAULT '';

ALTER TABLE user_history
    ADD COLUMN IF NOT EXISTS type VARCHAR(50);

ALTER TABLE user_history
    ADD COLUMN IF NOT EXISTS duration INTEGER DEFAULT 0;

UPDATE user_history
SET stream_id = item_id
WHERE stream_id IS NULL
  AND item_id IS NOT NULL;

UPDATE user_history
SET stream_id = 0
WHERE stream_id IS NULL;

UPDATE user_history
SET type = item_type
WHERE (type IS NULL OR type = '')
  AND item_type IS NOT NULL;

UPDATE user_history
SET type = 'unknown'
WHERE type IS NULL
   OR type = '';

UPDATE user_history
SET title = ''
WHERE title IS NULL;

CREATE INDEX IF NOT EXISTS idx_user_history_stream
    ON user_history(user_id, stream_id);
