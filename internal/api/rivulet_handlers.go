package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Zerr0-C00L/StreamArr/internal/providers"
	streammeta "github.com/Zerr0-C00L/StreamArr/internal/services/streams"
	"github.com/gorilla/mux"
)

type rivuletSourcesRequest struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Year          int    `json:"year,omitempty"`
	TMDBID        int    `json:"tmdb_id,omitempty"`
	IMDBID        string `json:"imdb_id,omitempty"`
	Season        int    `json:"season,omitempty"`
	Episode       int    `json:"episode,omitempty"`
	ParentTitle   string `json:"parent_title,omitempty"`
	PlexRatingKey string `json:"plex_rating_key,omitempty"`
}

type rivuletSource struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Title        string  `json:"title,omitempty"`
	Quality      string  `json:"quality,omitempty"`
	Cached       bool    `json:"cached"`
	HDR          bool    `json:"hdr"`
	DynamicRange string  `json:"dynamic_range,omitempty"`
	Codec        string  `json:"codec,omitempty"`
	Audio        string  `json:"audio,omitempty"`
	Source       string  `json:"source,omitempty"`
	Seeders      int     `json:"seeders,omitempty"`
	SizeGB       float64 `json:"size_gb,omitempty"`
	FileName     string  `json:"file_name,omitempty"`
	PlayURL      string  `json:"play_url"`
}

type rivuletPlayToken struct {
	Hash  string `json:"hash,omitempty"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

// RivuletCapabilities exposes a tiny discovery endpoint for private clients.
func (h *Handler) RivuletCapabilities(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"name":       "StreamArr Sources",
		"source_api": true,
		"playback":   true,
		"types":      []string{"movie", "episode"},
	})
}

// RivuletSources returns StreamArr-resolved source options for private clients.
func (h *Handler) RivuletSources(w http.ResponseWriter, r *http.Request) {
	if h.streamProvider == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"matched":   false,
			"available": false,
			"sources":   []rivuletSource{},
		})
		return
	}

	var req rivuletSourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Type = normalizeRivuletType(req.Type)
	if req.Type == "" {
		respondError(w, http.StatusBadRequest, "type must be movie or episode")
		return
	}

	imdbID, releaseYear, err := h.resolveRivuletIMDBID(r.Context(), req)
	if err != nil {
		log.Printf("[Rivulet] Could not resolve %s %q tmdb=%d: %v", req.Type, req.Title, req.TMDBID, err)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"matched":   false,
			"available": false,
			"sources":   []rivuletSource{},
		})
		return
	}

	var providerStreams []providers.TorrentioStream
	if req.Type == "episode" {
		providerStreams, err = h.streamProvider.GetSeriesStreams(imdbID, req.Season, req.Episode)
	} else {
		providerStreams, err = h.streamProvider.GetMovieStreamsWithYear(imdbID, releaseYear)
		if err == nil && req.Title != "" {
			providerStreams = validateMovieStreams(providerStreams, req.Title, releaseYear)
		}
	}
	if err != nil {
		log.Printf("[Rivulet] Stream lookup failed for %s %s: %v", req.Type, imdbID, err)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"matched":   true,
			"available": false,
			"sources":   []rivuletSource{},
		})
		return
	}

	sources := h.buildRivuletSources(providerStreams)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"matched":   true,
		"available": len(sources) > 0,
		"sources":   sources,
	})
}

// RivuletPlay resolves a private source token and redirects to a playable URL.
func (h *Handler) RivuletPlay(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tokenValue := strings.TrimSpace(vars["token"])
	if tokenValue == "" {
		respondError(w, http.StatusBadRequest, "missing source token")
		return
	}

	token, err := decodeRivuletPlayToken(tokenValue)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid source token")
		return
	}

	if token.URL != "" && token.Hash == "" {
		http.Redirect(w, r, token.URL, http.StatusFound)
		return
	}

	if h.rdClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Real-Debrid is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	streamURL, err := h.rdClient.GetStreamURL(ctx, token.Hash)
	if err != nil {
		log.Printf("[Rivulet] Failed to resolve source %q: %v", token.Title, err)
		respondError(w, http.StatusBadGateway, "failed to resolve source")
		return
	}

	http.Redirect(w, r, streamURL, http.StatusFound)
}

func (h *Handler) resolveRivuletIMDBID(ctx context.Context, req rivuletSourcesRequest) (string, int, error) {
	if strings.HasPrefix(req.IMDBID, "tt") {
		return req.IMDBID, req.Year, nil
	}

	if req.TMDBID <= 0 {
		return "", 0, fmt.Errorf("missing tmdb_id or imdb_id")
	}

	if req.Type == "episode" {
		if req.Season <= 0 || req.Episode <= 0 {
			return "", 0, fmt.Errorf("missing season/episode")
		}
		if h.seriesStore != nil {
			if series, err := h.seriesStore.GetByTMDBID(ctx, req.TMDBID); err == nil && series != nil {
				if strings.HasPrefix(series.IMDBID, "tt") {
					return series.IMDBID, series.Year, nil
				}
				if imdbID, ok := series.Metadata["imdb_id"].(string); ok && strings.HasPrefix(imdbID, "tt") {
					return imdbID, series.Year, nil
				}
			}
		}
		if h.tmdbClient != nil {
			externalIDs, err := h.tmdbClient.GetSeriesExternalIDs(ctx, req.TMDBID)
			if err == nil && strings.HasPrefix(externalIDs.IMDBID, "tt") {
				return externalIDs.IMDBID, req.Year, nil
			}
		}
		return "", 0, fmt.Errorf("series imdb id not found")
	}

	if h.movieStore != nil {
		if movie, err := h.movieStore.GetByTMDBID(ctx, req.TMDBID); err == nil && movie != nil {
			if imdbID, ok := movie.Metadata["imdb_id"].(string); ok && strings.HasPrefix(imdbID, "tt") {
				year := movie.Year
				if movie.ReleaseDate != nil && !movie.ReleaseDate.IsZero() {
					year = movie.ReleaseDate.Year()
				}
				return imdbID, year, nil
			}
		}
	}
	if h.tmdbClient != nil {
		movie, err := h.tmdbClient.GetMovie(ctx, req.TMDBID)
		if err == nil && movie != nil {
			if imdbID, ok := movie.Metadata["imdb_id"].(string); ok && strings.HasPrefix(imdbID, "tt") {
				year := movie.Year
				if movie.ReleaseDate != nil && !movie.ReleaseDate.IsZero() {
					year = movie.ReleaseDate.Year()
				}
				return imdbID, year, nil
			}
		}
	}

	return "", 0, fmt.Errorf("movie imdb id not found")
}

func (h *Handler) buildRivuletSources(providerStreams []providers.TorrentioStream) []rivuletSource {
	sources := make([]rivuletSource, 0, len(providerStreams))
	for _, stream := range providerStreams {
		hash := stream.InfoHash
		if !isValidHash(hash) && stream.URL != "" {
			hash = extractHashFromURL(stream.URL)
		}

		token := rivuletPlayToken{
			Hash:  hash,
			Title: stream.Title,
		}
		if !isValidHash(hash) && stream.URL != "" {
			token.Hash = ""
			token.URL = stream.URL
		}
		if token.Hash == "" && token.URL == "" {
			continue
		}

		id, err := encodeRivuletPlayToken(token)
		if err != nil {
			continue
		}

		parsed := streammeta.ParseQualityFromTorrentName(firstNonEmpty(stream.Title, stream.Name))
		quality := firstNonEmpty(parsed.Resolution, stream.Quality)
		dynamicRange := parsed.HDRType
		label := rivuletSourceLabel(quality, dynamicRange, parsed.Codec, parsed.AudioFormat, stream.Source)
		sizeGB := float64(stream.Size) / (1024 * 1024 * 1024)
		if sizeGB < 0 {
			sizeGB = 0
		}

		sources = append(sources, rivuletSource{
			ID:           id,
			Label:        label,
			Title:        stream.Name,
			Quality:      quality,
			Cached:       stream.Cached,
			HDR:          dynamicRange != "" && !strings.EqualFold(dynamicRange, "SDR"),
			DynamicRange: dynamicRange,
			Codec:        parsed.Codec,
			Audio:        parsed.AudioFormat,
			Source:       stream.Source,
			Seeders:      stream.Seeders,
			SizeGB:       sizeGB,
			FileName:     stream.Title,
			PlayURL:      "/api/v1/rivulet/play/" + id,
		})
	}

	return sources
}

func normalizeRivuletType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie":
		return "movie"
	case "episode", "series", "show", "tv":
		return "episode"
	default:
		return ""
	}
}

func encodeRivuletPlayToken(token rivuletPlayToken) (string, error) {
	data, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeRivuletPlayToken(value string) (rivuletPlayToken, error) {
	var token rivuletPlayToken
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return token, err
	}
	if err := json.Unmarshal(data, &token); err != nil {
		return token, err
	}
	if token.Hash == "" && token.URL == "" {
		return token, fmt.Errorf("empty token")
	}
	return token, nil
}

func rivuletSourceLabel(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	seen := make(map[string]bool)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, part)
	}
	if len(cleaned) == 0 {
		return "Server Version"
	}
	return strings.Join(cleaned, " - ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
