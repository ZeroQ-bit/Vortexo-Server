CREATE UNIQUE INDEX IF NOT EXISTS unique_episode_stream_idx
ON media_streams (series_id, season, episode)
WHERE series_id IS NOT NULL;
