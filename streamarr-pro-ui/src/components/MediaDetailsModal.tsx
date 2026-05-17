import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { streamarrApi, tmdbImageUrl } from '../services/api';
import { ArrowLeft, Star, Calendar, Plus, Check, Loader2, Film, Tv, Play, ChevronDown } from 'lucide-react';
import type { Episode, SearchResult, Series } from '../types';
import type { TrendingItem } from '../services/api';

interface MediaDetailsModalProps {
  item: SearchResult | TrendingItem;
  mediaType: string;
  onClose: () => void;
  onAdd: (item: SearchResult | TrendingItem, type: string) => void;
  isAdding: boolean;
  isAdded: boolean;
}

export default function MediaDetailsModal({
  item,
  mediaType,
  onClose,
  onAdd,
  isAdding,
  isAdded
}: MediaDetailsModalProps) {
  const isMovie = mediaType === 'movie';
  const tmdbId = ('tmdb_id' in item && item.tmdb_id) ? item.tmdb_id : item.id;
  const [selectedSeason, setSelectedSeason] = useState<number | null>(null);

  // Fetch full details from backend (which calls TMDB)
  const { data: details, isLoading } = useQuery({
    queryKey: ['tmdb-details', mediaType, tmdbId],
    queryFn: async () => {
      const response = await streamarrApi.getTMDBDetails(isMovie ? 'movie' : 'tv', tmdbId);
      return response.data;
    },
    enabled: !!tmdbId,
  });

  const { data: librarySeries = [] } = useQuery<Series[]>({
    queryKey: ['series', 'all'],
    queryFn: async () => {
      const response = await streamarrApi.getSeries({ limit: 10000 });
      return Array.isArray(response.data) ? response.data : [];
    },
    enabled: !isMovie && isAdded,
  });

  const librarySeriesItem = useMemo(() => {
    if (isMovie || !isAdded) return null;
    return librarySeries.find(series => series.tmdb_id === tmdbId) || null;
  }, [isAdded, isMovie, librarySeries, tmdbId]);

  const { data: episodes = [], isLoading: episodesLoading } = useQuery<Episode[]>({
    queryKey: ['episodes', librarySeriesItem?.id],
    queryFn: async () => {
      if (!librarySeriesItem) return [];
      const response = await streamarrApi.getEpisodes(librarySeriesItem.id);
      return Array.isArray(response.data) ? response.data : [];
    },
    enabled: !!librarySeriesItem,
  });

  const episodesBySeason = useMemo(() => {
    const grouped: Record<number, Episode[]> = {};
    episodes.forEach((episode) => {
      if (!grouped[episode.season_number]) {
        grouped[episode.season_number] = [];
      }
      grouped[episode.season_number].push(episode);
    });
    Object.values(grouped).forEach((seasonEpisodes) =>
      seasonEpisodes.sort((a, b) => a.episode_number - b.episode_number)
    );
    return grouped;
  }, [episodes]);

  const episodeSeasons = useMemo(
    () => Object.keys(episodesBySeason).map(Number).sort((a, b) => a - b),
    [episodesBySeason]
  );

  useEffect(() => {
    if (episodeSeasons.length === 0) {
      setSelectedSeason(null);
      return;
    }
    if (selectedSeason == null || !episodeSeasons.includes(selectedSeason)) {
      setSelectedSeason(episodeSeasons[0]);
    }
  }, [episodeSeasons, selectedSeason]);

  // Close on escape
  useEffect(() => {
    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleEsc);
    return () => window.removeEventListener('keydown', handleEsc);
  }, [onClose]);

  const title = item.title || item.name || 'Unknown';
  const overview = item.overview || details?.overview || 'No overview available.';
  const backdropPath = ('backdrop_path' in item ? item.backdrop_path : null) || details?.backdrop_path;
  const posterPath = item.poster_path || details?.poster_path;
  const year = item.release_date?.substring(0, 4) || item.first_air_date?.substring(0, 4) || details?.release_date?.substring(0, 4) || details?.first_air_date?.substring(0, 4);
  const rating = item.vote_average || details?.vote_average;
  const genres = details?.genres
    ?.map((genre: any) => typeof genre === 'string' ? genre : genre?.name)
    .filter(Boolean)
    .join(', ') || '';
  const runtime = details?.runtime;
  const seasons = details?.number_of_seasons;

  // Get the best trailer from videos
  const trailer = useMemo(() => {
    if (!details?.videos?.results) return null;
    const videos = details.videos.results;
    return videos.find((v: any) => v.site === 'YouTube' && v.type === 'Trailer' && v.official) ||
           videos.find((v: any) => v.site === 'YouTube' && v.type === 'Trailer') ||
           videos.find((v: any) => v.site === 'YouTube') ||
           null;
  }, [details]);

  return (
    <div className="fixed inset-0 z-50 bg-black/95 overflow-y-auto" onClick={onClose}>
      <div className="min-h-screen" onClick={(e) => e.stopPropagation()}>
        {/* Hero Section */}
        <div className="relative min-h-[70vh] w-full">
          {/* Background Image */}
          <div className="absolute inset-0">
            {backdropPath ? (
              <img
                src={tmdbImageUrl(backdropPath, 'original')}
                alt={title}
                className="w-full h-full object-cover"
              />
            ) : posterPath ? (
              <img
                src={tmdbImageUrl(posterPath, 'original')}
                alt={title}
                className="w-full h-full object-cover object-top"
              />
            ) : (
              <div className="w-full h-full bg-gradient-to-br from-slate-800 to-slate-950" />
            )}
            {/* Gradient Overlays */}
            <div className="absolute inset-0 bg-gradient-to-r from-[#141414] via-[#141414]/60 to-transparent" />
            <div className="absolute inset-0 bg-gradient-to-t from-[#141414] via-transparent to-[#141414]/30" />
            <div className="absolute bottom-0 left-0 right-0 h-64 bg-gradient-to-t from-[#141414] to-transparent" />
          </div>

          {/* Close Button */}
          <button
            onClick={onClose}
            className="absolute top-6 left-6 z-30 flex items-center gap-2 px-4 py-2 rounded-full bg-black/50 hover:bg-black/70 backdrop-blur-sm transition-all group"
          >
            <ArrowLeft className="w-5 h-5 text-white group-hover:scale-110 transition-transform" />
            <span className="text-white font-medium">Back</span>
          </button>

          {/* Content Info */}
          <div className="absolute bottom-16 left-0 right-0 px-8 md:px-16 lg:px-20">
            <div className="max-w-3xl">
              {/* Type Badge */}
              <div className="flex items-center gap-3 mb-4">
                <span className={`px-3 py-1 rounded-md text-sm font-bold uppercase tracking-wide ${
                  isMovie ? 'bg-purple-600' : 'bg-green-600'
                } text-white`}>
                  {isMovie ? 'Movie' : 'Series'}
                </span>
              </div>

              {/* Title */}
              <h1 className="text-4xl md:text-6xl lg:text-7xl font-black text-white mb-6 drop-shadow-2xl leading-tight">
                {title}
              </h1>

              {/* Meta Info */}
              <div className="flex flex-wrap items-center gap-4 mb-6">
                {rating && rating > 0 && (
                  <div className="flex items-center gap-1.5">
                    <Star className="w-5 h-5 text-yellow-400 fill-yellow-400" />
                    <span className="text-white font-bold text-lg">{rating.toFixed(1)}</span>
                  </div>
                )}
                {year && (
                  <div className="flex items-center gap-1.5">
                    <Calendar className="w-5 h-5 text-slate-400" />
                    <span className="text-slate-300 text-lg">{year}</span>
                  </div>
                )}
                {runtime && isMovie && (
                  <span className="text-slate-300 text-lg">{runtime} min</span>
                )}
                {seasons && !isMovie && (
                  <div className="flex items-center gap-1.5">
                    <Tv className="w-5 h-5 text-slate-400" />
                    <span className="text-slate-300 text-lg">{seasons} Season{seasons !== 1 ? 's' : ''}</span>
                  </div>
                )}
              </div>

              {/* Overview */}
              <p className="text-slate-200 text-lg md:text-xl leading-relaxed mb-8 line-clamp-4">
                {overview}
              </p>

              {/* Action Button */}
              <div className="flex items-center gap-4 flex-wrap">
                {trailer && (
                  <a
                    href={`https://www.youtube.com/watch?v=${trailer.key}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2 px-6 py-3 bg-white hover:bg-white/90 text-black font-semibold rounded-lg transition-all hover:scale-105"
                  >
                    <Play className="w-5 h-5 fill-current" />
                    Watch Trailer
                  </a>
                )}
                {isAdded ? (
                  <button className="flex items-center gap-2 px-6 py-3 bg-green-600 text-white font-semibold rounded-lg cursor-default">
                    <Check className="w-5 h-5" />
                    Added to Library
                  </button>
                ) : (
                  <button
                    onClick={() => onAdd(item, mediaType)}
                    disabled={isAdding}
                    className={`flex items-center gap-2 px-6 py-3 ${trailer ? 'bg-slate-700 hover:bg-slate-600' : 'bg-white hover:bg-white/90 text-black'} ${trailer ? 'text-white' : ''} font-semibold rounded-lg disabled:bg-slate-600 disabled:text-slate-400 transition-all hover:scale-105`}
                  >
                    {isAdding ? (
                      <>
                        <Loader2 className="w-5 h-5 animate-spin" />
                        Adding...
                      </>
                    ) : (
                      <>
                        <Plus className="w-5 h-5" />
                        Add to Library
                      </>
                    )}
                  </button>
                )}
              </div>
            </div>
          </div>
        </div>

        {/* Additional Details Section */}
        <div className="relative z-10 px-8 md:px-16 lg:px-20 pb-20 bg-[#141414]">
          {isLoading ? (
            <div className="flex items-center justify-center py-16">
              <Loader2 className="w-10 h-10 animate-spin text-red-600" />
            </div>
          ) : (
            <div className="max-w-5xl space-y-8">
              {/* Genres */}
              {genres && (
                <div>
                  <h3 className="text-lg font-semibold text-white mb-3">Genres</h3>
                  <p className="text-slate-300">{genres}</p>
                </div>
              )}

              {!isMovie && isAdded && (
                <div>
                  <div className="flex items-center justify-between mb-4 flex-wrap gap-4">
                    <h3 className="text-lg font-semibold text-white">Episodes</h3>
                    {episodeSeasons.length > 0 && selectedSeason != null && (
                      <div className="relative">
                        <select
                          value={selectedSeason}
                          onChange={(event) => setSelectedSeason(Number(event.target.value))}
                          className="appearance-none bg-[#242424] text-white px-4 py-2 pr-10 rounded border border-gray-600 hover:border-gray-400 transition-colors cursor-pointer font-medium"
                        >
                          {episodeSeasons.map((season) => (
                            <option key={season} value={season}>Season {season}</option>
                          ))}
                        </select>
                        <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400 pointer-events-none" />
                      </div>
                    )}
                  </div>

                  {episodesLoading ? (
                    <div className="flex items-center justify-center py-10">
                      <Loader2 className="w-8 h-8 animate-spin text-red-600" />
                    </div>
                  ) : episodes.length === 0 ? (
                    <div className="text-slate-400 py-6">No episodes found</div>
                  ) : (
                    <div className="space-y-2">
                      {(episodesBySeason[selectedSeason || episodeSeasons[0]] || []).map((episode) => (
                        <div key={episode.id} className="rounded-lg bg-white/5 border border-white/10 p-4">
                          <div className="flex items-center gap-3 text-white">
                            <span className="text-slate-400 font-mono text-sm">
                              S{String(episode.season_number).padStart(2, '0')}E{String(episode.episode_number).padStart(2, '0')}
                            </span>
                            <span className="font-semibold">{episode.title || `Episode ${episode.episode_number}`}</span>
                            {episode.air_date && (
                              <span className="text-slate-500 text-sm">{new Date(episode.air_date).getFullYear()}</span>
                            )}
                          </div>
                          {episode.overview && (
                            <p className="text-slate-400 text-sm mt-2 line-clamp-2">{episode.overview}</p>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}

              {/* Cast */}
              {details?.credits?.cast && details.credits.cast.length > 0 && (
                <div>
                  <h3 className="text-lg font-semibold text-white mb-4">Cast</h3>
                  <div className="flex gap-4 overflow-x-auto pb-4 scrollbar-hide">
                    {details.credits.cast.slice(0, 10).map((person: any) => (
                      <div key={person.id} className="flex-shrink-0 w-32">
                        <div className="aspect-[2/3] rounded-lg overflow-hidden bg-slate-800 mb-2">
                          {person.profile_path ? (
                            <img
                              src={tmdbImageUrl(person.profile_path, 'w200')}
                              alt={person.name}
                              className="w-full h-full object-cover"
                            />
                          ) : (
                            <div className="w-full h-full flex items-center justify-center">
                              <Film className="w-8 h-8 text-slate-600" />
                            </div>
                          )}
                        </div>
                        <p className="text-white text-sm font-medium truncate">{person.name}</p>
                        <p className="text-slate-400 text-xs truncate">{person.character}</p>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
