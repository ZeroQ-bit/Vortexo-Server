DROP INDEX IF EXISTS idx_media_stream_options_hash;
DROP INDEX IF EXISTS idx_media_stream_options_episode_lookup;
DROP INDEX IF EXISTS idx_media_stream_options_movie_lookup;
DROP INDEX IF EXISTS unique_episode_stream_option_idx;
DROP INDEX IF EXISTS unique_movie_stream_option_idx;
DROP TABLE IF EXISTS media_stream_options;

DELETE FROM schema_migrations WHERE version = 22;
