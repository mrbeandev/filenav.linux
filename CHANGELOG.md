# Changelog

All notable changes to this project are documented here. This project follows [Semantic Versioning](https://semver.org).

## [1.2.0] - 2026-10-23

### Added
- Movies section: point it at a folder, press Scan, and filenav identifies each video (TMDB), organises it into `Title (Year)/`, downloads poster, backdrop and English subtitles (OpenSubtitles), and shows a poster library with movie pages (cast, plot, rating) and a player. Unrecognised files go to "Unknown", where you can search TMDB or paste an IMDb ID. Nothing runs in the background. Keys are set in Settings → Movies and are never sent back to the browser. Config: `movies` (on by default).
- Smart video playback: files play directly when the browser supports them; otherwise filenav (with ffmpeg installed) converts only what's needed while streaming: audio only (AC3/DTS/TrueHD → AAC, almost no CPU) or video (HEVC etc. → H.264 720p). Seeking and subtitles work in converted streams.
- Storage analyzer: cached index of folder sizes with a Storage view (ring chart by file type, folders by size, largest files). Full scans are parallel (2.5M files in about 1.3 s on 16 cores); the index is saved to disk, validated on startup by folder mtime, and kept current live with inotify (falls back to a 10-minute check if the watch limit is reached). Rescan asks for confirmation and can be stopped; the previous results stay visible. Config: `analyzer` (on by default).
- Show/hide toggle on all passcode and password fields.

### Changed
- Settings split into General and Movies tabs with grouped rows, status pills and a connection test.
- UI redesigned following Apple's Human Interface Guidelines: system colors (light and dark), sidebar on wide screens and tab bar on phones, large titles, inset grouped lists, sheets and action sheets.

## [1.1.1] - 2026-10-22

### Added
- Installable web app (PWA): web manifest, app icons (including maskable and Apple touch icon), and a service worker that caches only the app shell, never files or API responses.
- README graphics: hero banner and screenshots of the real UI.

### Changed
- Selection actions moved from a bar above the file list to a floating bar at the bottom of the screen (all screen sizes); the list no longer jumps when selecting. Shows count and total size. Extra bottom padding keeps the last rows reachable.

### Fixed
- Breadcrumbs no longer wrap or squash on deep folders: they scroll sideways, jump to the current folder automatically, and fade the left edge when earlier folders are hidden (admin UI and share page).
- Right-clicking a file no longer selects it; the context menu acts on that file only (or on the whole selection when right-clicking a selected item).
- Share dialog / shared links list: long file names no longer overflow; items show the file name, folder and stats on separate lines, ellipsised, on desktop and mobile.
- `filenav -h` / `filenav help` now list all commands and the flags (previously printed "Usage of filenav :" with only flags); subcommands show their own usage; unknown commands are reported.

## [1.1.0] - 2026-10-22

### Added
- `filenav update`: checks the latest GitHub release and, if it is newer, downloads it, verifies the SHA-256 against `SHA256SUMS`, replaces the binary atomically and restarts the systemd service. Options: `--check`, `--yes`, `--force`, `--version vX.Y.Z`.

## [1.0.0] - 2026-10-22

First public release.

### Features
- Single static binary for Linux (amd64, arm64, armv7, 386), ~8 MB of RAM when idle
- File management: browse, upload (files and whole folders), download, rename, move, copy, delete, search, new folder/file
- Resumable chunked uploads; resumable range downloads; streamed zip downloads for folders and selections
- Share links with password, expiry, download limit, a note for visitors, visitor uploads and an upload-only "drop box" mode
- Passkey sign-in (WebAuthn) with passcode fallback, passkey-only mode, session management
- CodeMirror 6 editor for any text file (text detected by content); Markdown rendering
- Previews: images (including HEIC/HEIF and TIFF), video, audio, PDF; "Open with…" menu
- Config file at `~/.filenav_linux/config.yml` (YAML or JSON), env vars and flags
- Config switches: auth on/off, passkeys on/off, read-only mode, hidden files, upload size limit, sharing on/off
- `filenav setup`: one-command systemd service install (user or system) with status and health checks
- Mobile-friendly UI, dark and light themes
