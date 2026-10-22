package main

import (
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

	// static UI
	m.HandleFunc("GET /{$}", s.serveAsset("index.html"))
	m.HandleFunc("GET /assets/{file}", s.handleAsset)
	m.HandleFunc("GET /favicon.ico", s.serveAsset("favicon.svg"))

	// auth
	m.HandleFunc("GET /api/me", s.handleMe)
	m.HandleFunc("POST /api/login", s.handleLogin)
	m.HandleFunc("POST /api/logout", s.handleLogout)
	m.HandleFunc("GET /api/passkey/login/begin", s.handlePasskeyLoginBegin)
	m.HandleFunc("POST /api/passkey/login/finish", s.handlePasskeyLoginFinish)
	m.HandleFunc("GET /api/passkey/register/begin", a(s.handlePasskeyRegBegin))
	m.HandleFunc("POST /api/passkey/register/finish", a(s.handlePasskeyRegFinish))
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
	m.HandleFunc("PUT /api/text", a(s.handleTextPut))
	m.HandleFunc("POST /api/mkdir", a(s.handleMkdir))
	m.HandleFunc("POST /api/rename", a(s.handleRename))
	m.HandleFunc("POST /api/move", a(s.handleMoveCopy(false)))
	m.HandleFunc("POST /api/copy", a(s.handleMoveCopy(true)))
	m.HandleFunc("POST /api/delete", a(s.handleDelete))
	m.HandleFunc("GET /api/disk", a(s.handleDisk))
	m.HandleFunc("POST /api/upload/init", a(s.handleUploadInit))
	m.HandleFunc("PUT /api/upload/{uid}", a(s.handleUploadChunk))
	m.HandleFunc("DELETE /api/upload/{uid}", a(s.handleUploadCancel))

	// shares (admin)
	m.HandleFunc("GET /api/shares", a(s.handleShareList))
	m.HandleFunc("POST /api/shares", a(s.handleShareCreate))
	m.HandleFunc("DELETE /api/shares/{id}", a(s.handleShareDelete))

	// shares (public)
	m.HandleFunc("GET /s/{id}", s.serveAsset("share.html"))
	m.HandleFunc("GET /s/{id}/{$}", s.serveAsset("share.html"))
	m.HandleFunc("GET /s/{id}/api/info", s.handlePubInfo)
	m.HandleFunc("POST /s/{id}/api/unlock", s.handlePubUnlock)
	m.HandleFunc("GET /s/{id}/api/list", s.handlePubList)
	m.HandleFunc("GET /s/{id}/dl", s.handlePubDownload)
	m.HandleFunc("GET /s/{id}/zip", s.handlePubZip)
	m.HandleFunc("POST /s/{id}/api/upload/init", s.handlePubUploadInit)
	m.HandleFunc("PUT /s/{id}/api/upload/{uid}", s.handlePubUploadChunk)
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

func (s *Server) sendAsset(w http.ResponseWriter, r *http.Request, name string) {
	if strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
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
	w.Header().Set("Cache-Control", "no-cache")
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
