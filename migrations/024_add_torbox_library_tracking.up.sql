ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS tb_library_added BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS tb_torrent_id TEXT;

ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS tb_library_added_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_media_streams_tb_library_pending
ON media_streams (tb_library_added, media_type, media_id)
WHERE tb_library_added = FALSE;

ALTER TABLE media_stream_options
ADD COLUMN IF NOT EXISTS tb_library_added BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE media_stream_options
ADD COLUMN IF NOT EXISTS tb_torrent_id TEXT;

CREATE INDEX IF NOT EXISTS idx_media_stream_options_tb_library_pending
ON media_stream_options (tb_library_added, media_type, media_id)
WHERE tb_library_added = FALSE;
