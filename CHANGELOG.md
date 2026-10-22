# Changelog

All notable changes to this project are documented here. This project follows [Semantic Versioning](https://semver.org).

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
