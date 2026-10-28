# Changelog

All notable changes to this project are documented here. This project follows [Semantic Versioning](https://semver.org).

## [1.6.0] - 2026-10-28

### Added
- Column view previews: images and videos show a thumbnail (images compressed to at most 640 px, videos their first frame, taken 1 s in to skip black fade-ins). Thumbnails are made on first view with ffmpeg at low priority, one at a time, and cached in `<data>/thumbs` (keyed by path, size and modification time, so edited files get a fresh one; capped at 300 MB, least recently used removed first). Without ffmpeg, JPEG/PNG/GIF thumbnails are made in Go. Click the thumbnail to open the image or play the video.

### Fixed
- Column view sometimes reloaded every column from the left after opening a folder: the folder cache expired after 15 s. Folders now stay cached until you open them again, columns keep their scroll position, and only the newly opened folder is fetched.
- Column view: the file-type icon in the preview was not centred.

## [1.5.1] - 2026-10-28

### Fixed
- Loading placeholders now match the active layout. In column view, opening a folder keeps the existing columns and shows a loading column only for the folder being opened (previously the whole view switched to list-style placeholder rows). Table view shows placeholder table rows with the column headings; list view is unchanged.

## [1.5.0] - 2026-10-28

### Added
- Each tab (Files, Movies, Storage, Shared Links, Settings) remembers where you left it, including the scroll position, for the browser session. Tapping the tab you're already on goes to its top level.

- File table: on wider screens the file list is a table with Name, Size, Type and Date Modified columns. Click a column heading to sort (Type sorting is new); the header stays pinned while scrolling. A List / Table switch sits next to the item count and is remembered, as is the sort order. Phones keep the compact list.
- Column view (desktop): a third layout next to List and Table. Each opened folder adds a column to the right showing names only; choosing a file adds a preview + details column (image, video, audio, PDF or text preview; type, size, modified, location; Open, Download, more). Columns have a small fixed width, can be resized by dragging their edge (double-click resets; widths are remembered), and the strip scrolls sideways, keeping the newest column in view. Clicking an item in an earlier column jumps back. Double-click a file to open it.

### Changed
- Storage: on wide screens (1180 px and up) the overview card and the "By size" folder list sit side by side; Largest files stays full width below. Narrower screens stack them as before.
- Shared Links and Settings are now full pages (like Files and Storage) instead of pop-ups, with their own address (`#shares`, `#settings`, `#settings/movies`), so browser back/forward works. Shared links are grouped into Active and Expired, shown as file-style rows with Copy, Open and a menu (rclone config, Show in Files, Revoke).

- Storage card: the type breakdown is now a table, one row per type with its colour, a bar, the size and its share of used space, under column headings. Free space is shown separately. Replaces the legend where sizes sat far from their labels.

### Fixed
- Test fix: TestFolderSize could fail intermittently when the analyzer indexed a file between its creation and write.
- Folder size sheet: long file names are cut off with "…" instead of widening the sheet and hiding the total.

## [1.4.0] - 2026-10-28

### Added
- Folder size: "Folder size" in a folder's menu (and a chart button for the current folder) shows total size, file and folder counts, a breakdown by type and the largest files. Instant from the storage index; "Rescan" re-reads just that folder. Works with the analyzer turned off too (scans the folder directly).
- Storage: "Rescan" inside a folder offers "Rescan this folder" (only that subtree is re-read and updated in the index) or the whole drive.

## [1.3.1] - 2026-10-28

### Changed
- Breadcrumb: larger text (15 px) and more vertical padding, easier to tap.

### Added
- Loading feedback everywhere: a thin progress bar at the top for any request slower than 150 ms; opening a folder shows a spinner on the row, then the folder's title, path and skeleton rows; Storage, Movies, the viewer, Settings, share lists and the public share page show loaders; search shows a spinner in the search field.
- Buttons that do work (Unlock, Save, Create link, Organise, Scan, Fetch, Revoke, log out device…) show a spinner and can't be pressed twice.
- File operations (delete, move, copy, rename, new folder) show a small "Deleting 3 items…" pill while they run.

### Fixed
- Clicking through folders quickly could briefly show an older folder's contents (out-of-order responses are now ignored). Same for search.

## [1.3.0] - 2026-10-26

### Added
- rclone / WebDAV access for share links. Every share has a WebDAV address that follows the same rules as the web page (password, expiry, download limit, read-only, upload-only drop box; visitors can never delete, rename or overwrite). The share dialog and the public page show a ready-to-paste rclone config (password included, already obscured). Large uploads go in 16 MB pieces using rclone's Nextcloud chunked-upload mode, so they work behind Cloudflare's 100 MB request limit. Needs rclone 1.63 or newer. Checksums are returned so `rclone check` can verify uploads.

## [1.2.3] - 2026-10-23

### Fixed
- Movie names with a number that looks like a year before the real year ("Blade.Runner.2049.2017") were matched to the wrong film. Years in the future are now treated as part of the title, and adjacent years are parsed correctly.

### Changed
- README screenshots and banner updated for the redesign, Movies, the player, Storage and the new share dialog.

## [1.2.2] - 2026-10-23

### Fixed
- Video player overflowed the window on wide or short screens (the control bar was pushed below the visible area). The player is now letterboxed to fit the available space, using the video's real aspect ratio.

## [1.2.1] - 2026-10-23

### Added
- Trailer button on every movie page: plays the official trailer (picked from TMDB, YouTube privacy-enhanced embed).

### Changed
- New video player: Video.js 10 with its frosted skin (the player used on media-chrome.org): captions menu, playback speed, picture-in-picture, fullscreen, keyboard shortcuts, touch gestures.
- Converted playback now uses on-demand HLS: the player sees the full movie, so its normal seek bar, duration and subtitles work, and iPhone/Safari can play converted videos. Segments are created only as you watch: the converter pauses when it is about a minute ahead, stops 90 s after playback ends, and only one video conversion runs at a time. Audio-only conversion copies the video and cuts at its real keyframes.
- Referrer-Policy is now `strict-origin` (was `no-referrer`) so YouTube embeds work; only the site origin is ever sent.

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
