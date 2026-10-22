package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Server struct {
	cfg        Config
	absRoot    string
	root       *os.Root
	st         *Store
	web        fs.FS
	mux        *http.ServeMux
	loginLimit *limiter
	shareLimit *limiter
	challenges *challengeStore

	upMu    sync.Mutex
	uploads map[string]*uploadMeta
	upLocks map[string]*sync.Mutex
}

func NewServer(cfg Config, absRoot string, root *os.Root, st *Store, web fs.FS) *Server {
	s := &Server{
		cfg: cfg, absRoot: absRoot, root: root, st: st, web: web,
		mux:        http.NewServeMux(),
		loginLimit: newLimiter(10, 15*time.Minute),
		shareLimit: newLimiter(20, 15*time.Minute),
		challenges: newChallengeStore(),
		uploads:    map[string]*uploadMeta{},
		upLocks:    map[string]*sync.Mutex{},
	}
	s.loadUploads()
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	a := s.requireAuth
	wr := s.requireWrite
	sh := func(h http.HandlerFunc) http.HandlerFunc {
		return requireFeature(s.cfg.Shares, "sharing is disabled in the config", h)
	}

	// static UI
	m.HandleFunc("GET /{$}", s.serveAsset("index.html"))
	m.HandleFunc("GET /assets/{file...}", s.handleAsset)
	m.HandleFunc("GET /favicon.ico", s.serveAsset("favicon.svg"))

	// auth
	m.HandleFunc("GET /api/me", s.handleMe)
	m.HandleFunc("POST /api/login", s.handleLogin)
	m.HandleFunc("POST /api/logout", s.handleLogout)
	m.HandleFunc("GET /api/passkey/login/begin", requireFeature(s.cfg.Passkeys, "passkeys are disabled in the config", s.handlePasskeyLoginBegin))
	m.HandleFunc("POST /api/passkey/login/finish", requireFeature(s.cfg.Passkeys, "passkeys are disabled in the config", s.handlePasskeyLoginFinish))
	m.HandleFunc("GET /api/passkey/register/begin", a(requireFeature(s.cfg.Passkeys, "passkeys are disabled in the config", s.handlePasskeyRegBegin)))
	m.HandleFunc("POST /api/passkey/register/finish", a(requireFeature(s.cfg.Passkeys, "passkeys are disabled in the config", s.handlePasskeyRegFinish)))
	m.HandleFunc("GET /api/passkeys", a(s.handlePasskeyList))
	m.HandleFunc("DELETE /api/passkeys/{id}", a(s.handlePasskeyDelete))
	m.HandleFunc("POST /api/settings/passcode", a(s.handleChangePasscode))
	m.HandleFunc("POST /api/settings/passkey-only", a(s.handlePasskeyOnly))
	m.HandleFunc("GET /api/sessions", a(s.handleSessions))
	m.HandleFunc("DELETE /api/sessions/{id}", a(s.handleRevokeSessions))

	// files
	m.HandleFunc("GET /api/list", a(s.handleList))
	m.HandleFunc("GET /api/dl", a(s.handleDownload))
	m.HandleFunc("GET /api/zip", a(s.handleZip))
	m.HandleFunc("GET /api/search", a(s.handleSearch))
	m.HandleFunc("GET /api/text", a(s.handleTextGet))
	m.HandleFunc("PUT /api/text", wr(s.handleTextPut))
	m.HandleFunc("POST /api/mkdir", wr(s.handleMkdir))
	m.HandleFunc("POST /api/rename", wr(s.handleRename))
	m.HandleFunc("POST /api/move", wr(s.handleMoveCopy(false)))
	m.HandleFunc("POST /api/copy", wr(s.handleMoveCopy(true)))
	m.HandleFunc("POST /api/delete", wr(s.handleDelete))
	m.HandleFunc("GET /api/disk", a(s.handleDisk))
	m.HandleFunc("POST /api/upload/init", wr(s.handleUploadInit))
	m.HandleFunc("PUT /api/upload/{uid}", wr(s.handleUploadChunk))
	m.HandleFunc("DELETE /api/upload/{uid}", wr(s.handleUploadCancel))

	// shares (admin)
	m.HandleFunc("GET /api/shares", a(s.handleShareList))
	m.HandleFunc("POST /api/shares", a(sh(s.handleShareCreate)))
	m.HandleFunc("DELETE /api/shares/{id}", a(s.handleShareDelete))

	// shares (public)
	m.HandleFunc("GET /s/{id}", s.serveAsset("share.html"))
	m.HandleFunc("GET /s/{id}/{$}", s.serveAsset("share.html"))
	m.HandleFunc("GET /s/{id}/api/info", sh(s.handlePubInfo))
	m.HandleFunc("POST /s/{id}/api/unlock", sh(s.handlePubUnlock))
	m.HandleFunc("GET /s/{id}/api/list", sh(s.handlePubList))
	m.HandleFunc("GET /s/{id}/dl", sh(s.handlePubDownload))
	m.HandleFunc("GET /s/{id}/api/text", sh(s.handlePubText))
	m.HandleFunc("GET /s/{id}/zip", sh(s.handlePubZip))
	m.HandleFunc("POST /s/{id}/api/upload/init", sh(s.handlePubUploadInit))
	m.HandleFunc("PUT /s/{id}/api/upload/{uid}", sh(s.handlePubUploadChunk))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	if s.isHTTPS(r) {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	s.mux.ServeHTTP(w, r)
}

const uiCSP = "default-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; " +
	"style-src 'self' 'unsafe-inline'; script-src 'self'; frame-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'self'"

func (s *Server) serveAsset(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.sendAsset(w, r, name) }
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	s.sendAsset(w, r, r.PathValue("file"))
}

var assetStart = time.Now()

// The HEIC/TIFF decoder needs eval (emscripten); it only ever runs inside this
// opaque-origin sandbox, never in the app's own origin.
const decoderCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval' blob:; " +
	"worker-src blob:; img-src blob: data:; connect-src blob: data:"

func (s *Server) sendAsset(w http.ResponseWriter, r *http.Request, name string) {
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			http.NotFound(w, r)
			return
		}
	}
	if name == "imgdecode.html" {
		s.sendDecoder(w, r)
		return
	}
	f, err := s.web.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "asset", 500)
		return
	}
	w.Header().Set("Content-Security-Policy", uiCSP)
	if strings.HasPrefix(name, "vendor/c/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // content-hashed
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, assetStart, rs)
}

// ---------- JSON helpers ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		jsonErr(w, 400, "bad request body")
		return false
	}
	return true
}

// fsErr maps filesystem errors to HTTP responses without leaking absolute paths.
func fsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		jsonErr(w, 404, "not found")
	case errors.Is(err, fs.ErrExist):
		jsonErr(w, 409, "already exists")
	case errors.Is(err, fs.ErrPermission):
		jsonErr(w, 403, "permission denied")
	case errors.Is(err, errBadPath):
		jsonErr(w, 400, err.Error())
	default:
		var pe *fs.PathError
		if errors.As(err, &pe) {
			jsonErr(w, 500, pe.Op+": "+pe.Err.Error())
			return
		}
		var le *os.LinkError
		if errors.As(err, &le) {
			jsonErr(w, 500, le.Op+": "+le.Err.Error())
			return
		}
		jsonErr(w, 500, err.Error())
	}
}

// ---------- background cleanup ----------

func (s *Server) janitor(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	s.cleanup()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.cleanup()
		}
	}
}

func (s *Server) cleanup() {
	s.loginLimit.GC()
	s.shareLimit.GC()
	s.challenges.GC()
	now := time.Now()
	_ = s.st.Update(func(st *State) error {
		for k, v := range st.Sessions {
			if now.After(v.Expires) {
				delete(st.Sessions, k)
			}
		}
		for k, v := range st.Shares {
			if v.Expired(now.Add(-7 * 24 * time.Hour)) { // keep a week so the owner can see them
				delete(st.Shares, k)
			}
		}
		return nil
	})
	if n := s.cleanStaleUploads(48 * time.Hour); n > 0 {
		log.Printf("janitor: removed %d stale partial uploads", n)
	}
}

var (
	decoderOnce sync.Once
	decoderPage []byte
)

// sendDecoder serves the HEIC/TIFF decoder page with its script inlined:
// the page is sandboxed (opaque origin) so it can't load same-origin scripts.
func (s *Server) sendDecoder(w http.ResponseWriter, r *http.Request) {
	decoderOnce.Do(func() {
		js, err := fs.ReadFile(s.web, "vendor/imgdecode.js")
		if err != nil {
			return
		}
		js = bytes.ReplaceAll(js, []byte("</script"), []byte("<\\/script"))
		decoderPage = append([]byte("<!doctype html><meta charset=utf-8><script>"), js...)
		decoderPage = append(decoderPage, "</script>"...)
	})
	if decoderPage == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", decoderCSP)
	h.Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(decoderPage)
}
