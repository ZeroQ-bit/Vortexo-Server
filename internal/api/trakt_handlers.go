package api

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/auth"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/database"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
)

type traktDeviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

type traktSyncWatchedRequest struct {
	ImportMovies bool `json:"import_movies"`
	ImportShows  bool `json:"import_shows"`
}

type traktSyncWatchedResponse struct {
	ImportedMovies   int       `json:"imported_movies"`
	ImportedShows    int       `json:"imported_shows"`
	ImportedEpisodes int       `json:"imported_episodes"`
	Skipped          int       `json:"skipped"`
	SyncedAt         time.Time `json:"synced_at"`
}

func (h *Handler) TraktStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.traktStore == nil {
		respondError(w, http.StatusServiceUnavailable, "Trakt store not initialized")
		return
	}

	client := h.traktClient()
	authRecord, err := h.traktStore.GetAuth(r.Context(), userID)
	connected := err == nil && authRecord != nil
	response := map[string]interface{}{
		"configured":       client.Configured(),
		"has_client_id":    client.HasClientID(),
		"connected":        connected,
		"needs_client_key": !client.Configured(),
	}
	if connected {
		response["expires_at"] = authRecord.ExpiresAt
		response["last_sync_at"] = authRecord.LastSyncAt
		if authRecord.ExpiresAt != nil {
			response["needs_refresh"] = time.Now().After(authRecord.ExpiresAt.Add(-5 * time.Minute))
		}
	}
	respondJSON(w, http.StatusOK, response)
}

func (h *Handler) TraktDeviceCode(w http.ResponseWriter, r *http.Request) {
	if _, ok := authenticatedUserID(r); !ok {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	client := h.traktClient()
	if !client.HasClientID() {
		respondError(w, http.StatusBadRequest, "Trakt client ID is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	code, err := client.StartDeviceAuth(ctx)
	if err != nil {
		log.Printf("[Trakt] device code failed: %v", err)
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, code)
}

func (h *Handler) TraktDeviceToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.traktStore == nil {
		respondError(w, http.StatusServiceUnavailable, "Trakt store not initialized")
		return
	}

	var req traktDeviceTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.DeviceCode) == "" {
		respondError(w, http.StatusBadRequest, "missing device_code")
		return
	}

	client := h.traktClient()
	if !client.Configured() {
		respondError(w, http.StatusBadRequest, "Trakt client ID and secret are not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	token, err := client.ExchangeDeviceCode(ctx, req.DeviceCode)
	if err != nil {
		var apiErr *services.TraktAPIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode == "authorization_pending" || apiErr.ErrorCode == "slow_down") {
			respondJSON(w, http.StatusAccepted, map[string]interface{}{
				"pending": true,
				"error":   apiErr.ErrorCode,
				"message": apiErr.Description,
			})
			return
		}
		log.Printf("[Trakt] device token failed: %v", err)
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}

	now := time.Now()
	if err := h.traktStore.SaveAuth(ctx, database.TraktAuth{
		UserID:       userID,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    services.TraktTokenExpiresAt(token, now),
		Scope:        token.Scope,
	}); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save Trakt token")
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"connected":  true,
		"expires_at": services.TraktTokenExpiresAt(token, now),
		"scope":      token.Scope,
	})
}

func (h *Handler) TraktSyncWatched(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.traktStore == nil || h.userStore == nil {
		respondError(w, http.StatusServiceUnavailable, "Trakt sync store not initialized")
		return
	}

	req := traktSyncWatchedRequest{ImportMovies: true, ImportShows: true}
	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.ImportMovies && !req.ImportShows {
			req.ImportMovies = true
			req.ImportShows = true
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	client := h.traktClient()
	authRecord, err := h.traktAuthForRequest(ctx, userID, client)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	var response traktSyncWatchedResponse
	if req.ImportMovies {
		movies, err := client.GetWatchedMovies(ctx, authRecord.AccessToken)
		if err != nil {
			respondError(w, http.StatusBadGateway, err.Error())
			return
		}
		imported, skipped := h.importTraktWatchedMovies(ctx, userID, movies)
		response.ImportedMovies = imported
		response.Skipped += skipped
	}
	if req.ImportShows {
		shows, err := client.GetWatchedShows(ctx, authRecord.AccessToken)
		if err != nil {
			respondError(w, http.StatusBadGateway, err.Error())
			return
		}
		importedShows, importedEpisodes, skipped := h.importTraktWatchedShows(ctx, userID, shows)
		response.ImportedShows = importedShows
		response.ImportedEpisodes = importedEpisodes
		response.Skipped += skipped
	}

	response.SyncedAt = time.Now().UTC()
	if err := h.traktStore.UpdateLastSync(ctx, userID, response.SyncedAt); err != nil {
		log.Printf("[Trakt] failed to update last sync: %v", err)
	}

	respondJSON(w, http.StatusOK, response)
}

func (h *Handler) TraktDisconnect(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if h.traktStore == nil {
		respondError(w, http.StatusServiceUnavailable, "Trakt store not initialized")
		return
	}
	if err := h.traktStore.DeleteAuth(r.Context(), userID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to disconnect Trakt")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"connected": false})
}

func (h *Handler) traktAuthForRequest(ctx context.Context, userID int, client *services.TraktClient) (*database.TraktAuth, error) {
	if !client.Configured() {
		return nil, errors.New("Trakt client ID and secret are not configured")
	}
	authRecord, err := h.traktStore.GetAuth(ctx, userID)
	if err != nil {
		return nil, errors.New("Trakt account is not connected")
	}
	if authRecord.ExpiresAt == nil || time.Now().Before(authRecord.ExpiresAt.Add(-5*time.Minute)) {
		return authRecord, nil
	}
	token, err := client.RefreshToken(ctx, authRecord.RefreshToken)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	authRecord.AccessToken = token.AccessToken
	authRecord.RefreshToken = token.RefreshToken
	authRecord.ExpiresAt = services.TraktTokenExpiresAt(token, now)
	authRecord.Scope = token.Scope
	if err := h.traktStore.SaveAuth(ctx, *authRecord); err != nil {
		return nil, err
	}
	return authRecord, nil
}

func (h *Handler) importTraktWatchedMovies(ctx context.Context, userID int, movies []services.TraktWatchedMovie) (int, int) {
	imported := 0
	skipped := 0
	for _, item := range movies {
		watchedAt := latestTraktTime(item.LastWatchedAt, item.LastUpdatedAt)
		if watchedAt.IsZero() {
			watchedAt = time.Now()
		}
		tmdbID := h.resolveTraktTMDBID("movie", item.Movie.IDs.TMDB, item.Movie.IDs.IMDB)
		if tmdbID == 0 || strings.TrimSpace(item.Movie.Title) == "" {
			skipped++
			continue
		}

		entry := database.ExternalWatchHistory{
			UserID:    userID,
			Source:    "trakt",
			MediaType: "movie",
			TMDBID:    tmdbID,
			IMDBID:    item.Movie.IDs.IMDB,
			TraktID:   item.Movie.IDs.Trakt,
			Title:     item.Movie.Title,
			Year:      item.Movie.Year,
			WatchedAt: watchedAt,
			Plays:     item.Plays,
		}
		if err := h.traktStore.UpsertExternalWatchHistory(ctx, entry); err != nil {
			log.Printf("[Trakt] movie import failed title=%q: %v", item.Movie.Title, err)
			skipped++
			continue
		}
		_ = h.userStore.UpsertImportedWatchHistory(userID, externalHistoryStreamID(entry), item.Movie.Title, "movie", 100, 100, watchedAt)
		imported++
	}
	return imported, skipped
}

func (h *Handler) importTraktWatchedShows(ctx context.Context, userID int, shows []services.TraktWatchedShow) (int, int, int) {
	importedShows := 0
	importedEpisodes := 0
	skipped := 0

	for _, show := range shows {
		watchedAt := latestTraktTime(show.LastWatchedAt, show.LastUpdatedAt)
		if watchedAt.IsZero() {
			watchedAt = latestTraktShowEpisodeTime(show)
		}
		if watchedAt.IsZero() {
			watchedAt = time.Now()
		}
		tmdbID := h.resolveTraktTMDBID("tv", show.Show.IDs.TMDB, show.Show.IDs.IMDB)
		if tmdbID == 0 || strings.TrimSpace(show.Show.Title) == "" {
			skipped++
			continue
		}

		showEntry := database.ExternalWatchHistory{
			UserID:    userID,
			Source:    "trakt",
			MediaType: "tv",
			TMDBID:    tmdbID,
			IMDBID:    show.Show.IDs.IMDB,
			TraktID:   show.Show.IDs.Trakt,
			Title:     show.Show.Title,
			Year:      show.Show.Year,
			WatchedAt: watchedAt,
			Plays:     show.Plays,
		}
		if err := h.traktStore.UpsertExternalWatchHistory(ctx, showEntry); err != nil {
			log.Printf("[Trakt] show import failed title=%q: %v", show.Show.Title, err)
			skipped++
			continue
		}
		_ = h.userStore.UpsertImportedWatchHistory(userID, externalHistoryStreamID(showEntry), show.Show.Title, "tv", 100, 100, watchedAt)
		importedShows++

		for _, season := range show.Seasons {
			for _, episode := range season.Episodes {
				episodeWatchedAt := latestTraktTime(episode.LastWatchedAt, nil)
				if episodeWatchedAt.IsZero() {
					episodeWatchedAt = watchedAt
				}
				entry := database.ExternalWatchHistory{
					UserID:        userID,
					Source:        "trakt",
					MediaType:     "episode",
					TMDBID:        tmdbID,
					IMDBID:        show.Show.IDs.IMDB,
					TraktID:       show.Show.IDs.Trakt,
					SeasonNumber:  season.Number,
					EpisodeNumber: episode.Number,
					Title:         show.Show.Title,
					Year:          show.Show.Year,
					WatchedAt:     episodeWatchedAt,
					Plays:         episode.Plays,
				}
				if err := h.traktStore.UpsertExternalWatchHistory(ctx, entry); err != nil {
					log.Printf("[Trakt] episode import failed title=%q s=%d e=%d: %v", show.Show.Title, season.Number, episode.Number, err)
					skipped++
					continue
				}
				importedEpisodes++
			}
		}
	}

	return importedShows, importedEpisodes, skipped
}

func (h *Handler) resolveTraktTMDBID(mediaType string, tmdbID int, imdbID string) int {
	if tmdbID > 0 {
		return tmdbID
	}
	if h.tmdbClient == nil || !strings.HasPrefix(strings.TrimSpace(imdbID), "tt") {
		return 0
	}
	resolved, err := h.tmdbClient.IMDBToTMDB(strings.TrimSpace(imdbID), mediaType)
	if err != nil {
		return 0
	}
	return resolved
}

func (h *Handler) traktClient() *services.TraktClient {
	clientID := strings.TrimSpace(os.Getenv("TRAKT_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("TRAKT_CLIENT_SECRET"))
	if h.settingsManager != nil {
		cfg := h.settingsManager.Get()
		if cfg != nil {
			if strings.TrimSpace(cfg.TraktClientID) != "" {
				clientID = strings.TrimSpace(cfg.TraktClientID)
			}
			if strings.TrimSpace(cfg.TraktClientSecret) != "" {
				clientSecret = strings.TrimSpace(cfg.TraktClientSecret)
			}
		}
	}
	return services.NewTraktClient(clientID, clientSecret)
}

func authenticatedUserID(r *http.Request) (int, bool) {
	claims, ok := auth.GetUserFromContext(r.Context())
	if !ok || claims == nil || claims.UserID <= 0 {
		return 0, false
	}
	return claims.UserID, true
}

func latestTraktTime(primary *time.Time, fallback *time.Time) time.Time {
	if primary != nil && !primary.IsZero() {
		return primary.UTC()
	}
	if fallback != nil && !fallback.IsZero() {
		return fallback.UTC()
	}
	return time.Time{}
}

func latestTraktShowEpisodeTime(show services.TraktWatchedShow) time.Time {
	latest := time.Time{}
	for _, season := range show.Seasons {
		for _, episode := range season.Episodes {
			if episode.LastWatchedAt != nil && episode.LastWatchedAt.After(latest) {
				latest = episode.LastWatchedAt.UTC()
			}
		}
	}
	return latest
}

func externalHistoryStreamID(entry database.ExternalWatchHistory) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(entry.Source))
	_, _ = hash.Write([]byte("|"))
	_, _ = hash.Write([]byte(entry.MediaType))
	_, _ = hash.Write([]byte("|"))
	_, _ = hash.Write([]byte(strconv.Itoa(entry.TMDBID)))
	_, _ = hash.Write([]byte("|"))
	_, _ = hash.Write([]byte(strconv.Itoa(entry.SeasonNumber)))
	_, _ = hash.Write([]byte("|"))
	_, _ = hash.Write([]byte(strconv.Itoa(entry.EpisodeNumber)))
	return int(hash.Sum32() & 0x7fffffff)
}
