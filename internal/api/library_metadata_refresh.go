package api

import (
	"context"
	"fmt"
	"time"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
)

func (h *Handler) refreshLibraryMetadata(ctx context.Context) error {
	if h.tmdbClient == nil {
		return fmt.Errorf("TMDB client is not configured")
	}
	if h.movieStore == nil && h.seriesStore == nil {
		return fmt.Errorf("library stores are not initialized")
	}

	total := 0
	var movies []*models.Movie
	var seriesList []*models.Series
	var err error

	if h.movieStore != nil {
		movies, err = h.movieStore.List(ctx, 0, 10000, nil)
		if err != nil {
			return fmt.Errorf("list movies: %w", err)
		}
		total += len(movies)
	}

	if h.seriesStore != nil {
		seriesList, err = h.seriesStore.List(ctx, 0, 10000, nil)
		if err != nil {
			return fmt.Errorf("list series: %w", err)
		}
		total += len(seriesList)
	}

	services.GlobalScheduler.UpdateProgress(services.ServiceMetadataRefresh, 0, total, "Refreshing TMDB metadata...")

	processed := 0
	failures := 0

	for _, movie := range movies {
		processed++
		services.GlobalScheduler.UpdateProgress(services.ServiceMetadataRefresh, processed, total, fmt.Sprintf("Refreshing movie: %s", movie.Title))
		if err := h.refreshMovieMetadata(ctx, movie); err != nil {
			failures++
		}
		if err := sleepOrDone(ctx, 150*time.Millisecond); err != nil {
			return err
		}
	}

	for _, series := range seriesList {
		processed++
		services.GlobalScheduler.UpdateProgress(services.ServiceMetadataRefresh, processed, total, fmt.Sprintf("Refreshing series: %s", series.Title))
		if err := h.refreshSeriesMetadata(ctx, series); err != nil {
			failures++
		}
		if err := sleepOrDone(ctx, 150*time.Millisecond); err != nil {
			return err
		}
	}

	message := fmt.Sprintf("Metadata refresh complete: %d items refreshed", processed-failures)
	if failures > 0 {
		message = fmt.Sprintf("%s, %d failed", message, failures)
	}
	services.GlobalScheduler.UpdateProgress(services.ServiceMetadataRefresh, total, total, message)

	if failures == total && total > 0 {
		return fmt.Errorf("metadata refresh failed for all %d items", total)
	}
	return nil
}

func (h *Handler) refreshMovieMetadata(ctx context.Context, existing *models.Movie) error {
	if existing == nil || existing.TMDBID <= 0 {
		return nil
	}

	refreshed, err := h.tmdbClient.GetMovie(ctx, existing.TMDBID)
	if err != nil {
		return err
	}

	refreshed.ID = existing.ID
	refreshed.Monitored = existing.Monitored
	refreshed.Available = existing.Available
	refreshed.QualityProfile = existing.QualityProfile
	refreshed.AddedAt = existing.AddedAt
	refreshed.CollectionID = existing.CollectionID
	refreshed.CollectionChecked = existing.CollectionChecked
	refreshed.LastChecked = existing.LastChecked
	refreshed.Metadata = mergeMovieMetadata(existing.Metadata, refreshed)

	return h.movieStore.Update(ctx, refreshed)
}

func (h *Handler) refreshSeriesMetadata(ctx context.Context, existing *models.Series) error {
	if existing == nil || existing.TMDBID <= 0 {
		return nil
	}

	refreshed, err := h.tmdbClient.GetSeries(ctx, existing.TMDBID)
	if err != nil {
		return err
	}

	refreshed.ID = existing.ID
	refreshed.Monitored = existing.Monitored
	refreshed.QualityProfile = existing.QualityProfile
	refreshed.SearchStatus = existing.SearchStatus
	refreshed.AddedAt = existing.AddedAt
	if refreshed.IMDBID == "" {
		refreshed.IMDBID = existing.IMDBID
	}
	refreshed.Metadata = mergeSeriesMetadata(existing.Metadata, refreshed)

	return h.seriesStore.Update(ctx, refreshed)
}

func mergeMovieMetadata(existing models.Metadata, refreshed *models.Movie) models.Metadata {
	metadata := cloneMetadata(existing)
	if refreshed == nil {
		return metadata
	}

	metadata["title"] = refreshed.Title
	metadata["original_title"] = refreshed.OriginalTitle
	metadata["overview"] = refreshed.Overview
	metadata["poster_path"] = refreshed.PosterPath
	metadata["backdrop_path"] = refreshed.BackdropPath
	metadata["release_date"] = refreshed.ReleaseDate
	metadata["runtime"] = refreshed.Runtime
	metadata["genres"] = refreshed.Genres
	for key, value := range refreshed.Metadata {
		metadata[key] = value
	}
	if refreshed.QualityProfile != "" {
		metadata["quality_profile"] = refreshed.QualityProfile
	}
	return metadata
}

func mergeSeriesMetadata(existing models.Metadata, refreshed *models.Series) models.Metadata {
	metadata := cloneMetadata(existing)
	if refreshed == nil {
		return metadata
	}

	metadata["title"] = refreshed.Title
	metadata["original_title"] = refreshed.OriginalTitle
	metadata["overview"] = refreshed.Overview
	metadata["poster_path"] = refreshed.PosterPath
	metadata["backdrop_path"] = refreshed.BackdropPath
	metadata["first_air_date"] = refreshed.FirstAirDate
	metadata["status"] = refreshed.Status
	metadata["seasons"] = refreshed.Seasons
	metadata["total_episodes"] = refreshed.TotalEpisodes
	metadata["genres"] = refreshed.Genres
	for key, value := range refreshed.Metadata {
		metadata[key] = value
	}
	if refreshed.IMDBID != "" {
		metadata["imdb_id"] = refreshed.IMDBID
	}
	if refreshed.QualityProfile != "" {
		metadata["quality_profile"] = refreshed.QualityProfile
	}
	return metadata
}

func cloneMetadata(metadata models.Metadata) models.Metadata {
	clone := models.Metadata{}
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

func sleepOrDone(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
