# StreamArr Pro

<p align="center">
  <img src="streamarr-pro-ui/public/logo.png" alt="StreamArr Pro" width="150">
</p>

<p align="center">
  <b>🎬 Self-hosted media library, IPTV server, and Debrid stream discovery</b>
</p>

<p align="center">
  StreamArr Pro combines a Netflix-style library UI, Stremio-compatible stream discovery,<br>
  live TV / IPTV output, and Debrid-backed playback links for IPTV apps.
</p>

<p align="center">
  <a href="https://github.com/ZeroQ-bit/StreamArr-Pro/releases"><img src="https://img.shields.io/github/v/release/ZeroQ-bit/StreamArr-Pro?style=for-the-badge&logo=github&color=blue" alt="Release"></a>
  <a href="#-quick-start"><img src="https://img.shields.io/badge/Docker-Ready-2496ED?style=for-the-badge&logo=docker" alt="Docker"></a>
  <a href="#"><img src="https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go" alt="Go"></a>
  <a href="#"><img src="https://img.shields.io/badge/React-18-61DAFB?style=for-the-badge&logo=react" alt="React"></a>
  <a href="https://ko-fi.com/zeroq"><img src="https://img.shields.io/badge/Support-Ko--fi-FF5E5B?style=for-the-badge&logo=ko-fi" alt="Ko-fi"></a>
</p>

---

## 📸 Screenshots

<table>
  <tr>
    <td><img src="https://github.com/user-attachments/assets/1f38d243-c68c-4b89-9a4d-dd3e63517ab4" alt="Dashboard" width="400"/></td>
    <td><img src="https://github.com/user-attachments/assets/a80802ca-4c8e-49d7-a463-71cc5968b817" alt="Library" width="400"/></td>
  </tr>
  <tr>
    <td><img src="https://github.com/user-attachments/assets/ad8219d7-da4c-404c-8f76-43e9b5b19937" alt="Movie Details" width="400"/></td>
    <td><img src="https://github.com/user-attachments/assets/b06b7f24-fb5a-4940-ba1b-0d76c469749d" alt="Discover" width="400"/></td>
  </tr>
  <tr>
    <td><img src="https://github.com/user-attachments/assets/c1ec1d3d-ff43-43c5-b719-0bd28e804cb2" alt="Live TV" width="400"/></td>
    <td><img src="https://github.com/user-attachments/assets/737a5b6f-20d7-47d3-a55e-3f751fa0af0f" alt="Settings" width="400"/></td>
  </tr>
</table>

---

## 🧭 What StreamArr Pro Is

StreamArr Pro is a self-hosted media manager that sits between discovery, cached stream sources, and IPTV apps:

- **Library manager** for movies and shows using TMDB metadata
- **Discovery layer** for Stremio-compatible providers such as Torrentio, Comet, and MediaFusion
- **IPTV server** with Xtream Codes and M3U output for players like TiviMate and VLC

StreamArr is not meant to be a traditional downloader. It tracks metadata, checks cached streams, and exposes playable IPTV endpoints while dedicated media managers handle filesystem libraries.

## 🆕 What We Added Recently

- **DMM provider hooks** for Real-Debrid cached hash lookups
- **Recovery and filtering guards** for malformed hashes, junk torrents, and unreleased media
- **Faster background services** for Real-Debrid sync and cache scanning
- **Safer library cleanup tools** for filter mismatches and missing Real-Debrid items

## ✨ Key Features

### 🎬 Media Library
- **Comprehensive Content** — Add movies & TV shows from TMDB with full metadata, posters, and descriptions
- **Smart Collections** — Auto-detect franchises (Marvel, Star Wars, etc.) and add entire collections
- **MDBList Integration** — Auto-sync with your watchlists, trending lists, and custom lists
- **Advanced Filtering** — Filter by genre, year, rating, language with multi-select dropdowns
- **Sorting Options** — Sort by title, date added, release date, rating, runtime, and more
- **Bulk Management** — Mass select and delete items from your library
- **Calendar View** — Track upcoming movie releases and episode air dates

### 📺 Streaming Engine
- **Multi-Provider Support** — Works with Torrentio, Comet, MediaFusion, and other Stremio addons
- **Premium Debrid** — Real-Debrid, Premiumize, and AllDebrid integration for cached streams
- **Smart Fallback** — Automatically tries multiple providers until finding available streams
- **Stream Selection** — View quality, file size, codec, seeders, and cache status
- **Quality Filters** — Filter by resolution, exclude CAM/TS, set max file size

### 🧩 Debrid Workflow
- **Real-Debrid Library Sync** — Adds the best cached library streams into your RD account when enabled
- **DMM Provider Mode** — Uses DMM-style cached hash lookups as another stream source
- **Safer Stream Filtering** — Skips unreleased titles and retires obviously broken payloads such as subtitle-only or image-only results
- **Configurable Workers** — RD library sync and cache scanning can run on short recurring intervals

### 📡 Live TV
- **M3U Playlist Support** — Import your own IPTV sources
- **EPG Guide** — Electronic Program Guide with XMLTV support
- **Category Filters** — Sports, News, Entertainment, Kids, and more
- **Channel Management** — Enable/disable sources and organize channels

### 📱 IPTV App Compatibility
- **Xtream Codes API** — Full compatibility with popular IPTV apps
- **Tested Apps** — TiviMate, iMPlayer, Chillio, IPTV Smarters, XCIPTV, OTT Navigator
- **M3U Export** — Standard playlist for VLC, Kodi, and any M3U player
- **VOD Support** — Movies and series appear as Video on Demand

### 🎨 Modern Interface
- **Netflix-Style UI** — Dark theme with horizontal scrolling, hover effects, and smooth animations
- **Discover Page** — Browse trending content with sorting by popularity, rating, and release date
- **Detail Modals** — Full movie/series info with seasons, episodes, and stream selection
- **Responsive Design** — Works on desktop, tablet, and mobile

---

## 🛠️ Tech Stack

| Component | Technology |
|-----------|------------|
| **Backend** | Go 1.21+ with Gorilla Mux |
| **Frontend** | React 18 + TypeScript + Vite |
| **Styling** | Tailwind CSS |
| **Database** | PostgreSQL 16 |
| **Containerization** | Docker & Docker Compose |
| **State Management** | TanStack Query |

---

## 🚀 Quick Start

### Prerequisites
- Docker & Docker Compose
- TMDB API Key ([Get free key](https://developer.themoviedb.org/docs/getting-started))

### Installation

```bash
# Clone the repository
git clone https://github.com/ZeroQ-bit/StreamArr-Pro.git
cd StreamArr-Pro

# Start with Docker Compose
docker compose up -d

# View logs (optional)
docker compose logs -f streamarr
```

**🎉 Done!** Open http://localhost:8080 in your browser.

### First-Time Setup
- Open the app and create your first admin account
- Add your TMDB API key in Settings to enable metadata lookups
- Add your stream providers and optional Debrid credentials
- Use Riven or another dedicated media manager for Plex-visible filesystem libraries

---

## 🐳 Docker Installation (Detailed)

### Important: Working Directory

> ⚠️ **Always run `docker compose` commands from the project directory!**
> 
> The in-app update feature requires the container to be started from the cloned repository folder.
> This ensures the host volume mount (`.:/app/host`) points to the correct location for git updates.

### Standard Installation (Recommended)

```bash
# 1. Clone to your preferred location
git clone https://github.com/ZeroQ-bit/StreamArr-Pro.git /opt/streamarr
cd /opt/streamarr

# 2. (Optional) Configure environment
cp .env.example .env
# Edit .env with your settings

# 3. Start the containers
docker compose up -d

# 4. Verify containers are running
docker ps
```

### VPS/Server Installation

```bash
# SSH into your server
ssh user@your-server-ip

# Clone repository
git clone https://github.com/ZeroQ-bit/StreamArr-Pro.git ~/StreamArr-Pro
cd ~/StreamArr-Pro

# Start with Docker Compose (always from this directory!)
docker compose up -d --build

# Check logs
docker compose logs -f streamarr
```

### Updating via UI

The **Update App** button in Settings will:
1. Pull latest code from GitHub
2. Rebuild the Docker image
3. Restart containers automatically

**Requirements for in-app updates:**
- Container must be started from the git repository directory
- Docker socket must be mounted (`/var/run/docker.sock`)
- Host directory must be mounted (`.:/app/host`)

If updates aren't working, rebuild from the correct directory:
```bash
cd /path/to/StreamArr-Pro  # Your cloned repository
docker compose down
docker compose up -d --build
```

### Manual Update

```bash
cd /path/to/StreamArr-Pro
git pull origin main
docker compose down
docker compose up -d --build
```

### Docker Compose Volumes Explained

```yaml
volumes:
  - streamarr_cache:/app/cache      # Persistent cache
  - streamarr_logs:/app/logs        # Application logs
  - ./channels:/app/channels        # EPG channel data
  - ./proxies.txt:/app/proxies.txt  # Proxy configuration
  - /var/run/docker.sock:/var/run/docker.sock  # For in-app updates
  - .:/app/host                     # Git repo access for updates
```

---

## 🔐 Security Notes

- **API keys do not belong in the repository.** Add them through the UI or in a local `.env` file that is not committed.
- `.env.example`, `docker-compose.yml`, and `systemd/` files in this repo only contain placeholders or example defaults.
- If you fork or deploy StreamArr, change example JWT / password defaults before exposing the app outside your local network.

## ⚙️ Configuration

### 1. API Keys (Settings → API Keys)

| Setting | Description | Required |
|---------|-------------|----------|
| **TMDB API Key** | For movie/series metadata | ✅ Yes |
| **MDBList API Key** | For watchlist sync | Optional |
| **Real-Debrid API Key** | For premium cached streams | Optional |

### 2. Stream Providers (Settings → Addons)

Add Stremio-compatible provider URLs:

| Provider | Example URL | Notes |
|----------|-------------|-------|
| **Comet** | `https://comet.elfhosted.com` | Fast, good cache detection |
| **Torrentio** | `https://torrentio.strem.fun` | Most sources, highly configurable |
| **MediaFusion** | `https://mediafusion.elfhosted.com` | Good alternative |

> 💡 **Tip:** Add multiple providers for automatic fallback if one fails.

### 3. Quality Settings (Settings → Quality)

- **Max Resolution** — 4K, 1080p, or 720p
- **Max File Size** — Skip oversized files
- **Excluded Qualities** — CAM, TS, SCR, HDTS
- **Language Filters** — Exclude unwanted languages

### 4. Real-Debrid Library Sync (Settings → Services)

- **Real-Debrid Library Sync** adds the best cached library stream into your RD library when enabled
- **Stream cache checks** mark movies and episodes as available or unavailable for IPTV filters
- **DMM Provider Mode** can be enabled to query cached hash data as another stream source
- **Important:** Plex-visible folders are handled outside StreamArr; use Riven or another filesystem library manager for that workflow

---

## 📱 IPTV App Setup

### Xtream Codes Login

| Field | Value |
|-------|-------|
| **Server URL** | `http://YOUR-IP:8080` |
| **Username** | Set in Settings → Xtream |
| **Password** | Set in Settings → Xtream |

### M3U Playlist URL
```
http://YOUR-IP:8080/get.php?username=user&password=pass&type=m3u_plus&output=ts
```

### Tested Applications

| App | Platform | Status |
|-----|----------|--------|
| TiviMate | Android TV | ✅ Excellent |
| iMPlayer | iOS / Apple TV | ✅ Excellent |
| Chillio | Apple TV | ✅ Excellent |
| IPTV Smarters | All | ✅ Works |
| OTT Navigator | Android | ✅ Works |
| VLC / Kodi | All | ✅ M3U |

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        StreamArr Pro                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐    ┌──────────────┐    ┌─────────────────┐   │
│  │   React UI   │───▶│   Go API     │───▶│  PostgreSQL     │   │
│  │  (Vite/TS)   │    │  (Gorilla)   │    │  (Database)     │   │
│  └──────────────┘    └──────────────┘    └─────────────────┘   │
│         │                   │                                   │
│         │                   ▼                                   │
│         │           ┌──────────────┐    ┌─────────────────┐    │
│         │           │   Providers  │───▶│   Real-Debrid   │    │
│         │           │  (Torrentio) │    │   (Caching)     │    │
│         │           └──────────────┘    └─────────────────┘    │
│         │                                                       │
│         ▼                                                       │
│  ┌────────────────────────────────────────────────────────┐    │
│  │              Xtream Codes API                           │    │
│  │   /player_api.php  •  /movie/  •  /series/  •  /live/  │    │
│  └────────────────────────────────────────────────────────┘    │
│                              │                                  │
│                              ▼                                  │
│  ┌────────────────────────────────────────────────────────┐    │
│  │  TiviMate • iMPlayer • Chillio • IPTV Smarters • VLC   │    │
│  └────────────────────────────────────────────────────────┘    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 🔧 Docker Commands

```bash
# Start services
docker compose up -d

# Stop services
docker compose down

# View logs
docker compose logs -f streamarr

# Rebuild after updates
git pull && docker compose up -d --build

# Full reset (WARNING: deletes all data)
docker compose down -v && docker compose up -d
```

---

## 📊 API Endpoints

### REST API (v1)
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/movies` | List all movies |
| GET | `/api/v1/movies/{id}/streams` | Get movie streams |
| GET | `/api/v1/series` | List all series |
| GET | `/api/v1/series/{id}/episodes` | Get series episodes |
| GET | `/api/v1/channels` | List live channels |
| GET | `/api/v1/health` | Health check |
| POST | `/api/v1/movies` | Add movie to library |
| DELETE | `/api/v1/movies/{id}` | Remove movie |

### Xtream Codes API
| Endpoint | Description |
|----------|-------------|
| `/player_api.php` | Main Xtream API |
| `/get.php` | Playlist generation |
| `/movie/{user}/{pass}/{id}.mp4` | Movie stream |
| `/series/{user}/{pass}/{id}.mp4` | Episode stream |
| `/live/{user}/{pass}/{id}.m3u8` | Live channel |
| `/xmltv.php` | EPG data |

---

## 🐛 Troubleshooting

<details>
<summary><b>No streams found</b></summary>

1. Check that at least one provider addon is configured
2. Verify your Real-Debrid API key is valid
3. Ensure addon URLs don't have trailing slashes
4. Try different content (some may not have sources)
</details>

<details>
<summary><b>IPTV app won't connect</b></summary>

1. Use your server's IP address, not `localhost`
2. Ensure port 8080 is open/accessible
3. Check credentials in Settings → Xtream
4. Some apps require `http://` prefix
</details>

<details>
<summary><b>Streams buffer or won't play</b></summary>

1. Verify Real-Debrid subscription is active
2. Prefer streams with ⚡ Cached badge
3. Try lower quality (1080p instead of 4K)
4. Try a different stream source
</details>

<details>
<summary><b>Live TV not working</b></summary>

1. Go to Settings → Services → Refresh Channels
2. Enable sources in Settings → Live TV
3. Wait for EPG to load (can take a few minutes)
</details>

---

## 📁 Project Structure

```
StreamArr-Pro/
├── cmd/                    # Application entrypoints
│   ├── server/             # Main server
│   ├── worker/             # Background worker
│   └── migrate/            # Database migrations
├── internal/               # Core application code
│   ├── api/                # REST API handlers & routes
│   ├── auth/               # Authentication middleware
│   ├── database/           # Database stores
│   ├── models/             # Data models
│   ├── providers/          # Stream providers
│   ├── services/           # Business logic (TMDB, MDBList, etc.)
│   └── xtream/             # Xtream Codes API
├── streamarr-pro-ui/       # React frontend
│   ├── src/
│   │   ├── components/     # Reusable UI components
│   │   ├── pages/          # Page components
│   │   └── services/       # API client
│   └── package.json
├── migrations/             # SQL migrations
├── docker-compose.yml      # Docker configuration
└── Dockerfile              # Multi-stage build
```

---

## 🤝 Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing`)
5. Open a Pull Request

---

## 📝 License

MIT License - see [LICENSE.md](LICENSE.md)

---

## ☕ Support

If StreamArr Pro is useful to you, consider supporting development:

<a href="https://ko-fi.com/zeroq"><img src="https://www.ko-fi.com/img/githubbutton_sm.svg" alt="Support on Ko-fi"></a>

---

## ⚠️ Disclaimer

StreamArr Pro is a self-hosted media organizer for **personal, lawful use only**. It does not host, index, or distribute any media content. Users are responsible for ensuring compliance with local laws and terms of service for any third-party services they configure.

---

<p align="center">
  Made with ❤️ by <a href="https://github.com/ZeroQ-bit">ZeroQ</a>
</p>
