package database

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
)

type SettingsStore struct {
	db *sql.DB
}

func NewSettingsStore(db *sql.DB) *SettingsStore {
	return &SettingsStore{db: db}
}

// Get retrieves a single setting by key
func (s *SettingsStore) Get(ctx context.Context, key string) (*models.Settings, error) {
	query := `SELECT id, key, value, type, updated_at FROM settings WHERE key = $1`

	setting := &models.Settings{}
	err := s.db.QueryRowContext(ctx, query, key).Scan(
		&setting.ID,
		&setting.Key,
		&setting.Value,
		&setting.Type,
		&setting.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	return setting, err
}

// Set updates or inserts a setting
func (s *SettingsStore) Set(ctx context.Context, key, value, typ string) error {
	query := `
		INSERT INTO settings (key, value, type, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (key) 
		DO UPDATE SET value = $2, type = $3, updated_at = NOW()
	`

	_, err := s.db.ExecContext(ctx, query, key, value, typ)
	return err
}

// GetAll retrieves all settings as a SettingsResponse
func (s *SettingsStore) GetAll(ctx context.Context) (*models.SettingsResponse, error) {
	query := `SELECT key, value, type FROM settings`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value, typ string
		if err := rows.Scan(&key, &value, &typ); err != nil {
			return nil, err
		}
		settings[key] = value
	}

	return s.mapToResponse(settings), nil
}

// SetAll updates multiple settings at once
func (s *SettingsStore) SetAll(ctx context.Context, response *models.SettingsResponse) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	settingsMap := s.responseToMap(response)

	query := `
		INSERT INTO settings (key, value, type, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (key) 
		DO UPDATE SET value = $2, type = $3, updated_at = NOW()
	`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for key, value := range settingsMap {
		typ := "string"
		if _, err := strconv.Atoi(value); err == nil {
			typ = "int"
		} else if value == "true" || value == "false" {
			typ = "bool"
		}

		if _, err := stmt.ExecContext(ctx, key, value, typ); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Helper functions to convert between map and struct
func (s *SettingsStore) mapToResponse(m map[string]string) *models.SettingsResponse {
	getBool := func(key string) bool {
		return m[key] == "true"
	}
	getBoolWithDefault := func(key string, def bool) bool {
		if _, ok := m[key]; !ok {
			return def
		}
		return m[key] == "true"
	}
	getInt := func(key string, def int) int {
		if val, err := strconv.Atoi(m[key]); err == nil {
			return val
		}
		return def
	}
	getString := func(key string) string {
		return m[key]
	}
	getStringWithDefault := func(key, defaultVal string) string {
		if val, ok := m[key]; ok && val != "" {
			return val
		}
		return defaultVal
	}

	return &models.SettingsResponse{
		TMDBAPIKey:                      getString("tmdb_api_key"),
		FanartTVAPIKey:                  getString("fanart_tv_api_key"),
		RealDebridToken:                 getString("realdebrid_token"),
		TorBoxAPIKey:                    getString("torbox_api_key"),
		PremiumizeAPIKey:                getString("premiumize_api_key"),
		MDBListAPIKey:                   getString("mdblist_api_key"),
		OpenSubtitlesEnabled:            getBool("opensubtitles_enabled"),
		OpenSubtitlesAPIKey:             getString("opensubtitles_api_key"),
		OpenSubtitlesUsername:           getString("opensubtitles_username"),
		OpenSubtitlesPassword:           getString("opensubtitles_password"),
		OpenSubtitlesLanguages:          getStringWithDefault("opensubtitles_languages", "en"),
		UserCreatePlaylist:              getBool("user_create_playlist"),
		TotalPages:                      getInt("total_pages", 5),
		MinYear:                         getInt("min_year", 1970),
		MinRuntime:                      getInt("min_runtime", 30),
		Language:                        getString("language"),
		MoviesOriginCountry:             getString("movies_origin_country"),
		SeriesOriginCountry:             getString("series_origin_country"),
		M3U8Limit:                       getInt("m3u8_limit", 0),
		IncludeLiveTV:                   getBool("include_live_tv"),
		IncludeAdultVOD:                 getBool("include_adult_vod"),
		OnlyCachedStreams:               getBool("only_cached_streams"),
		OnlyReleasedContent:             getBool("only_released_content"),
		HideUnavailableContent:          getBool("hide_unavailable_content"),
		AutoAddBestStreamsToRealDebrid:  getBool("auto_add_best_streams_to_realdebrid"),
		AutoAddBestStreamsToTorBox:      getBool("auto_add_best_streams_to_torbox"),
		EnableQualityVariants:           getBool("enable_quality_variants"),
		ShowFullStreamName:              getBool("show_full_stream_name"),
		UseRealDebrid:                   getBool("use_realdebrid"),
		UseTorBox:                       getBool("use_torbox"),
		UsePremiumize:                   getBool("use_premiumize"),
		MediaFusionEnabled:              getBool("mediafusion_enabled"),
		TorrentioProviders:              getString("torrentio_providers"),
		DMMProviderEnabled:              getBool("dmm_provider_enabled"),
		DMMProviderURL:                  getStringWithDefault("dmm_provider_url", "https://debridmediamanager.com"),
		DMMLibraryImportEnabled:         getBool("dmm_library_import_enabled"),
		DMMLibraryFillMissingEnabled:    getBoolWithDefault("dmm_library_fill_missing_enabled", true),
		RDWebDAVLibraryEnabled:          getBool("rd_webdav_library_enabled"),
		RDWebDAVMountEnabled:            getBool("rd_webdav_mount_enabled"),
		RDWebDAVURL:                     getStringWithDefault("rd_webdav_url", "https://dav.real-debrid.com"),
		RDWebDAVUsername:                getString("rd_webdav_username"),
		RDWebDAVPassword:                getString("rd_webdav_password"),
		RDWebDAVMountPath:               getStringWithDefault("rd_webdav_mount_path", "/mnt/rd"),
		RDWebDAVLibraryPath:             getStringWithDefault("rd_webdav_library_path", "/app/rd-library"),
		RDWebDAVScanIntervalMinutes:     getInt("rd_webdav_scan_interval_minutes", 60),
		RDWebDAVCleanStaleSymlinks:      getBoolWithDefault("rd_webdav_clean_stale_symlinks", true),
		RDWebDAVPreferWebDAVLibraryOnly: getBool("rd_webdav_prefer_webdav_library_only"),
		RDWebDAVPartialScanFallback:     getBoolWithDefault("rd_webdav_partial_scan_fallback", true),
		IncludePopularMovies:            getBool("include_popular_movies"),
		IncludeTopRatedMovies:           getBool("include_top_rated_movies"),
		IncludeNowPlaying:               getBool("include_now_playing"),
		IncludeUpcoming:                 getBool("include_upcoming"),
		IncludeLatestReleasesMovies:     getBool("include_latest_releases_movies"),
		IncludeCollections:              getBool("include_collections"),
		IncludePopularSeries:            getBool("include_popular_series"),
		IncludeTopRatedSeries:           getBool("include_top_rated_series"),
		IncludeAiringToday:              getBool("include_airing_today"),
		IncludeOnTheAir:                 getBool("include_on_the_air"),
		IncludeLatestReleasesSeries:     getBool("include_latest_releases_series"),
		UserSetHost:                     getString("user_set_host"),
		ExpirationHours:                 getInt("expiration_hours", 3),
		AutoCacheIntervalHours:          getInt("auto_cache_interval_hours", 6),
		Timeout:                         getInt("timeout", 20),
		UseGithubForCache:               getBool("use_github_for_cache"),
		Debug:                           getBool("debug"),
		XtreamUsername:                  getStringWithDefault("xtream_username", "streamarr"),
		XtreamPassword:                  getStringWithDefault("xtream_password", "streamarr"),
	}
}

func (s *SettingsStore) responseToMap(r *models.SettingsResponse) map[string]string {
	return map[string]string{
		"tmdb_api_key":                         r.TMDBAPIKey,
		"fanart_tv_api_key":                    r.FanartTVAPIKey,
		"realdebrid_token":                     r.RealDebridToken,
		"torbox_api_key":                       r.TorBoxAPIKey,
		"premiumize_api_key":                   r.PremiumizeAPIKey,
		"mdblist_api_key":                      r.MDBListAPIKey,
		"opensubtitles_enabled":                fmt.Sprintf("%t", r.OpenSubtitlesEnabled),
		"opensubtitles_api_key":                r.OpenSubtitlesAPIKey,
		"opensubtitles_username":               r.OpenSubtitlesUsername,
		"opensubtitles_password":               r.OpenSubtitlesPassword,
		"opensubtitles_languages":              r.OpenSubtitlesLanguages,
		"user_create_playlist":                 fmt.Sprintf("%t", r.UserCreatePlaylist),
		"total_pages":                          fmt.Sprintf("%d", r.TotalPages),
		"min_year":                             fmt.Sprintf("%d", r.MinYear),
		"min_runtime":                          fmt.Sprintf("%d", r.MinRuntime),
		"language":                             r.Language,
		"movies_origin_country":                r.MoviesOriginCountry,
		"series_origin_country":                r.SeriesOriginCountry,
		"m3u8_limit":                           fmt.Sprintf("%d", r.M3U8Limit),
		"include_live_tv":                      fmt.Sprintf("%t", r.IncludeLiveTV),
		"include_adult_vod":                    fmt.Sprintf("%t", r.IncludeAdultVOD),
		"only_cached_streams":                  fmt.Sprintf("%t", r.OnlyCachedStreams),
		"only_released_content":                fmt.Sprintf("%t", r.OnlyReleasedContent),
		"hide_unavailable_content":             fmt.Sprintf("%t", r.HideUnavailableContent),
		"auto_add_best_streams_to_realdebrid":  fmt.Sprintf("%t", r.AutoAddBestStreamsToRealDebrid),
		"auto_add_best_streams_to_torbox":      fmt.Sprintf("%t", r.AutoAddBestStreamsToTorBox),
		"enable_quality_variants":              fmt.Sprintf("%t", r.EnableQualityVariants),
		"show_full_stream_name":                fmt.Sprintf("%t", r.ShowFullStreamName),
		"use_realdebrid":                       fmt.Sprintf("%t", r.UseRealDebrid),
		"use_torbox":                           fmt.Sprintf("%t", r.UseTorBox),
		"use_premiumize":                       fmt.Sprintf("%t", r.UsePremiumize),
		"mediafusion_enabled":                  fmt.Sprintf("%t", r.MediaFusionEnabled),
		"torrentio_providers":                  r.TorrentioProviders,
		"dmm_provider_enabled":                 fmt.Sprintf("%t", r.DMMProviderEnabled),
		"dmm_provider_url":                     r.DMMProviderURL,
		"dmm_library_import_enabled":           fmt.Sprintf("%t", r.DMMLibraryImportEnabled),
		"dmm_library_fill_missing_enabled":     fmt.Sprintf("%t", r.DMMLibraryFillMissingEnabled),
		"rd_webdav_library_enabled":            fmt.Sprintf("%t", r.RDWebDAVLibraryEnabled),
		"rd_webdav_mount_enabled":              fmt.Sprintf("%t", r.RDWebDAVMountEnabled),
		"rd_webdav_url":                        r.RDWebDAVURL,
		"rd_webdav_username":                   r.RDWebDAVUsername,
		"rd_webdav_password":                   r.RDWebDAVPassword,
		"rd_webdav_mount_path":                 r.RDWebDAVMountPath,
		"rd_webdav_library_path":               r.RDWebDAVLibraryPath,
		"rd_webdav_scan_interval_minutes":      fmt.Sprintf("%d", r.RDWebDAVScanIntervalMinutes),
		"rd_webdav_clean_stale_symlinks":       fmt.Sprintf("%t", r.RDWebDAVCleanStaleSymlinks),
		"rd_webdav_prefer_webdav_library_only": fmt.Sprintf("%t", r.RDWebDAVPreferWebDAVLibraryOnly),
		"rd_webdav_partial_scan_fallback":      fmt.Sprintf("%t", r.RDWebDAVPartialScanFallback),
		"include_popular_movies":               fmt.Sprintf("%t", r.IncludePopularMovies),
		"include_top_rated_movies":             fmt.Sprintf("%t", r.IncludeTopRatedMovies),
		"include_now_playing":                  fmt.Sprintf("%t", r.IncludeNowPlaying),
		"include_upcoming":                     fmt.Sprintf("%t", r.IncludeUpcoming),
		"include_latest_releases_movies":       fmt.Sprintf("%t", r.IncludeLatestReleasesMovies),
		"include_collections":                  fmt.Sprintf("%t", r.IncludeCollections),
		"include_popular_series":               fmt.Sprintf("%t", r.IncludePopularSeries),
		"include_top_rated_series":             fmt.Sprintf("%t", r.IncludeTopRatedSeries),
		"include_airing_today":                 fmt.Sprintf("%t", r.IncludeAiringToday),
		"include_on_the_air":                   fmt.Sprintf("%t", r.IncludeOnTheAir),
		"include_latest_releases_series":       fmt.Sprintf("%t", r.IncludeLatestReleasesSeries),
		"user_set_host":                        r.UserSetHost,
		"expiration_hours":                     fmt.Sprintf("%d", r.ExpirationHours),
		"auto_cache_interval_hours":            fmt.Sprintf("%d", r.AutoCacheIntervalHours),
		"timeout":                              fmt.Sprintf("%d", r.Timeout),
		"use_github_for_cache":                 fmt.Sprintf("%t", r.UseGithubForCache),
		"debug":                                fmt.Sprintf("%t", r.Debug),
		"xtream_username":                      r.XtreamUsername,
		"xtream_password":                      r.XtreamPassword,
	}
}
