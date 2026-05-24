package api

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/auth"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/database"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
)

type vortexoHomeFeed struct {
	GeneratedAt  time.Time        `json:"generated_at"`
	RefreshAfter time.Time        `json:"refresh_after"`
	Rows         []vortexoHomeRow `json:"rows"`
}

type vortexoHomeRow struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Reason       string            `json:"reason,omitempty"`
	RefreshAfter time.Time         `json:"refresh_after"`
	Items        []vortexoHomeItem `json:"items"`
}

type vortexoHomeItem struct {
	ID               string   `json:"id"`
	RatingKey        string   `json:"rating_key,omitempty"`
	Key              string   `json:"key,omitempty"`
	GUID             string   `json:"guid,omitempty"`
	MediaType        string   `json:"media_type"`
	TMDBID           int      `json:"tmdb_id,omitempty"`
	IMDBID           string   `json:"imdb_id,omitempty"`
	ShowTMDBID       int      `json:"show_tmdb_id,omitempty"`
	ShowTitle        string   `json:"show_title,omitempty"`
	SeasonNumber     int      `json:"season_number,omitempty"`
	EpisodeNumber    int      `json:"episode_number,omitempty"`
	Title            string   `json:"title"`
	OriginalTitle    string   `json:"original_title,omitempty"`
	Overview         string   `json:"overview,omitempty"`
	PosterPath       string   `json:"poster_path,omitempty"`
	BackdropPath     string   `json:"backdrop_path,omitempty"`
	LandscapePath    string   `json:"landscape_path,omitempty"`
	LogoPath         string   `json:"logo_path,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
	Year             int      `json:"year,omitempty"`
	Runtime          int      `json:"runtime,omitempty"`
	Genres           []string `json:"genres,omitempty"`
	VoteAverage      float64  `json:"vote_average,omitempty"`
	VoteCount        int      `json:"vote_count,omitempty"`
	ReleaseDate      string   `json:"release_date,omitempty"`
	FirstAirDate     string   `json:"first_air_date,omitempty"`
	AddedAt          int64    `json:"added_at,omitempty"`
	UpdatedAt        int64    `json:"updated_at,omitempty"`
	LastViewedAt     int64    `json:"last_viewed_at,omitempty"`
	NumberOfSeasons  int      `json:"number_of_seasons,omitempty"`
	NumberOfEpisodes int      `json:"number_of_episodes,omitempty"`
}

type vortexoHomeCandidate struct {
	item       vortexoHomeItem
	identity   string
	source     string
	isLibrary  bool
	addedAt    time.Time
	updatedAt  time.Time
	scoreBoost float64
}

type vortexoHomeContext struct {
	now                 time.Time
	userID              int
	genreWeights        map[string]int
	watched             map[string]bool
	watchedTitles       map[string]bool
	recentWatchName     string
	upNextCandidates    []vortexoHomeCandidate
	watchlistCandidates []vortexoHomeCandidate
}

type vortexoHomeRecipe struct {
	id     string
	title  string
	reason string
	period string
	filter func(vortexoHomeCandidate, vortexoHomeContext) bool
	score  func(vortexoHomeCandidate, vortexoHomeContext) float64
}

var vortexoHomeCandidateCache = struct {
	sync.Mutex
	expiresAt  time.Time
	candidates []vortexoHomeCandidate
}{}

const vortexoHomeCandidateCacheTTL = 5 * time.Minute

// VortexoHome returns server-curated, mixed movie/series Home rows for private
// clients. The tvOS app renders these rows and keeps playback resolution in the
// existing Vortexo source bridge.
func (h *Handler) VortexoHome(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	rowLimit := boundedHomeInt(r.URL.Query().Get("row_limit"), 8, 1, 12)
	itemLimit := boundedHomeInt(r.URL.Query().Get("item_limit"), 30, 6, 50)

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	candidates := h.cachedVortexoHomeCandidates(ctx, now)
	homeCtx := h.vortexoHomeContext(ctx, r, candidates, now)
	rows := h.buildVortexoHomeRows(candidates, homeCtx, rowLimit, itemLimit)

	respondJSON(w, http.StatusOK, vortexoHomeFeed{
		GeneratedAt:  now.UTC(),
		RefreshAfter: now.Add(time.Hour).UTC(),
		Rows:         rows,
	})
}

func (h *Handler) cachedVortexoHomeCandidates(ctx context.Context, now time.Time) []vortexoHomeCandidate {
	vortexoHomeCandidateCache.Lock()
	if now.Before(vortexoHomeCandidateCache.expiresAt) && len(vortexoHomeCandidateCache.candidates) > 0 {
		cached := append([]vortexoHomeCandidate(nil), vortexoHomeCandidateCache.candidates...)
		vortexoHomeCandidateCache.Unlock()
		return cached
	}
	vortexoHomeCandidateCache.Unlock()

	candidates := h.vortexoHomeCandidates(ctx)

	vortexoHomeCandidateCache.Lock()
	vortexoHomeCandidateCache.candidates = append([]vortexoHomeCandidate(nil), candidates...)
	vortexoHomeCandidateCache.expiresAt = now.Add(vortexoHomeCandidateCacheTTL)
	vortexoHomeCandidateCache.Unlock()

	return candidates
}

func (h *Handler) vortexoHomeCandidates(ctx context.Context) []vortexoHomeCandidate {
	var candidates []vortexoHomeCandidate

	if h.movieStore != nil {
		movies, err := h.movieStore.List(ctx, 0, 1500, nil)
		if err == nil {
			if _, opts, ok := h.activeContentFilterSettings(); ok {
				filtered := movies[:0]
				for _, movie := range movies {
					if allowed, _ := services.MovieAllowedByContentFilters(movie, opts); allowed {
						filtered = append(filtered, movie)
					}
				}
				movies = filtered
			}
			for _, movie := range movies {
				candidates = append(candidates, vortexoHomeCandidateFromMovie(movie))
			}
		}
	}

	if h.seriesStore != nil {
		series, err := h.seriesStore.List(ctx, 0, 1500, nil)
		if err == nil {
			if _, opts, ok := h.activeContentFilterSettings(); ok {
				filtered := series[:0]
				for _, item := range series {
					if allowed, _ := services.SeriesAllowedByContentFilters(item, opts); allowed {
						filtered = append(filtered, item)
					}
				}
				series = filtered
			}
			for _, item := range series {
				candidates = append(candidates, vortexoHomeCandidateFromSeries(item))
			}
		}
	}

	if h.tmdbClient != nil {
		candidates = append(candidates, h.vortexoDiscoverHomeCandidates(ctx)...)
	}

	return deduplicateVortexoHomeCandidates(candidates)
}

func (h *Handler) vortexoDiscoverHomeCandidates(ctx context.Context) []vortexoHomeCandidate {
	type result struct {
		items  []services.TrendingItem
		source string
	}
	results := make(chan result, 4)

	fetch := func(source string, fn func(context.Context) ([]services.TrendingItem, error)) {
		items, err := fn(ctx)
		if err != nil {
			results <- result{source: source}
			return
		}
		results <- result{items: items, source: source}
	}

	go fetch("trending", func(ctx context.Context) ([]services.TrendingItem, error) {
		return h.tmdbClient.GetTrending(ctx, "all", "day")
	})
	go fetch("popular", func(ctx context.Context) ([]services.TrendingItem, error) {
		return h.tmdbClient.GetPopular(ctx, "movie")
	})
	go fetch("popular", func(ctx context.Context) ([]services.TrendingItem, error) {
		return h.tmdbClient.GetPopular(ctx, "tv")
	})
	go fetch("new", func(ctx context.Context) ([]services.TrendingItem, error) {
		return h.tmdbClient.GetNowPlaying(ctx, "movie")
	})

	var candidates []vortexoHomeCandidate
	for i := 0; i < 4; i++ {
		res := <-results
		for _, item := range res.items {
			candidates = append(candidates, vortexoHomeCandidateFromTrending(item, res.source))
		}
	}

	return candidates
}

func (h *Handler) vortexoHomeContext(
	ctx context.Context,
	r *http.Request,
	candidates []vortexoHomeCandidate,
	now time.Time,
) vortexoHomeContext {
	homeCtx := vortexoHomeContext{
		now:           now,
		genreWeights:  map[string]int{},
		watched:       map[string]bool{},
		watchedTitles: map[string]bool{},
	}

	if userID, ok := h.vortexoHomeUserID(ctx, r); ok {
		homeCtx.userID = userID
		if h.userStore != nil {
			if history, err := h.userStore.GetWatchHistory(userID, 80); err == nil {
				applyVortexoWatchHistory(&homeCtx, history, candidates)
			}
		}
		if h.traktStore != nil {
			if history, err := h.traktStore.GetExternalWatchHistory(ctx, userID, 20000); err == nil {
				applyVortexoExternalWatchHistory(&homeCtx, history, candidates)
				homeCtx.upNextCandidates = h.vortexoHomeUpNextCandidates(ctx, history, candidates, now, 40)
			}
		}
	}

	return homeCtx
}

type vortexoHomeShowProgress struct {
	tmdbID        int
	imdbID        string
	title         string
	year          int
	latestAt      time.Time
	latestSeason  int
	latestEpisode int
	watched       map[[2]int]bool
}

func (h *Handler) vortexoHomeUpNextCandidates(
	ctx context.Context,
	history []database.ExternalWatchHistory,
	candidates []vortexoHomeCandidate,
	now time.Time,
	limit int,
) []vortexoHomeCandidate {
	if h.tmdbClient == nil || len(history) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 30
	}

	progressByShow := make(map[int]*vortexoHomeShowProgress)
	for _, entry := range history {
		mediaType := strings.ToLower(strings.TrimSpace(entry.MediaType))
		if mediaType != "tv" && mediaType != "show" && mediaType != "series" && mediaType != "episode" {
			continue
		}
		if entry.TMDBID <= 0 {
			continue
		}

		progress := progressByShow[entry.TMDBID]
		if progress == nil {
			progress = &vortexoHomeShowProgress{
				tmdbID:  entry.TMDBID,
				watched: map[[2]int]bool{},
			}
			progressByShow[entry.TMDBID] = progress
		}
		if strings.TrimSpace(progress.title) == "" && strings.TrimSpace(entry.Title) != "" {
			progress.title = entry.Title
		}
		if progress.imdbID == "" {
			progress.imdbID = entry.IMDBID
		}
		if progress.year == 0 {
			progress.year = entry.Year
		}
		if entry.WatchedAt.After(progress.latestAt) {
			progress.latestAt = entry.WatchedAt
			if entry.SeasonNumber > 0 && entry.EpisodeNumber > 0 {
				progress.latestSeason = entry.SeasonNumber
				progress.latestEpisode = entry.EpisodeNumber
			}
		}
		if mediaType == "episode" && entry.SeasonNumber > 0 && entry.EpisodeNumber > 0 {
			progress.watched[[2]int{entry.SeasonNumber, entry.EpisodeNumber}] = true
		}
	}

	progressItems := make([]*vortexoHomeShowProgress, 0, len(progressByShow))
	for _, progress := range progressByShow {
		if len(progress.watched) == 0 {
			continue
		}
		progressItems = append(progressItems, progress)
	}
	sort.SliceStable(progressItems, func(i, j int) bool {
		return progressItems[i].latestAt.After(progressItems[j].latestAt)
	})

	candidateByIdentity := make(map[string]vortexoHomeCandidate, len(candidates))
	for _, candidate := range candidates {
		candidateByIdentity[candidate.identity] = candidate
	}

	maxShowsToInspect := limit * 2
	if maxShowsToInspect < 24 {
		maxShowsToInspect = 24
	}

	result := make([]vortexoHomeCandidate, 0, limit)
	seen := map[string]bool{}
	for index, progress := range progressItems {
		if len(result) >= limit {
			break
		}
		if index >= maxShowsToInspect {
			break
		}

		candidate, ok := h.vortexoHomeNextEpisodeCandidate(ctx, progress, candidateByIdentity, now)
		if !ok || candidate.identity == "" || seen[candidate.identity] {
			continue
		}
		seen[candidate.identity] = true
		result = append(result, candidate)
	}
	return result
}

func (h *Handler) vortexoHomeNextEpisodeCandidate(
	ctx context.Context,
	progress *vortexoHomeShowProgress,
	candidateByIdentity map[string]vortexoHomeCandidate,
	now time.Time,
) (vortexoHomeCandidate, bool) {
	if progress == nil || progress.tmdbID <= 0 {
		return vortexoHomeCandidate{}, false
	}

	showIdentity := "tv:" + strconv.Itoa(progress.tmdbID)
	baseCandidate := candidateByIdentity[showIdentity]

	var localSeries *models.Series
	if h.seriesStore != nil {
		if series, err := h.seriesStore.GetByTMDBID(ctx, progress.tmdbID); err == nil && series != nil {
			localSeries = series
		}
	}

	if localSeries != nil && h.episodeStore != nil {
		if episodes, err := h.episodeStore.ListBySeries(ctx, localSeries.ID); err == nil {
			if episode := firstUnwatchedHomeEpisode(progress, episodes, now); episode != nil {
				return vortexoHomeCandidateFromNextEpisode(progress, baseCandidate, localSeries, episode), true
			}
		}
	}

	tmdbSeries, err := h.tmdbClient.GetSeries(ctx, progress.tmdbID)
	if err != nil || tmdbSeries == nil {
		return vortexoHomeCandidate{}, false
	}

	seasonCount := tmdbSeries.Seasons
	startSeason := progress.latestSeason
	if startSeason <= 0 {
		startSeason = 1
	}
	if seasonCount > 0 && startSeason > seasonCount {
		startSeason = seasonCount
	}

	stopSeason := seasonCount
	if stopSeason <= 0 {
		stopSeason = startSeason + 2
	}
	if stopSeason > startSeason+3 {
		stopSeason = startSeason + 3
	}

	for seasonNumber := startSeason; seasonNumber <= stopSeason; seasonNumber++ {
		season, err := h.tmdbClient.GetSeason(ctx, progress.tmdbID, seasonNumber)
		if err != nil || season == nil {
			continue
		}
		sort.SliceStable(season.Episodes, func(i, j int) bool {
			return season.Episodes[i].EpisodeNumber < season.Episodes[j].EpisodeNumber
		})
		for _, tmdbEpisode := range season.Episodes {
			if tmdbEpisode.SeasonNumber <= 0 {
				tmdbEpisode.SeasonNumber = seasonNumber
			}
			if tmdbEpisode.EpisodeNumber <= 0 {
				continue
			}
			key := [2]int{tmdbEpisode.SeasonNumber, tmdbEpisode.EpisodeNumber}
			if progress.watched[key] || !homeEpisodeDateHasAired(tmdbEpisode.AirDate, now) {
				continue
			}
			episode := &models.Episode{
				TMDBID:        tmdbEpisode.ID,
				SeriesID:      tmdbSeries.ID,
				SeasonNumber:  tmdbEpisode.SeasonNumber,
				EpisodeNumber: tmdbEpisode.EpisodeNumber,
				Title:         firstNonEmpty(tmdbEpisode.Name, homeEpisodeFallbackTitle(tmdbEpisode.SeasonNumber, tmdbEpisode.EpisodeNumber)),
				Overview:      tmdbEpisode.Overview,
				AirDate:       homeParseDate(tmdbEpisode.AirDate),
				StillPath:     tmdbEpisode.StillPath,
				Runtime:       tmdbEpisode.Runtime,
			}
			return vortexoHomeCandidateFromNextEpisode(progress, baseCandidate, tmdbSeries, episode), true
		}
	}

	return vortexoHomeCandidate{}, false
}

func firstUnwatchedHomeEpisode(
	progress *vortexoHomeShowProgress,
	episodes []*models.Episode,
	now time.Time,
) *models.Episode {
	if progress == nil || len(episodes) == 0 {
		return nil
	}
	sortedEpisodes := append([]*models.Episode(nil), episodes...)
	sort.SliceStable(sortedEpisodes, func(i, j int) bool {
		if sortedEpisodes[i].SeasonNumber != sortedEpisodes[j].SeasonNumber {
			return sortedEpisodes[i].SeasonNumber < sortedEpisodes[j].SeasonNumber
		}
		return sortedEpisodes[i].EpisodeNumber < sortedEpisodes[j].EpisodeNumber
	})
	for _, episode := range sortedEpisodes {
		if episode == nil || episode.SeasonNumber <= 0 || episode.EpisodeNumber <= 0 {
			continue
		}
		if progress.watched[[2]int{episode.SeasonNumber, episode.EpisodeNumber}] {
			continue
		}
		if !homeEpisodeTimeHasAired(episode.AirDate, now) {
			continue
		}
		return episode
	}
	return nil
}

func vortexoHomeCandidateFromNextEpisode(
	progress *vortexoHomeShowProgress,
	baseCandidate vortexoHomeCandidate,
	series *models.Series,
	episode *models.Episode,
) vortexoHomeCandidate {
	showTitle := firstNonEmpty(series.Title, baseCandidate.item.Title, progress.title)
	episodeTitle := firstNonEmpty(episode.Title, homeEpisodeFallbackTitle(episode.SeasonNumber, episode.EpisodeNumber))
	firstAirDate := homeDateString(episode.AirDate)
	posterPath := firstNonEmpty(series.PosterPath, baseCandidate.item.PosterPath)
	backdropPath := firstNonEmpty(series.BackdropPath, baseCandidate.item.BackdropPath)
	landscapePath := firstNonEmpty(
		episode.StillPath,
		firstString(metadataStringArray(series.Metadata, "landscape_paths")),
		baseCandidate.item.LandscapePath,
		backdropPath,
	)
	logoPath := firstNonEmpty(
		firstString(metadataStringArray(series.Metadata, "logo_paths")),
		metadataString(series.Metadata, "logo_path"),
		baseCandidate.item.LogoPath,
	)
	imdbID := firstNonEmpty(series.IMDBID, progress.imdbID, baseCandidate.item.IMDBID)
	updatedAt := progress.latestAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	episodeID := strconv.FormatInt(episode.ID, 10)
	if episode.TMDBID > 0 {
		episodeID = strconv.Itoa(episode.TMDBID)
	}
	if episodeID == "0" {
		episodeID = strconv.Itoa(episode.SeasonNumber) + "-" + strconv.Itoa(episode.EpisodeNumber)
	}

	item := vortexoHomeItem{
		ID:               "episode:" + strconv.Itoa(progress.tmdbID) + ":" + strconv.Itoa(episode.SeasonNumber) + ":" + strconv.Itoa(episode.EpisodeNumber),
		RatingKey:        "vortexo:tmdb:episode:" + strconv.Itoa(progress.tmdbID) + ":" + strconv.Itoa(episode.SeasonNumber) + ":" + strconv.Itoa(episode.EpisodeNumber) + ":" + episodeID,
		Key:              "tmdb://tv/" + strconv.Itoa(progress.tmdbID) + "/season/" + strconv.Itoa(episode.SeasonNumber) + "/episode/" + strconv.Itoa(episode.EpisodeNumber),
		GUID:             "tmdb://tv/" + strconv.Itoa(progress.tmdbID) + "/season/" + strconv.Itoa(episode.SeasonNumber) + "/episode/" + strconv.Itoa(episode.EpisodeNumber),
		MediaType:        "episode",
		TMDBID:           progress.tmdbID,
		IMDBID:           imdbID,
		ShowTMDBID:       progress.tmdbID,
		ShowTitle:        showTitle,
		SeasonNumber:     episode.SeasonNumber,
		EpisodeNumber:    episode.EpisodeNumber,
		Title:            episodeTitle,
		Overview:         firstNonEmpty(episode.Overview, series.Overview, baseCandidate.item.Overview),
		PosterPath:       posterPath,
		BackdropPath:     backdropPath,
		LandscapePath:    landscapePath,
		LogoPath:         logoPath,
		OriginalLanguage: firstNonEmpty(series.OriginalLang, baseCandidate.item.OriginalLanguage),
		Keywords:         firstNonEmptyStringSlice(homeMetadataKeywords(series.Metadata), baseCandidate.item.Keywords),
		Year:             yearFromHomeDate(firstNonEmpty(firstAirDate, homeDateString(series.FirstAirDate))),
		Runtime:          episode.Runtime,
		Genres:           firstNonEmptyStringSlice(series.Genres, baseCandidate.item.Genres),
		VoteAverage:      firstNonZeroFloat(series.VoteAverage, baseCandidate.item.VoteAverage),
		VoteCount:        firstNonZeroInt(series.VoteCount, baseCandidate.item.VoteCount),
		FirstAirDate:     firstAirDate,
		AddedAt:          updatedAt.Unix(),
		UpdatedAt:        updatedAt.Unix(),
		LastViewedAt:     updatedAt.Unix(),
		NumberOfSeasons:  series.Seasons,
		NumberOfEpisodes: series.TotalEpisodes,
	}
	if item.Year == 0 {
		item.Year = series.Year
	}
	if item.Runtime == 0 {
		item.Runtime = baseCandidate.item.Runtime
	}

	candidate := vortexoHomeCandidate{
		item:       item,
		identity:   "tv:" + strconv.Itoa(progress.tmdbID),
		source:     "trakt-up-next",
		isLibrary:  baseCandidate.isLibrary,
		addedAt:    updatedAt,
		updatedAt:  updatedAt,
		scoreBoost: 12,
	}
	return candidate
}

func (h *Handler) vortexoHomeUserID(ctx context.Context, r *http.Request) (int, bool) {
	if userID, ok := optionalVortexoUserID(r); ok {
		return userID, true
	}
	if h.traktStore == nil {
		return 0, false
	}
	return h.traktStore.FallbackHomeUserID(ctx)
}

func applyVortexoWatchHistory(
	homeCtx *vortexoHomeContext,
	history []database.WatchHistory,
	candidates []vortexoHomeCandidate,
) {
	byTitle := make(map[string]vortexoHomeCandidate, len(candidates))
	for _, candidate := range candidates {
		byTitle[normalizeHomeText(candidate.item.Title)] = candidate
	}

	for _, entry := range history {
		titleKey := normalizeHomeText(entry.Title)
		if titleKey == "" {
			continue
		}
		homeCtx.watchedTitles[titleKey] = true
		if homeCtx.recentWatchName == "" {
			homeCtx.recentWatchName = entry.Title
		}
		if candidate, ok := byTitle[titleKey]; ok {
			homeCtx.watched[candidate.identity] = true
			for _, genre := range candidate.item.Genres {
				homeCtx.genreWeights[normalizeHomeText(genre)] += 3
			}
		}
	}
}

func applyVortexoExternalWatchHistory(
	homeCtx *vortexoHomeContext,
	history []database.ExternalWatchHistory,
	candidates []vortexoHomeCandidate,
) {
	byIdentity := make(map[string]vortexoHomeCandidate, len(candidates))
	byTitle := make(map[string]vortexoHomeCandidate, len(candidates))
	for _, candidate := range candidates {
		byIdentity[candidate.identity] = candidate
		byTitle[normalizeHomeText(candidate.item.Title)] = candidate
	}

	for _, entry := range history {
		titleKey := normalizeHomeText(entry.Title)
		if titleKey != "" {
			homeCtx.watchedTitles[titleKey] = true
			if homeCtx.recentWatchName == "" {
				homeCtx.recentWatchName = entry.Title
			}
		}

		mediaType := strings.ToLower(strings.TrimSpace(entry.MediaType))
		if mediaType == "show" || mediaType == "series" || mediaType == "episode" {
			mediaType = "tv"
		}
		identity := ""
		if entry.TMDBID > 0 && (mediaType == "movie" || mediaType == "tv") {
			identity = mediaType + ":" + strconv.Itoa(entry.TMDBID)
		}

		var candidate vortexoHomeCandidate
		var ok bool
		if identity != "" {
			candidate, ok = byIdentity[identity]
		}
		if !ok && titleKey != "" {
			candidate, ok = byTitle[titleKey]
		}
		if !ok {
			continue
		}

		if entry.MediaType != "episode" {
			homeCtx.watched[candidate.identity] = true
		}
		for _, genre := range candidate.item.Genres {
			homeCtx.genreWeights[normalizeHomeText(genre)] += 3
		}
	}
}

func vortexoHomeWatchlistCandidates(
	watchlist []database.ExternalWatchlistItem,
	candidates []vortexoHomeCandidate,
) []vortexoHomeCandidate {
	byIdentity := make(map[string]vortexoHomeCandidate, len(candidates))
	byTitle := make(map[string]vortexoHomeCandidate, len(candidates))
	for _, candidate := range candidates {
		byIdentity[candidate.identity] = candidate
		byTitle[normalizeHomeText(candidate.item.Title)] = candidate
	}

	result := make([]vortexoHomeCandidate, 0, len(watchlist))
	seen := map[string]bool{}
	for _, entry := range watchlist {
		mediaType := strings.ToLower(strings.TrimSpace(entry.MediaType))
		if mediaType == "show" || mediaType == "series" {
			mediaType = "tv"
		}
		if mediaType != "tv" {
			mediaType = "movie"
		}

		identity := ""
		if entry.TMDBID > 0 {
			identity = mediaType + ":" + strconv.Itoa(entry.TMDBID)
		}

		var candidate vortexoHomeCandidate
		var ok bool
		if identity != "" {
			candidate, ok = byIdentity[identity]
		}
		if !ok {
			candidate, ok = byTitle[normalizeHomeText(entry.Title)]
		}
		if !ok {
			candidate = vortexoHomeCandidateFromWatchlist(entry, mediaType)
		}
		if candidate.identity == "" || seen[candidate.identity] {
			continue
		}
		seen[candidate.identity] = true
		result = append(result, candidate)
	}
	return result
}

func (h *Handler) buildVortexoHomeRows(
	candidates []vortexoHomeCandidate,
	homeCtx vortexoHomeContext,
	rowLimit int,
	itemLimit int,
) []vortexoHomeRow {
	if len(candidates) == 0 {
		return []vortexoHomeRow{}
	}

	recipes := selectVortexoHomeRecipes(homeCtx)
	used := make(map[string]bool, len(candidates))
	rows := make([]vortexoHomeRow, 0, rowLimit)

	if len(homeCtx.upNextCandidates) > 0 && len(rows) < rowLimit {
		items := buildVortexoHomePinnedItems(homeCtx.upNextCandidates, used, itemLimit)
		if len(items) > 0 {
			rows = append(rows, vortexoHomeRow{
				ID:           "trakt-up-next",
				Title:        "Continue Watching",
				Reason:       "Trakt Up Next episodes sorted by recent activity",
				RefreshAfter: homeCtx.now.Add(time.Hour).UTC(),
				Items:        items,
			})
		}
	}

	for _, recipe := range recipes {
		if len(rows) >= rowLimit {
			break
		}

		items := buildVortexoHomeRowItems(candidates, homeCtx, recipe, used, itemLimit)
		if len(items) == 0 {
			continue
		}

		rows = append(rows, vortexoHomeRow{
			ID:           recipe.id,
			Title:        recipe.title,
			Reason:       recipe.reason,
			RefreshAfter: homeCtx.now.Add(recipeRefreshDuration(recipe.period)).UTC(),
			Items:        items,
		})
	}

	if len(rows) == 0 {
		items := buildVortexoHomeRowItems(candidates, homeCtx, fallbackVortexoHomeRecipe(), used, itemLimit)
		if len(items) > 0 {
			rows = append(rows, vortexoHomeRow{
				ID:           "more-to-explore",
				Title:        "More to Explore",
				RefreshAfter: homeCtx.now.Add(time.Hour).UTC(),
				Items:        items,
			})
		}
	}

	return rows
}

func buildVortexoHomePinnedItems(
	candidates []vortexoHomeCandidate,
	used map[string]bool,
	limit int,
) []vortexoHomeItem {
	items := make([]vortexoHomeItem, 0, limit)
	for _, candidate := range candidates {
		if len(items) >= limit {
			break
		}
		if candidate.identity == "" || used[candidate.identity] {
			continue
		}
		used[candidate.identity] = true
		items = append(items, candidate.item)
	}
	return items
}

func buildVortexoHomeRowItems(
	candidates []vortexoHomeCandidate,
	homeCtx vortexoHomeContext,
	recipe vortexoHomeRecipe,
	used map[string]bool,
	limit int,
) []vortexoHomeItem {
	type ranked struct {
		candidate vortexoHomeCandidate
		score     float64
		shuffle   uint64
	}

	seed := vortexoHomeSeed(homeCtx, recipe)
	rankedItems := make([]ranked, 0, len(candidates))
	for _, candidate := range candidates {
		if used[candidate.identity] {
			continue
		}
		if recipe.filter != nil && !recipe.filter(candidate, homeCtx) {
			continue
		}
		score := genericVortexoHomeScore(candidate, homeCtx)
		if recipe.score != nil {
			score += recipe.score(candidate, homeCtx)
		}
		shuffle := stableHomeHash(seed + "|" + candidate.identity)
		score += stableHomeJitter(shuffle, 4.0)
		rankedItems = append(rankedItems, ranked{
			candidate: candidate,
			score:     score,
			shuffle:   shuffle,
		})
	}

	sort.SliceStable(rankedItems, func(i, j int) bool {
		if rankedItems[i].score != rankedItems[j].score {
			return rankedItems[i].score > rankedItems[j].score
		}
		return rankedItems[i].shuffle < rankedItems[j].shuffle
	})

	items := make([]vortexoHomeItem, 0, limit)
	for _, ranked := range rankedItems {
		if len(items) >= limit {
			break
		}
		used[ranked.candidate.identity] = true
		items = append(items, ranked.candidate.item)
	}
	return items
}

func selectVortexoHomeRecipes(homeCtx vortexoHomeContext) []vortexoHomeRecipe {
	var recipes []vortexoHomeRecipe

	if len(homeCtx.genreWeights) > 0 {
		title := "Because You Watched"
		if strings.TrimSpace(homeCtx.recentWatchName) != "" {
			title = "Because You Watched " + strings.TrimSpace(homeCtx.recentWatchName)
		}
		recipes = append(recipes, vortexoHomeRecipe{
			id:     "because-you-watched",
			title:  title,
			reason: "Based on your recent viewing",
			period: "hourly",
			filter: func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return !ctx.watched[c.identity] && candidateGenreAffinity(c, ctx) > 0
			},
			score: func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				return candidateGenreAffinity(c, ctx) * 12
			},
		})
	}

	fixed := []vortexoHomeRecipe{
		{
			id:     "top-picks",
			title:  "Top Picks For You",
			reason: "Mixed movies and series selected by server taste signals",
			period: "hourly",
			filter: func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return !ctx.watched[c.identity]
			},
			score: func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				return candidateGenreAffinity(c, ctx)*10 + c.scoreBoost
			},
		},
		{
			id:     "trending-now",
			title:  "Trending Now",
			reason: "Refreshed from TMDB and your local library",
			period: "hourly",
			filter: func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return c.source == "trending" || c.source == "popular"
			},
			score: func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				if c.source == "trending" {
					return 30
				}
				return 15
			},
		},
		{
			id:     "new-and-popular",
			title:  "New & Popular",
			reason: "Recent releases with strong interest",
			period: "daily",
			filter: func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return itemYear(c.item) >= ctx.now.Year()-2 || c.source == "new"
			},
			score: func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				return recencyScore(c, ctx.now) + 8
			},
		},
		{
			id:     "recently-added",
			title:  "Recently Added",
			reason: "Newest items from your Vortexo Server library",
			period: "daily",
			filter: func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return c.isLibrary && !c.addedAt.IsZero()
			},
			score: func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				return timeScore(c.addedAt, ctx.now) * 20
			},
		},
	}
	recipes = append(recipes, fixed...)

	rotating := applicableVortexoHomeRecipes(homeCtx.now)
	seed := strconv.Itoa(homeCtx.userID) + "|rows|" + homeCtx.now.Format("2006-01-02-15")
	sort.SliceStable(rotating, func(i, j int) bool {
		return stableHomeHash(seed+"|"+rotating[i].id) < stableHomeHash(seed+"|"+rotating[j].id)
	})
	recipes = append(recipes, rotating...)
	return recipes
}

func applicableVortexoHomeRecipes(now time.Time) []vortexoHomeRecipe {
	month := now.Month()
	hour := now.Hour()
	weekday := now.Weekday()
	isWeekend := weekday == time.Friday || weekday == time.Saturday || weekday == time.Sunday

	recipes := []vortexoHomeRecipe{
		keywordVortexoHomeRecipe("hidden-gems", "Hidden Gems", "Strong matches that are easy to miss", "daily", nil, []string{"indie", "cult", "underrated"}),
		keywordVortexoHomeRecipe("critically-acclaimed", "Critically Acclaimed", "High-rated movies and series", "daily", nil, nil),
		keywordVortexoHomeRecipe("crime-mystery", "Crime & Mystery", "Cases, conspiracies, and investigations", "daily", []string{"crime", "mystery", "thriller"}, []string{"detective", "murder", "investigation", "conspiracy"}),
		keywordVortexoHomeRecipe("sci-fi-worlds", "Sci-Fi Worlds", "Future tech, space, and strange worlds", "daily", []string{"science fiction", "sci-fi", "fantasy"}, []string{"space", "future", "alien", "robot", "android", "dystopian"}),
		keywordVortexoHomeRecipe("dark-drama", "Dark Drama", "Heavier stories with tension", "daily", []string{"drama", "thriller"}, []string{"grief", "trauma", "revenge", "secret", "corruption"}),
		keywordVortexoHomeRecipe("binge-worthy-series", "Binge-Worthy Series", "Shows with enough story to settle into", "daily", nil, []string{"season", "episode", "saga", "ensemble"}),
		keywordVortexoHomeRecipe("movie-night-picks", "Movie Night Picks", "Feature-length picks with broad appeal", "daily", nil, []string{"cinematic", "blockbuster", "award", "festival"}),
		keywordVortexoHomeRecipe("action-rush", "Action Rush", "Fast, loud, and high-stakes", "daily", []string{"action", "adventure", "war"}, []string{"mission", "heist", "assassin", "battle", "survival"}),
		keywordVortexoHomeRecipe("comedies-to-clear-your-head", "Comedies to Clear Your Head", "Lighter picks with rewatch energy", "daily", []string{"comedy"}, []string{"friendship", "family", "awkward", "satire"}),
		keywordVortexoHomeRecipe("standouts-you-missed", "Standouts You Missed", "Good older picks that can get buried by new releases", "daily", nil, []string{"classic", "favorite", "award-winning"}),
		keywordVortexoHomeRecipe("popular-unwatched", "Popular But Unwatched", "Familiar titles you have not started here", "daily", nil, nil),
		keywordVortexoHomeRecipe("documentaries-worth-watching", "Documentaries Worth Watching", "Real stories and true events", "daily", []string{"documentary"}, []string{"true story", "history", "music", "sports"}),
		keywordVortexoHomeRecipe("animation-for-adults", "Animation For Adults", "Animated picks outside kids-only mode", "daily", []string{"animation"}, []string{"adult animation", "satire", "anime"}),
		keywordVortexoHomeRecipe("international-picks", "International Picks", "Great picks beyond the usual feed", "daily", nil, []string{"foreign", "international"}),
		keywordVortexoHomeRecipe("from-the-90s", "From The 90s", "Older favorites and missed classics", "daily", nil, nil),
	}

	if isWeekend {
		recipes = append(recipes, keywordVortexoHomeRecipe("weekend-binge", "Weekend Binge", "Longer stories for an open night", "hourly", []string{"drama", "crime", "science fiction", "fantasy"}, []string{"season", "epic", "saga"}))
	}
	if hour >= 21 || hour < 4 {
		recipes = append(recipes, keywordVortexoHomeRecipe("late-night-suspense", "Late Night Suspense", "Darker picks for late viewing", "hourly", []string{"horror", "thriller", "mystery"}, []string{"haunted", "killer", "nightmare", "paranormal"}))
	}
	switch month {
	case time.October:
		recipes = append(recipes, keywordVortexoHomeRecipe("spooky-season", "Spooky Season", "Horror, mystery, and supernatural picks", "daily", []string{"horror", "thriller", "mystery"}, []string{"ghost", "haunted", "witch", "vampire", "monster"}))
	case time.December:
		recipes = append(recipes, keywordVortexoHomeRecipe("holiday-watchlist", "Holiday Watchlist", "Seasonal comfort and big-family chaos", "daily", []string{"comedy", "family", "romance"}, []string{"christmas", "holiday", "winter", "family"}))
	case time.June, time.July, time.August:
		recipes = append(recipes, keywordVortexoHomeRecipe("winter-nights", "Winter Nights", "Bigger stories for cold evenings", "daily", []string{"drama", "fantasy", "thriller"}, []string{"snow", "mountain", "survival"}))
	}

	for i := range recipes {
		recipe := recipes[i]
		switch recipe.id {
		case "hidden-gems":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return !ctx.watched[c.identity] && c.item.VoteAverage >= 5.7 && c.item.VoteAverage < 7.8
			}
			recipes[i].score = func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				lowVoteBoost := 10.0
				if c.item.VoteCount > 1500 {
					lowVoteBoost = 0
				}
				return lowVoteBoost + candidateGenreAffinity(c, ctx)*5
			}
		case "critically-acclaimed":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return c.item.VoteAverage >= 7.2
			}
			recipes[i].score = func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				return c.item.VoteAverage * 6
			}
		case "binge-worthy-series":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return c.item.MediaType == "tv" && (c.item.NumberOfEpisodes >= 8 || c.item.NumberOfSeasons >= 2)
			}
			recipes[i].score = func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				return minFloat(float64(c.item.NumberOfEpisodes)/3, 18) + c.item.VoteAverage + candidateGenreAffinity(c, ctx)*4
			}
		case "movie-night-picks":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return c.item.MediaType == "movie" && c.item.VoteAverage >= 5.5
			}
			recipes[i].score = func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				runtimeScore := 0.0
				if c.item.Runtime >= 80 {
					runtimeScore = 6
				}
				return runtimeScore + c.item.VoteAverage*3 + minFloat(float64(c.item.VoteCount)/500, 8)
			}
		case "standouts-you-missed":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				year := itemYear(c.item)
				return !ctx.watched[c.identity] && year > 0 && year <= ctx.now.Year()-2 && c.item.VoteAverage >= 6.0
			}
			recipes[i].score = func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				lowVoteBoost := 0.0
				if c.item.VoteCount > 0 && c.item.VoteCount < 1200 {
					lowVoteBoost = 8
				}
				return lowVoteBoost + c.item.VoteAverage*4 + candidateGenreAffinity(c, ctx)*4
			}
		case "popular-unwatched":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				return !ctx.watched[c.identity] && !ctx.watchedTitles[normalizeHomeText(c.item.Title)]
			}
			recipes[i].score = func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
				return float64(c.item.VoteCount)/300 + c.item.VoteAverage
			}
		case "from-the-90s":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				year := itemYear(c.item)
				return year >= 1990 && year <= 1999
			}
		case "international-picks":
			recipes[i].filter = func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
				lang := strings.ToLower(strings.TrimSpace(c.item.OriginalLanguage))
				return lang != "en" && lang != ""
			}
		}
	}

	return recipes
}

func keywordVortexoHomeRecipe(id, title, reason, period string, genres []string, keywords []string) vortexoHomeRecipe {
	normalizedGenres := make([]string, 0, len(genres))
	for _, genre := range genres {
		normalizedGenres = append(normalizedGenres, normalizeHomeText(genre))
	}
	normalizedKeywords := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		normalizedKeywords = append(normalizedKeywords, normalizeHomeText(keyword))
	}
	return vortexoHomeRecipe{
		id:     id,
		title:  title,
		reason: reason,
		period: period,
		filter: func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
			return candidateMatchesHomeKeywords(c, normalizedGenres, normalizedKeywords)
		},
		score: func(c vortexoHomeCandidate, ctx vortexoHomeContext) float64 {
			return candidateGenreAffinity(c, ctx)*4 + keywordMatchScore(c, normalizedGenres, normalizedKeywords)
		},
	}
}

func fallbackVortexoHomeRecipe() vortexoHomeRecipe {
	return vortexoHomeRecipe{
		id:     "more-to-explore",
		title:  "More to Explore",
		period: "hourly",
		filter: func(c vortexoHomeCandidate, ctx vortexoHomeContext) bool {
			return true
		},
	}
}

func vortexoHomeCandidateFromMovie(movie *models.Movie) vortexoHomeCandidate {
	releaseDate := homeDateString(movie.ReleaseDate)
	logoPath := firstString(metadataStringArray(movie.Metadata, "logo_paths"))
	if logoPath == "" {
		logoPath = metadataString(movie.Metadata, "logo_path")
	}
	landscapePath := firstString(metadataStringArray(movie.Metadata, "landscape_paths"))
	item := vortexoHomeItem{
		ID:               "movie:" + strconv.Itoa(movie.TMDBID),
		MediaType:        "movie",
		TMDBID:           movie.TMDBID,
		IMDBID:           metadataString(movie.Metadata, "imdb_id"),
		Title:            movie.Title,
		OriginalTitle:    movie.OriginalTitle,
		Overview:         movie.Overview,
		PosterPath:       movie.PosterPath,
		BackdropPath:     movie.BackdropPath,
		LandscapePath:    landscapePath,
		LogoPath:         logoPath,
		OriginalLanguage: movie.OriginalLang,
		Keywords:         homeMetadataKeywords(movie.Metadata),
		Year:             movie.Year,
		Runtime:          movie.Runtime,
		Genres:           movie.Genres,
		VoteAverage:      movie.VoteAverage,
		VoteCount:        movie.VoteCount,
		ReleaseDate:      releaseDate,
		AddedAt:          unixIfSet(movie.AddedAt),
		UpdatedAt:        unixIfSet(movie.UpdatedAt),
	}
	if item.Year == 0 {
		item.Year = yearFromHomeDate(releaseDate)
	}
	candidate := vortexoHomeCandidate{
		item:      item,
		source:    "library",
		isLibrary: true,
		addedAt:   movie.AddedAt,
		updatedAt: movie.UpdatedAt,
	}
	candidate.identity = vortexoHomeIdentity(item)
	return candidate
}

func vortexoHomeCandidateFromSeries(series *models.Series) vortexoHomeCandidate {
	firstAirDate := homeDateString(series.FirstAirDate)
	logoPath := firstString(metadataStringArray(series.Metadata, "logo_paths"))
	if logoPath == "" {
		logoPath = metadataString(series.Metadata, "logo_path")
	}
	landscapePath := firstString(metadataStringArray(series.Metadata, "landscape_paths"))
	item := vortexoHomeItem{
		ID:               "tv:" + strconv.Itoa(series.TMDBID),
		MediaType:        "tv",
		TMDBID:           series.TMDBID,
		IMDBID:           series.IMDBID,
		Title:            series.Title,
		OriginalTitle:    series.OriginalTitle,
		Overview:         series.Overview,
		PosterPath:       series.PosterPath,
		BackdropPath:     series.BackdropPath,
		LandscapePath:    landscapePath,
		LogoPath:         logoPath,
		OriginalLanguage: series.OriginalLang,
		Keywords:         homeMetadataKeywords(series.Metadata),
		Year:             series.Year,
		Genres:           series.Genres,
		VoteAverage:      series.VoteAverage,
		VoteCount:        series.VoteCount,
		FirstAirDate:     firstAirDate,
		AddedAt:          unixIfSet(series.AddedAt),
		UpdatedAt:        unixIfSet(series.UpdatedAt),
		NumberOfSeasons:  series.Seasons,
		NumberOfEpisodes: series.TotalEpisodes,
	}
	if item.Year == 0 {
		item.Year = yearFromHomeDate(firstAirDate)
	}
	candidate := vortexoHomeCandidate{
		item:      item,
		source:    "library",
		isLibrary: true,
		addedAt:   series.AddedAt,
		updatedAt: series.UpdatedAt,
	}
	candidate.identity = vortexoHomeIdentity(item)
	return candidate
}

func vortexoHomeCandidateFromTrending(item services.TrendingItem, source string) vortexoHomeCandidate {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if mediaType == "show" || mediaType == "series" {
		mediaType = "tv"
	}
	if mediaType != "tv" {
		mediaType = "movie"
	}
	homeItem := vortexoHomeItem{
		ID:           mediaType + ":" + strconv.Itoa(item.ID),
		MediaType:    mediaType,
		TMDBID:       item.ID,
		Title:        item.Title,
		Overview:     item.Overview,
		PosterPath:   item.PosterPath,
		BackdropPath: item.BackdropPath,
		VoteAverage:  item.VoteAverage,
	}
	if mediaType == "tv" {
		homeItem.FirstAirDate = item.ReleaseDate
	} else {
		homeItem.ReleaseDate = item.ReleaseDate
	}
	homeItem.Year = yearFromHomeDate(item.ReleaseDate)
	candidate := vortexoHomeCandidate{
		item:       homeItem,
		source:     source,
		scoreBoost: 5,
	}
	candidate.identity = vortexoHomeIdentity(homeItem)
	return candidate
}

func vortexoHomeCandidateFromWatchlist(entry database.ExternalWatchlistItem, mediaType string) vortexoHomeCandidate {
	metadata := entry.Metadata
	if metadata == nil {
		metadata = models.Metadata{}
	}
	logoPath := firstString(metadataStringArray(metadata, "logo_paths"))
	if logoPath == "" {
		logoPath = metadataString(metadata, "logo_path")
	}
	landscapePath := firstString(metadataStringArray(metadata, "landscape_paths"))
	releaseDate := metadataString(metadata, "release_date")
	firstAirDate := metadataString(metadata, "first_air_date")

	item := vortexoHomeItem{
		ID:               mediaType + ":" + strconv.Itoa(entry.TMDBID),
		MediaType:        mediaType,
		TMDBID:           entry.TMDBID,
		IMDBID:           firstNonEmpty(metadataString(metadata, "imdb_id"), entry.IMDBID),
		Title:            firstNonEmpty(metadataString(metadata, "title"), entry.Title),
		OriginalTitle:    metadataString(metadata, "original_title"),
		Overview:         metadataString(metadata, "overview"),
		PosterPath:       metadataString(metadata, "poster_path"),
		BackdropPath:     metadataString(metadata, "backdrop_path"),
		LandscapePath:    landscapePath,
		LogoPath:         logoPath,
		OriginalLanguage: metadataString(metadata, "original_language"),
		Keywords:         homeMetadataKeywords(metadata),
		Year:             entry.Year,
		Runtime:          homeMetadataInt(metadata, "runtime"),
		Genres:           metadataStringArray(metadata, "genres"),
		VoteAverage:      homeMetadataFloat(metadata, "vote_average"),
		VoteCount:        homeMetadataInt(metadata, "vote_count"),
		ReleaseDate:      releaseDate,
		FirstAirDate:     firstAirDate,
		AddedAt:          entry.ListedAt.Unix(),
		UpdatedAt:        entry.ListedAt.Unix(),
		NumberOfSeasons:  homeMetadataInt(metadata, "number_of_seasons"),
		NumberOfEpisodes: homeMetadataInt(metadata, "total_episodes"),
	}
	if item.Year == 0 {
		item.Year = yearFromHomeDate(firstNonEmpty(item.ReleaseDate, item.FirstAirDate))
	}
	candidate := vortexoHomeCandidate{
		item:       item,
		source:     "trakt-watchlist",
		isLibrary:  false,
		addedAt:    entry.ListedAt,
		updatedAt:  entry.ListedAt,
		scoreBoost: 8,
	}
	candidate.identity = vortexoHomeIdentity(item)
	return candidate
}

func deduplicateVortexoHomeCandidates(candidates []vortexoHomeCandidate) []vortexoHomeCandidate {
	byID := make(map[string]vortexoHomeCandidate, len(candidates))
	order := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.identity == "" {
			continue
		}
		existing, exists := byID[candidate.identity]
		if !exists {
			byID[candidate.identity] = candidate
			order = append(order, candidate.identity)
			continue
		}
		if shouldReplaceHomeCandidate(existing, candidate) {
			byID[candidate.identity] = candidate
		}
	}

	result := make([]vortexoHomeCandidate, 0, len(order))
	for _, id := range order {
		result = append(result, byID[id])
	}
	return result
}

func shouldReplaceHomeCandidate(existing, candidate vortexoHomeCandidate) bool {
	if candidate.isLibrary != existing.isLibrary {
		return candidate.isLibrary
	}
	return homeArtworkScore(candidate.item) > homeArtworkScore(existing.item)
}

func homeArtworkScore(item vortexoHomeItem) int {
	score := 0
	if item.BackdropPath != "" {
		score += 2
	}
	if item.LandscapePath != "" {
		score += 2
	}
	if item.LogoPath != "" {
		score += 1
	}
	if item.PosterPath != "" {
		score += 1
	}
	return score
}

func optionalVortexoUserID(r *http.Request) (int, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return 0, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if token == "" || token == header {
		return 0, false
	}
	claims, err := auth.ValidateToken(token)
	if err != nil {
		return 0, false
	}
	return claims.UserID, true
}

func boundedHomeInt(raw string, fallback int, minValue int, maxValue int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value == 0 {
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func genericVortexoHomeScore(candidate vortexoHomeCandidate, homeCtx vortexoHomeContext) float64 {
	score := candidate.item.VoteAverage * 4
	if candidate.item.VoteCount > 0 {
		score += minFloat(float64(candidate.item.VoteCount)/400, 12)
	}
	score += recencyScore(candidate, homeCtx.now)
	if candidate.isLibrary {
		score += 3
	}
	return score
}

func candidateGenreAffinity(candidate vortexoHomeCandidate, homeCtx vortexoHomeContext) float64 {
	score := 0
	for _, genre := range candidate.item.Genres {
		score += homeCtx.genreWeights[normalizeHomeText(genre)]
	}
	return float64(score)
}

func recencyScore(candidate vortexoHomeCandidate, now time.Time) float64 {
	year := itemYear(candidate.item)
	if year <= 0 {
		return 0
	}
	age := now.Year() - year
	switch {
	case age <= 0:
		return 12
	case age == 1:
		return 9
	case age == 2:
		return 6
	case age <= 5:
		return 3
	default:
		return 0
	}
}

func timeScore(value time.Time, now time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	days := now.Sub(value).Hours() / 24
	if days < 0 {
		return 12
	}
	switch {
	case days <= 3:
		return 12
	case days <= 14:
		return 9
	case days <= 45:
		return 5
	case days <= 120:
		return 2
	default:
		return 0
	}
}

func candidateMatchesHomeKeywords(candidate vortexoHomeCandidate, genres []string, keywords []string) bool {
	if len(genres) == 0 && len(keywords) == 0 {
		return true
	}
	return keywordMatchScore(candidate, genres, keywords) > 0
}

func keywordMatchScore(candidate vortexoHomeCandidate, genres []string, keywords []string) float64 {
	score := 0.0
	candidateGenres := make(map[string]bool, len(candidate.item.Genres))
	for _, genre := range candidate.item.Genres {
		candidateGenres[normalizeHomeText(genre)] = true
	}
	for _, genre := range genres {
		if candidateGenres[genre] {
			score += 6
		}
	}

	text := normalizeHomeText(candidate.item.Title + " " + candidate.item.OriginalTitle + " " + candidate.item.Overview + " " + strings.Join(candidate.item.Keywords, " "))
	for _, keyword := range keywords {
		if keyword != "" && strings.Contains(text, keyword) {
			score += 3
		}
	}
	return score
}

func metadataStringArray(metadata models.Metadata, key string) []string {
	if metadata == nil {
		return nil
	}
	switch value := metadata[key].(type) {
	case []string:
		return value
	case []interface{}:
		result := make([]string, 0, len(value))
		for _, entry := range value {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	default:
		return nil
	}
}

func homeMetadataInt(metadata models.Metadata, key string) int {
	if metadata == nil {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
	case string:
		result, _ := strconv.Atoi(strings.TrimSpace(value))
		return result
	default:
		return 0
	}
}

func homeMetadataFloat(metadata models.Metadata, key string) float64 {
	if metadata == nil {
		return 0
	}
	switch value := metadata[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		result, _ := value.Float64()
		return result
	case string:
		result, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return result
	default:
		return 0
	}
}

func homeMetadataKeywords(metadata models.Metadata) []string {
	candidates := appendUniqueStrings(nil, metadataStringArray(metadata, "keywords")...)
	candidates = appendUniqueStrings(candidates, metadataStringArray(metadata, "tmdb_keywords")...)
	candidates = appendUniqueStrings(candidates, metadataStringArray(metadata, "tags")...)
	return candidates
}

func itemYear(item vortexoHomeItem) int {
	if item.Year > 0 {
		return item.Year
	}
	if year := yearFromHomeDate(item.ReleaseDate); year > 0 {
		return year
	}
	return yearFromHomeDate(item.FirstAirDate)
}

func homeDateString(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
}

func unixIfSet(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func homeEpisodeFallbackTitle(season, episode int) string {
	if season <= 0 || episode <= 0 {
		return "Episode"
	}
	return "S" + twoDigitHomeNumber(season) + "E" + twoDigitHomeNumber(episode)
}

func twoDigitHomeNumber(value int) string {
	if value >= 0 && value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

func homeEpisodeDateHasAired(value string, now time.Time) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	parsed := homeParseDate(value)
	return homeEpisodeTimeHasAired(parsed, now)
}

func homeEpisodeTimeHasAired(value *time.Time, now time.Time) bool {
	if value == nil || value.IsZero() {
		return false
	}
	return !value.After(now)
}

func homeParseDate(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", trimmed)
	if err != nil {
		return nil
	}
	return &parsed
}

func firstNonEmptyStringSlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstNonZeroFloat(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func yearFromHomeDate(value string) int {
	if len(value) < 4 {
		return 0
	}
	year, _ := strconv.Atoi(value[:4])
	return year
}

func vortexoHomeIdentity(item vortexoHomeItem) string {
	if item.TMDBID > 0 {
		return item.MediaType + ":" + strconv.Itoa(item.TMDBID)
	}
	return item.MediaType + ":title:" + normalizeHomeText(item.Title)
}

func normalizeHomeText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func vortexoHomeSeed(homeCtx vortexoHomeContext, recipe vortexoHomeRecipe) string {
	return strconv.Itoa(homeCtx.userID) + "|" + recipe.id + "|" + periodKey(homeCtx.now, recipe.period)
}

func periodKey(now time.Time, period string) string {
	switch period {
	case "weekly":
		year, week := now.ISOWeek()
		return strconv.Itoa(year) + "-w" + strconv.Itoa(week)
	case "daily":
		return now.Format("2006-01-02")
	default:
		return now.Format("2006-01-02-15")
	}
}

func recipeRefreshDuration(period string) time.Duration {
	switch period {
	case "weekly":
		return 24 * time.Hour
	case "daily":
		return 6 * time.Hour
	default:
		return time.Hour
	}
}

func stableHomeHash(value string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	return hash.Sum64()
}

func stableHomeJitter(hash uint64, maxValue float64) float64 {
	if maxValue <= 0 {
		return 0
	}
	return (float64(hash%1000) / 1000) * maxValue
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
