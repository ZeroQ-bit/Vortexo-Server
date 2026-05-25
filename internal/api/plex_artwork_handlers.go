package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
	"github.com/gorilla/mux"
)

func (h *Handler) GetPlexArtwork(w http.ResponseWriter, r *http.Request) {
	if h.plexArtworkStore == nil {
		respondError(w, http.StatusServiceUnavailable, "plex artwork cache is not initialized")
		return
	}

	vars := mux.Vars(r)
	mediaType := vars["type"]
	tmdbID, err := strconv.Atoi(vars["tmdb_id"])
	if err != nil || tmdbID <= 0 {
		respondError(w, http.StatusBadRequest, "invalid TMDB ID")
		return
	}

	if r.URL.Query().Get("refresh") == "true" {
		h.refreshSinglePlexArtwork(w, r, mediaType, tmdbID)
		return
	}

	record, err := h.plexArtworkStore.Get(r.Context(), mediaType, tmdbID)
	if err == sql.ErrNoRows {
		respondError(w, http.StatusNotFound, "plex artwork not cached")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if record.Status != "ok" || record.Artwork.IsEmpty() {
		respondError(w, http.StatusNotFound, "plex artwork unavailable")
		return
	}

	respondJSON(w, http.StatusOK, record)
}

func (h *Handler) refreshSinglePlexArtwork(w http.ResponseWriter, r *http.Request, mediaType string, tmdbID int) {
	if h.plexArtworkStore == nil || h.plexArtworkService == nil {
		respondError(w, http.StatusServiceUnavailable, "plex artwork service is not initialized")
		return
	}

	item, err := h.plexArtworkStore.FindLibrarySeedItem(r.Context(), mediaType, tmdbID)
	if err == sql.ErrNoRows {
		respondError(w, http.StatusNotFound, "item not found in Vortexo Server library")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entry, err := h.plexArtworkService.RefreshOne(r.Context(), *item, 2*time.Second)
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	if entry == nil || entry.Artwork.IsEmpty() {
		respondError(w, http.StatusNotFound, "plex artwork unavailable")
		return
	}

	record, err := h.plexArtworkStore.Get(r.Context(), mediaType, tmdbID)
	if err == nil {
		respondJSON(w, http.StatusOK, record)
		return
	}

	respondJSON(w, http.StatusOK, entry)
}

func (h *Handler) TriggerPlexArtworkRefresh(w http.ResponseWriter, r *http.Request) {
	if h.plexArtworkService == nil {
		respondError(w, http.StatusServiceUnavailable, "plex artwork service is not initialized")
		return
	}

	status := services.GlobalScheduler.GetStatus(services.ServicePlexArtworkSync)
	if status != nil && status.Running {
		respondError(w, http.StatusConflict, "plex artwork sync is already running")
		return
	}

	var req struct {
		Limit int `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Limit <= 0 || req.Limit > 2000 {
		req.Limit = 2000
	}

	go h.runPlexArtworkSync(req.Limit)

	respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"message": "Plex artwork sync triggered",
		"service": services.ServicePlexArtworkSync,
		"limit":   req.Limit,
	})
}

func (h *Handler) runPlexArtworkSync(limit int) {
	ctx := context.Background()
	interval := 24 * time.Hour
	services.GlobalScheduler.MarkRunning(services.ServicePlexArtworkSync)
	_, err := h.plexArtworkService.SyncLibrary(ctx, services.PlexArtworkSyncOptions{
		Limit:      limit,
		Delay:      2 * time.Second,
		StaleAfter: 30 * 24 * time.Hour,
		Progress: func(processed, total int, message string) {
			services.GlobalScheduler.UpdateProgress(services.ServicePlexArtworkSync, processed, total, message)
		},
	})
	services.GlobalScheduler.MarkComplete(services.ServicePlexArtworkSync, err, interval)
}
