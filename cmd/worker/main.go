package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/ZeroQ-bit/Vortexo-Server/internal/cache"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/config"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/database"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/epg"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/livetv"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/models"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/playlist"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/providers"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/services"
	"github.com/ZeroQ-bit/Vortexo-Server/internal/settings"
)

func main() {
	// Load initial config (uses DATABASE_URL from environment if set)
	cfg := config.Load()

	// Connect to database
	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	log.Println("Vortexo Server Background Workers Starting...")
	log.Println("========================================")

	// Initialize settings manager and load from database
	settingsManager := settings.NewManager(db)
	if err := settingsManager.Load(); err != nil {
		log.Printf("Warning: Could not load settings: %v, using defaults", err)
	}

	// Override config with ALL settings from database
	appSettings := settingsManager.Get()

	// API Keys
	if appSettings.TMDBAPIKey != "" {
		cfg.TMDBAPIKey = appSettings.TMDBAPIKey
		log.Println("✓ TMDB API key loaded from settings")
	}
	if appSettings.RealDebridAPIKey != "" {
		cfg.RealDebridAPIKey = appSettings.RealDebridAPIKey
		cfg.UseRealDebrid = true
		log.Println("✓ Real-Debrid API key loaded from settings")
	}
	if appSettings.PremiumizeAPIKey != "" {
		cfg.PremiumizeAPIKey = appSettings.PremiumizeAPIKey
		cfg.UsePremiumize = true
		log.Println("✓ Premiumize API key loaded from settings")
	}
	if appSettings.MDBListAPIKey != "" {
		cfg.MDBListAPIKey = appSettings.MDBListAPIKey
		log.Println("✓ MDBList API key loaded from settings")
	}

	// Provider settings
	cfg.UseRealDebrid = appSettings.UseRealDebrid
	cfg.UsePremiumize = appSettings.UsePremiumize
	if len(appSettings.StremioAddons) > 0 {
		// Convert settings.StremioAddon to config.StremioAddon
		cfg.StremioAddons = make([]config.StremioAddon, len(appSettings.StremioAddons))
		for i, addon := range appSettings.StremioAddons {
			cfg.StremioAddons[i] = config.StremioAddon{
				Name:    addon.Name,
				URL:     addon.URL,
				Enabled: addon.Enabled,
			}
		}
	}

	// Playlist settings
	if appSettings.TotalPages > 0 {
		cfg.TotalPages = appSettings.TotalPages
	}
	if appSettings.MinYear > 0 {
		cfg.MinYear = appSettings.MinYear
	}
	if appSettings.MinRuntime > 0 {
		cfg.MinRuntime = appSettings.MinRuntime
	}
	cfg.IncludeAdultVOD = appSettings.IncludeAdultVOD
	cfg.OnlyCachedStreams = appSettings.OnlyCachedStreams
	cfg.OnlyReleasedContent = appSettings.OnlyReleasedContent
	cfg.BlockBollywood = appSettings.BlockBollywood

	log.Println("✓ All settings loaded from database")

	// Initialize components
	tmdbClient := services.NewTMDBClient(cfg.TMDBAPIKey)

	// Initialize providers
	// Convert config.StremioAddon to providers.StremioAddon
	stremioAddons := make([]providers.StremioAddon, len(cfg.StremioAddons))
	for i, addon := range cfg.StremioAddons {
		stremioAddons[i] = providers.StremioAddon{
			Name:    addon.Name,
			URL:     addon.URL,
			Enabled: addon.Enabled,
		}
	}
	// Get proxies from settings if available
	proxies := settingsManager.Get().HTTPProxies
	if !settingsManager.Get().UseHTTPProxy {
		proxies = nil // Don't use proxies if disabled
	}
	multiProvider := providers.NewMultiProvider(
		cfg.RealDebridAPIKey,
		stremioAddons,
		tmdbClient,
		proxies,
	)
	currentSettings := settingsManager.Get()
	if currentSettings.DMMProviderEnabled {
		multiProvider.EnableDMMDirect(cfg.RealDebridAPIKey, currentSettings.DMMProviderURL)
	}

	// Initialize cache manager
	cacheManager := cache.NewManager(db)

	// Initialize playlist generator
	playlistGen := playlist.NewEnhancedGenerator(cfg, db, tmdbClient, multiProvider)

	// Initialize channel manager
	channelManager := livetv.NewChannelManager()

	// Initialize EPG manager
	epgManager := epg.NewEPGManager()

	// Initialize MDBList sync service
	mdbSyncService := services.NewMDBListSyncService(db, cfg.MDBListAPIKey, cfg.TMDBAPIKey)

	// Initialize stores for collection worker
	movieStore := database.NewMovieStore(db)
	seriesStore := database.NewSeriesStore(db)
	episodeStore := database.NewEpisodeStore(db)
	collectionStore := database.NewCollectionStore(db)

	// Create context for workers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start workers
	log.Println("Starting workers...")

	// Worker 1: Playlist Regeneration (every 12 hours)
	go playlistWorker(ctx, playlistGen, cfg, settingsManager, 12*time.Hour)

	// Worker 2: Cache Cleanup (every hour)
	go cacheCleanupWorker(ctx, cacheManager, 1*time.Hour)

	// Worker 3: EPG Update (every 6 hours)
	go epgUpdateWorker(ctx, epgManager, channelManager, 6*time.Hour)

	// Worker 4: Channel Refresh (every hour)
	go channelRefreshWorker(ctx, channelManager, 1*time.Hour)

	// Worker 5: MDBList Sync (every 6 hours)
	go mdblistSyncWorker(ctx, mdbSyncService, db, 6*time.Hour)

	// Worker 6: Collection Sync (every 24 hours)
	go collectionSyncWorker(ctx, collectionStore, movieStore, tmdbClient, settingsManager, 24*time.Hour)

	// Worker 7: Episode Scan (every 24 hours)
	go episodeScanWorker(ctx, seriesStore, episodeStore, tmdbClient, 24*time.Hour)

	// Worker 8: Balkan VOD Sync (every 24 hours)
	go balkanVODSyncWorker(ctx, movieStore, seriesStore, tmdbClient, settingsManager, 24*time.Hour)

	log.Println("✅ All workers started successfully")
	log.Println("========================================")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("\n🛑 Shutting down workers...")
	cancel()
	time.Sleep(2 * time.Second)
	log.Println("✅ Shutdown complete")
}

func playlistWorker(ctx context.Context, gen *playlist.EnhancedGenerator, cfg *config.Config, settingsManager *settings.Manager, interval time.Duration) {
	log.Printf("📋 Playlist Worker: Starting (interval: %v)", interval)

	// Run immediately on startup
	refreshPlaylistRuntimeConfig(cfg, settingsManager)
	if err := gen.GenerateComplete(ctx); err != nil {
		log.Printf("❌ Playlist generation error: %v", err)
	} else {
		log.Println("✅ Initial playlist generation complete")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("📋 Playlist Worker: Stopping")
			return
		case <-ticker.C:
			log.Println("📋 Playlist Worker: Starting playlist regeneration...")
			refreshPlaylistRuntimeConfig(cfg, settingsManager)
			if err := gen.GenerateComplete(ctx); err != nil {
				log.Printf("❌ Playlist generation error: %v", err)
			} else {
				log.Println("✅ Playlist regeneration complete")
			}
		}
	}
}

func refreshPlaylistRuntimeConfig(cfg *config.Config, settingsManager *settings.Manager) {
	if cfg == nil || settingsManager == nil {
		return
	}

	appSettings := settingsManager.Get()
	if appSettings.TotalPages > 0 {
		cfg.TotalPages = appSettings.TotalPages
	}
	if appSettings.MinYear > 0 {
		cfg.MinYear = appSettings.MinYear
	}
	cfg.OnlyCachedStreams = appSettings.OnlyCachedStreams
	cfg.OnlyReleasedContent = appSettings.OnlyReleasedContent
	cfg.BlockBollywood = appSettings.BlockBollywood
}

func cacheCleanupWorker(ctx context.Context, manager *cache.Manager, interval time.Duration) {
	log.Printf("🧹 Cache Cleanup Worker: Starting (interval: %v)", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("🧹 Cache Cleanup Worker: Stopping")
			return
		case <-ticker.C:
			log.Println("🧹 Cache Cleanup Worker: Running cleanup...")
			manager.Cleanup()
			log.Println("✅ Cache cleanup complete")
		}
	}
}

func epgUpdateWorker(ctx context.Context, epgManager *epg.Manager, channelManager *livetv.ChannelManager, interval time.Duration) {
	log.Printf("📺 EPG Update Worker: Starting (interval: %v)", interval)

	// Run immediately on startup
	updateEPG(epgManager, channelManager)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("📺 EPG Update Worker: Stopping")
			return
		case <-ticker.C:
			updateEPG(epgManager, channelManager)
		}
	}
}

func updateEPG(epgManager *epg.Manager, channelManager *livetv.ChannelManager) {
	log.Println("📺 EPG Update Worker: Updating EPG data...")
	channels := channelManager.GetAllChannels()
	channelList := make([]livetv.Channel, len(channels))
	for i, ch := range channels {
		channelList[i] = *ch
	}

	if err := epgManager.UpdateEPG(channelList); err != nil {
		log.Printf("❌ EPG update error: %v", err)
	} else {
		log.Println("✅ EPG update complete")
	}
}

func channelRefreshWorker(ctx context.Context, manager *livetv.ChannelManager, interval time.Duration) {
	log.Printf("📡 Channel Refresh Worker: Starting (interval: %v)", interval)

	// Run immediately on startup
	if err := manager.LoadChannels(); err != nil {
		log.Printf("❌ Initial channel load error: %v", err)
	} else {
		log.Println("✅ Initial channel load complete")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("📡 Channel Refresh Worker: Stopping")
			return
		case <-ticker.C:
			log.Println("📡 Channel Refresh Worker: Refreshing channels...")
			if err := manager.LoadChannels(); err != nil {
				log.Printf("❌ Channel refresh error: %v", err)
			} else {
				log.Println("✅ Channel refresh complete")
			}
		}
	}
}

func mdblistSyncWorker(ctx context.Context, syncService *services.MDBListSyncService, db *sql.DB, interval time.Duration) {
	log.Printf("📋 MDBList Sync Worker: Starting (interval: %v)", interval)

	// Run immediately on startup
	log.Println("📋 MDBList Sync Worker: Running initial sync...")
	if err := syncService.SyncAllLists(ctx); err != nil {
		log.Printf("❌ Initial MDBList sync error: %v", err)
	} else {
		movies, series, _ := syncService.GetSyncStats(ctx)
		log.Printf("✅ Initial MDBList sync complete - Library: %d movies, %d series", movies, series)
	}

	// Enrich any existing items missing artwork
	log.Println("📋 MDBList Sync Worker: Enriching items with TMDB artwork...")
	if err := syncService.EnrichExistingItems(ctx); err != nil {
		log.Printf("⚠️ MDBList enrichment error: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("📋 MDBList Sync Worker: Stopping")
			return
		case <-ticker.C:
			log.Println("📋 MDBList Sync Worker: Syncing MDBList lists...")
			if err := syncService.SyncAllLists(ctx); err != nil {
				log.Printf("❌ MDBList sync error: %v", err)
			} else {
				movies, series, _ := syncService.GetSyncStats(ctx)
				log.Printf("✅ MDBList sync complete - Library: %d movies, %d series", movies, series)
			}
			// Enrich any new items missing artwork
			if err := syncService.EnrichExistingItems(ctx); err != nil {
				log.Printf("⚠️ MDBList enrichment error: %v", err)
			}
		}
	}
}

func collectionSyncWorker(ctx context.Context, collectionStore *database.CollectionStore, movieStore *database.MovieStore, tmdbClient *services.TMDBClient, settingsManager *settings.Manager, interval time.Duration) {
	log.Printf("📦 Collection Sync Worker: Starting (interval: %v)", interval)

	// Run immediately on startup
	runCollectionSync(ctx, collectionStore, movieStore, tmdbClient, settingsManager)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("📦 Collection Sync Worker: Stopping")
			return
		case <-ticker.C:
			runCollectionSync(ctx, collectionStore, movieStore, tmdbClient, settingsManager)
		}
	}
}

func runCollectionSync(ctx context.Context, collectionStore *database.CollectionStore, movieStore *database.MovieStore, tmdbClient *services.TMDBClient, settingsManager *settings.Manager) {
	log.Println("📦 Collection Sync Worker: Phase 1 - Scanning movies for collections...")

	// Phase 1: Scan and link movies to collections
	movies, err := movieStore.ListUncheckedForCollection(ctx)
	if err != nil {
		log.Printf("❌ Collection Sync Phase 1 error: %v", err)
		return
	}

	totalMovies := len(movies)
	if totalMovies == 0 {
		log.Println("✅ Collection Sync Phase 1: All movies already checked")
	} else {
		log.Printf("📦 Scanning %d unchecked movies...\n", totalMovies)
		linked := 0

		for i, movie := range movies {
			if i%10 == 0 {
				log.Printf("📦 Progress: %d/%d movies scanned\n", i, totalMovies)
			}

			_, collection, err := tmdbClient.GetMovieWithCollection(ctx, movie.TMDBID)
			if err != nil {
				movieStore.MarkCollectionChecked(ctx, movie.ID)
				continue
			}

			if collection != nil {
				fullCollection, _, err := tmdbClient.GetCollection(ctx, collection.TMDBID)
				if err != nil {
					movieStore.MarkCollectionChecked(ctx, movie.ID)
					continue
				}

				if err := collectionStore.Create(ctx, fullCollection); err != nil {
					movieStore.MarkCollectionChecked(ctx, movie.ID)
					continue
				}

				if err := collectionStore.UpdateMovieCollection(ctx, movie.ID, fullCollection.ID); err != nil {
					movieStore.MarkCollectionChecked(ctx, movie.ID)
					continue
				}

				linked++
			}

			movieStore.MarkCollectionChecked(ctx, movie.ID)
		}

		log.Printf("✅ Collection Sync Phase 1 complete: %d movies linked to collections\n", linked)
	}

	// Phase 2: Sync incomplete collections if auto-add is enabled
	settings := settingsManager.Get()
	if settings.AutoAddCollections {
		log.Println("📦 Collection Sync Phase 2: Adding missing movies from incomplete collections...")

		collections, _, _ := collectionStore.GetCollectionsWithProgress(ctx, 1000, 0)
		var incompleteColls []*models.Collection
		for _, coll := range collections {
			if coll.MoviesInLibrary < coll.TotalMovies {
				incompleteColls = append(incompleteColls, coll)
			}
		}

		if len(incompleteColls) == 0 {
			log.Println("✅ Collection Sync Phase 2: All collections complete!")
		} else {
			log.Printf("📦 Found %d incomplete collections - skipping auto-add (requires stream search)\n", len(incompleteColls))
			log.Println("ℹ️  Use 'Add Collection' button in UI to manually add missing movies")
		}
	} else {
		log.Println("📦 Collection Sync Phase 2 skipped: AutoAddCollections is disabled")
	}
}
func episodeScanWorker(ctx context.Context, seriesStore *database.SeriesStore, episodeStore *database.EpisodeStore, tmdbClient *services.TMDBClient, interval time.Duration) {
	log.Printf("📺 Episode Scan Worker: Starting (interval: %v)", interval)

	// Run immediately on startup
	runEpisodeScan(ctx, seriesStore, episodeStore, tmdbClient)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("📺 Episode Scan Worker: Stopping")
			return
		case <-ticker.C:
			runEpisodeScan(ctx, seriesStore, episodeStore, tmdbClient)
		}
	}
}

func runEpisodeScan(ctx context.Context, seriesStore *database.SeriesStore, episodeStore *database.EpisodeStore, tmdbClient *services.TMDBClient) {
	log.Println("📺 Episode Scan Worker: Scanning episodes for all series...")

	allSeries, err := seriesStore.List(ctx, 0, 10000, nil)
	if err != nil {
		log.Printf("❌ Episode Scan error: %v", err)
		return
	}

	totalSeries := len(allSeries)
	if totalSeries == 0 {
		log.Println("✅ Episode Scan: No series in library")
		return
	}

	log.Printf("📺 Found %d series to scan\n", totalSeries)
	totalEpisodes := 0
	errors := 0

	for i, series := range allSeries {
		if i%5 == 0 {
			log.Printf("📺 Progress: %d/%d series scanned\n", i, totalSeries)
		}

		// Get series details from TMDB
		tmdbSeries, err := tmdbClient.GetSeries(ctx, series.TMDBID)
		if err != nil {
			errors++
			continue
		}

		numSeasons := tmdbSeries.Seasons
		if numSeasons == 0 {
			continue
		}

		// Get all episodes for this series
		episodes, err := tmdbClient.GetEpisodes(ctx, series.ID, series.TMDBID, numSeasons)
		if err != nil {
			errors++
			continue
		}

		// Set the series ID for all episodes
		for _, ep := range episodes {
			ep.SeriesID = series.ID
			ep.Monitored = series.Monitored
		}

		// Add episodes to database (batch insert)
		if len(episodes) > 0 {
			if err := episodeStore.AddBatch(ctx, episodes); err == nil {
				totalEpisodes += len(episodes)
			}
		}

		time.Sleep(200 * time.Millisecond) // Rate limit
	}

	log.Printf("✅ Episode Scan complete: %d episodes for %d series (%d errors)\n", totalEpisodes, totalSeries, errors)
}

func balkanVODSyncWorker(ctx context.Context, movieStore *database.MovieStore, seriesStore *database.SeriesStore, tmdbClient *services.TMDBClient, settingsManager *settings.Manager, interval time.Duration) {
	log.Printf("🇧🇦 Balkan VOD Sync Worker: Starting (interval: %v)", interval)

	// Run initial sync
	log.Println("🇧🇦 Balkan VOD Sync Worker: Running initial sync...")
	runBalkanVODSync(ctx, movieStore, seriesStore, tmdbClient, settingsManager)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("🇧🇦 Balkan VOD Sync Worker: Stopping")
			return
		case <-ticker.C:
			runBalkanVODSync(ctx, movieStore, seriesStore, tmdbClient, settingsManager)
		}
	}
}

func runBalkanVODSync(ctx context.Context, movieStore *database.MovieStore, seriesStore *database.SeriesStore, tmdbClient *services.TMDBClient, settingsManager *settings.Manager) {
	appSettings := settingsManager.Get()

	if !appSettings.BalkanVODEnabled {
		log.Println("🇧🇦 Balkan VOD Sync: Disabled in settings")
		return
	}

	if !appSettings.BalkanVODAutoSync {
		log.Println("🇧🇦 Balkan VOD Sync: Auto-sync disabled")
		return
	}

	log.Println("🇧🇦 Balkan VOD Sync: Starting import from GitHub repos...")
	services.GlobalScheduler.MarkRunning(services.ServiceBalkanVODSync)

	importer := services.NewBalkanVODImporter(movieStore, seriesStore, tmdbClient, appSettings)
	err := importer.ImportBalkanVOD(ctx)

	// Get configured interval from settings
	syncInterval := time.Duration(appSettings.BalkanVODSyncIntervalHours) * time.Hour
	if syncInterval < 1*time.Hour {
		syncInterval = 24 * time.Hour // Default to 24 hours if invalid
	}

	services.GlobalScheduler.MarkComplete(services.ServiceBalkanVODSync, err, syncInterval)

	if err != nil {
		log.Printf("❌ Balkan VOD Sync error: %v", err)
	} else {
		log.Println("✅ Balkan VOD Sync complete")
	}
}
