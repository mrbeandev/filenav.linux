# filenav

**A tiny self-hosted web file manager for Linux.** It's one static binary (~10 MB) with no dependencies, no database and no Docker.
It uses **~8 MB of RAM when idle**, so it's a good fit for small VPSes and Raspberry Pis where Nextcloud is too heavy.

- 📁 Browse, upload, download, rename, move, copy, delete, search
- 🚀 **Large files**: resumable chunked uploads that survive network drops; resumable downloads; video seeking
- 🔗 **Share links** with optional password, expiry, download limit, and **visitor uploads** (including an upload-only "drop box" mode)
- 🔑 **Passkeys** (Face ID / fingerprint / Windows Hello / security keys), with a passcode as fallback
- 📝 Code editor (CodeMirror, 100+ languages) for any text file; Markdown rendering
- 🖼️ Previews for images (including HEIC & TIFF), video, audio and PDF
- 📱 Mobile-friendly UI with dark and light themes
- ⚙️ `filenav setup` installs it as a systemd service in one command

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mrbeandev/filenav.linux/main/scripts/install.sh | sh
```
This downloads the right binary for your machine (amd64, arm64, armv7, 386) to `~/.local/bin`, or to `/usr/local/bin` when run as root.
You can also grab it yourself from [Releases](https://github.com/mrbeandev/filenav.linux/releases).

Then either:

```bash
filenav          # run in the foreground; on first run it creates ~/.filenav_linux/config.yml and prints a passcode
filenav setup    # install as a service that starts at boot (asks a few questions)
```

`filenav setup` creates a **user** service when run as a normal user, or a **system** service when run with `sudo`.
If the service is already installed, it checks everything and prints the status instead:

```
filenav is already set up.

  ✓ service    active (system)
  ✓ autostart  enabled
  ✓ config     /home/me/.filenav_linux/config.yml
  ✓ binary     /usr/local/bin/filenav
    serving    /home/me/filenav
  ✓ reachable  http://127.0.0.1:8080

Everything is fully set up and running.
```

## Configuration

Everything lives in **`~/.filenav_linux/config.yml`**. It's created with comments on first run; JSON (`config.json`) works too.
Restart after editing it: `filenav restart`.

```yaml
root: "/home/me/filenav"            # folder shown in the file manager
data_dir: "/home/me/.filenav_linux/data"
address: "127.0.0.1"                # 0.0.0.0 to reach it from other devices
port: 8080
origin: ""                          # public URL, e.g. https://files.example.com (needed for passkeys behind a proxy)
trust_proxy: false                  # true behind Caddy / nginx / Cloudflare
title: "filenav"

auth: true                          # false = no login at all (only on trusted networks!)
passkeys: true                      # allow passkey sign-in
passcode: ""                        # initial passcode, only used until you change it in the UI
session_days: 30

read_only: false                    # block uploads, edits, renames, moves and deletes
show_hidden: true                   # show dotfiles
max_upload_mb: 0                    # 0 = unlimited
chunk_mb: 16                        # upload chunk size (keep < 100 behind Cloudflare)

shares: true                        # public share links
share_uploads: true                 # share links that accept uploads

tls_cert: ""                        # serve HTTPS directly (optional)
tls_key: ""
```

Settings are applied in this order, each overriding the one before: built-in defaults → config file → `FILENAV_*` environment variables (e.g. `FILENAV_PORT=9000`) → command-line flags (`filenav serve -port 9000`, see `filenav serve -h`).
Use another config file with `--config /path/config.yml`. `filenav config` prints the effective settings.

> The `passcode` setting is only used **on first start**. Once you change the passcode in Settings or with `filenav passcode`, the config value is ignored.

## Commands

| Command | |
|---|---|
| `filenav` / `filenav serve` | run the server |
| `filenav setup` | install the systemd service, or check its status if already installed (`--force` to reinstall/upgrade, `--yes` for defaults) |
| `filenav status` | service and health report |
| `filenav start` / `stop` / `restart` | control the service |
| `filenav uninstall` | remove the service (keeps your files and config) |
| `filenav config` | print the effective configuration |
| `filenav passcode` | set a new passcode (logs everyone out) |
| `filenav reset-passkeys` | remove all passkeys (recovery if you're locked out) |
| `filenav logout-all` | end all sessions |
| `filenav update` | update to the latest release: checks GitHub, verifies the SHA-256, replaces the binary and restarts the service (`--check` to only check, `--version v1.2.3` for a specific version) |

**Upgrading:** `filenav update` (use `sudo filenav update` if the binary is in a root-owned directory such as `/usr/local/bin`).

## HTTPS / domain

Passkeys need HTTPS (or `localhost`). The simplest way to get HTTPS is [Caddy](https://caddyserver.com), which fetches certificates automatically ([`deploy/Caddyfile`](deploy/Caddyfile)):

```
files.example.com {
	reverse_proxy 127.0.0.1:8080 {
		flush_interval -1
	}
}
```

Then set `origin: "https://files.example.com"` and `trust_proxy: true`. For nginx, see [`deploy/nginx.conf`](deploy/nginx.conf). The important parts are `proxy_request_buffering off` and `proxy_buffering off`.

## How it works

- **Uploads** are sent in chunks (16 MB by default) and written straight to disk. If a chunk fails, it's retried with backoff. If you add the same file again later, the upload resumes where it stopped. Partial uploads are kept in `<root>/.filenav-uploads/` and deleted after 48 h of inactivity.
- **Downloads and video** use HTTP Range requests, so downloads resume and videos stream and seek. The server never converts video, so playback depends on what your browser supports (MP4/H.264 and WebM work everywhere).
- **Folders and multi-file selections** download as an uncompressed zip that's built on the fly, so it uses almost no CPU or RAM.
- **State** (sessions, passkeys, share links) is a single JSON file in `data_dir`. Back it up along with your files.
- **Security**:
  - all file access is confined to `root` (Go's `os.Root`), so tricks like `../` can't escape it
  - CSRF protection, plus rate limiting on login and share passwords
  - passcodes are hashed with PBKDF2
  - previews are sandboxed
  - share visitors can never overwrite or delete files

## Building from source

```bash
git clone https://github.com/mrbeandev/filenav.linux && cd filenav.linux
make            # needs Go ≥ 1.24
make test
make release    # cross-compile into dist/
```

The browser libraries (CodeMirror, marked, DOMPurify, heic2any, UTIF) are pre-bundled into `web/vendor/`, so building only needs Go. To update them: `make vendor` (needs Node).

## License

[MIT](LICENSE)
