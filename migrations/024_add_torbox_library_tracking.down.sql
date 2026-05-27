DROP INDEX IF EXISTS idx_media_stream_options_tb_library_pending;
DROP INDEX IF EXISTS idx_media_streams_tb_library_pending;

ALTER TABLE media_stream_options DROP COLUMN IF EXISTS tb_torrent_id;
ALTER TABLE media_stream_options DROP COLUMN IF EXISTS tb_library_added;

ALTER TABLE media_streams DROP COLUMN IF EXISTS tb_library_added_at;
ALTER TABLE media_streams DROP COLUMN IF EXISTS tb_torrent_id;
ALTER TABLE media_streams DROP COLUMN IF EXISTS tb_library_added;
