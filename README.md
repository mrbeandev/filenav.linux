# filenav

A tiny self-hosted web file manager. It's **one static binary** (~7 MB) with **no dependencies**, no database and no Docker.
It uses **~8 MB RAM idle and ~10 MB** while serving several large downloads at once.

## Features
- Browse, upload, download, rename, move, copy, delete, search, create folders and files, edit text files
- **Large uploads**: resumable chunked uploads (16 MiB chunks, streamed straight to disk). An upload survives network drops, and if you re-add the same file it resumes where it stopped. Size is limited only by disk space. You can drag & drop whole folders.
- **Large downloads**: HTTP Range support, so downloads can be resumed and videos can be seeked. Folders and multi-file selections download as a streamed zip (zip64, no compression, so it uses almost no CPU or RAM).
- **Share links**: one link for a file or folder, with optional:
  - password
  - expiry
  - download limit
  - note shown to visitors
  - **visitor uploads** ("view + upload", or "upload only" drop-box mode)
  - Visitors can never overwrite or delete existing files.
  - Links keep working when you rename or move the shared item.
- **Auth**: a single passcode or **passkeys** (Face ID / Touch ID / Windows Hello / phone / YubiKey). You can turn on passkey-only mode, list sessions and revoke them.
- **Viewer/editor**: CodeMirror 6 code editor (syntax highlighting for 100+ languages, search, folding) for *any* text file. Text vs binary is detected by content, so `.gitignore`, `.env`, `Makefile`, etc. open too. Markdown is rendered (sanitised) with an edit toggle.
- Previews: images (including HEIC/HEIF and TIFF, decoded in a sandboxed iframe), video and audio streaming with seeking, PDF. Previous/next navigation with arrow keys or swipe.
- Works on mobile. Light and dark themes.
- Security: brute-force rate limiting, CSRF protection, traversal-safe filesystem access (Go `os.Root`), and sandboxed previews.

## Quick start
```bash
make                       # needs Go ≥ 1.24; produces ./filenav
./filenav -root ~/files    # prints a generated passcode on first start
# open http://127.0.0.1:8080
```

## Install on a server (systemd + HTTPS)
```bash
make && sudo ./deploy/install.sh
sudo nano /etc/filenav.env        # set FILENAV_ORIGIN=https://your.domain
sudo systemctl restart filenav
sudo -u filenav FILENAV_DATA=/var/lib/filenav filenav passcode   # set your passcode
```
Then put **Caddy** (`deploy/Caddyfile`, automatic HTTPS) or **nginx** (`deploy/nginx.conf`) in front of it.
**Passkeys require HTTPS** and a correct `FILENAV_ORIGIN`.

To cross-compile on your PC and copy only the binary to the server: `make release` builds `dist/filenav-linux-{amd64,arm64,armv7}`.

## Configuration (flags or env)
| Flag | Env | Default |
|---|---|---|
| `-root` | `FILENAV_ROOT` | `./files` |
| `-data` | `FILENAV_DATA` | `./filenav-data` |
| `-addr` | `FILENAV_ADDR` | `127.0.0.1:8080` |
| `-origin` | `FILENAV_ORIGIN` | auto-detected |
| `-trust-proxy` | `FILENAV_TRUST_PROXY` | `false` |
| `-chunk-mb` | `FILENAV_CHUNK_MB` | `16` |
| `-title` | `FILENAV_TITLE` | `filenav` |
| `-tls-cert/-tls-key` | `FILENAV_TLS_CERT/KEY` | (serve HTTPS directly) |

## CLI
```
filenav passcode         # set/change passcode (logs everyone out)
filenav reset-passkeys   # recovery if you lose your passkey in passkey-only mode
filenav logout-all
```

## Frontend libraries
The third-party browser libraries (CodeMirror, marked, DOMPurify, heic2any, UTIF) are pre-bundled into `web/vendor/` and committed, so building filenav only needs Go.
To update them: `cd ui-vendor && npm ci && npm run build`.

## Notes
- State (sessions, passkeys, shares) lives in `$FILENAV_DATA/state.json`. Back it up along with your files.
- Partial uploads are kept in `<root>/.filenav-uploads/` (hidden in the UI) and deleted after 48 h of inactivity.
- Behind **Cloudflare**, keep `FILENAV_CHUNK_MB` below 100.
