package main

import (
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// ---------- admin ----------

type shareOut struct {
	*Share
	HasPass bool   `json:"has_password"`
	URL     string `json:"url"`
	Expired bool   `json:"expired"`
	Missing bool   `json:"missing"`
}

func (s *Server) shareView(r *http.Request, sh *Share) shareOut {
	c := *sh
	c.PassHash = ""
	_, err := s.root.Stat(fsPath(sh.Path))
	return shareOut{Share: &c, HasPass: sh.PassHash != "", URL: s.origin(r) + "/s/" + sh.ID,
		Expired: sh.Expired(time.Now()), Missing: err != nil}
}

func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("path")
	var list []*Share
	s.st.View(func(st *State) {
		for _, sh := range st.Shares {
			if _, ok := r.URL.Query()["path"]; ok && sh.Path != filter {
				continue
			}
			c := *sh
			list = append(list, &c)
		}
	})
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	out := make([]shareOut, 0, len(list))
	for _, sh := range list {
		out = append(out, s.shareView(r, sh))
	}
	writeJSON(w, map[string]any{"shares": out})
}

func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path         string  `json:"path"`
		ExpiresHours float64 `json:"expires_hours"` // 0 = never
		Password     string  `json:"password"`
		AllowUpload  bool    `json:"allow_upload"`
		UploadOnly   bool    `json:"upload_only"`
		Note         string  `json:"note"`
		MaxDownloads int64   `json:"max_downloads"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	rel, err := cleanRel(req.Path)
	if err != nil {
		fsErr(w, err)
		return
	}
	st, err := s.root.Stat(fsPath(rel))
	if err != nil {
		fsErr(w, err)
		return
	}
	if (req.AllowUpload || req.UploadOnly) && (!s.cfg.ShareUploads || s.cfg.ReadOnly) {
		jsonErr(w, 403, "upload links are disabled in the config")
		return
	}
	if (req.AllowUpload || req.UploadOnly) && !st.IsDir() {
		jsonErr(w, 400, "uploads are only possible on shared folders")
		return
	}
	if len(req.Note) > 2000 {
		req.Note = req.Note[:2000]
	}
	sh := &Share{
		ID: randomToken(9), Path: rel, IsDir: st.IsDir(), Created: time.Now(),
		AllowUpload: req.AllowUpload || req.UploadOnly, UploadOnly: req.UploadOnly,
		Note: strings.TrimSpace(req.Note), MaxDownload: max(req.MaxDownloads, 0),
	}
	if req.ExpiresHours > 0 {
		sh.Expires = time.Now().Add(time.Duration(req.ExpiresHours * float64(time.Hour)))
	}
	if req.Password != "" {
		if sh.PassHash, err = hashSecret(req.Password); err != nil {
			jsonErr(w, 500, err.Error())
			return
		}
	}
	_ = s.st.Update(func(st *State) error { st.Shares[sh.ID] = sh; return nil })
	out := s.shareView(r, sh)
	writeJSON(w, map[string]any{"share": out, "url": out.URL, "id": sh.ID, "rclone": s.rcloneOut(r, sh, req.Password)})
}

func (s *Server) handleShareDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.st.Update(func(st *State) error { delete(st.Shares, id); return nil })
	writeJSON(w, map[string]any{"ok": true})
}

// repointShares keeps share links working after a rename/move.
func (s *Server) repointShares(from, to string) {
	_ = s.st.Update(func(st *State) error {
		for _, sh := range st.Shares {
			if isWithin(sh.Path, from) && from != "" {
				sh.Path = to + strings.TrimPrefix(sh.Path, from)
			}
		}
		return nil
	})
}

// ---------- public ----------

// getShare loads a share and checks expiry and (optionally) password access.
func (s *Server) getShare(w http.ResponseWriter, r *http.Request, needUnlock bool) (*Share, bool) {
	id := r.PathValue("id")
	var sh *Share
	s.st.View(func(st *State) {
		if v, ok := st.Shares[id]; ok {
			c := *v
			sh = &c
		}
	})
	if sh == nil || sh.Expired(time.Now()) {
		jsonErr(w, 404, "this link does not exist or has expired")
		return nil, false
	}
	if needUnlock && !s.shareUnlocked(r, sh) {
		jsonErr(w, 401, "password required")
		return nil, false
	}
	return sh, true
}

func (s *Server) handlePubInfo(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.getShare(w, r, false)
	if !ok {
		return
	}
	unlocked := s.shareUnlocked(r, sh)
	out := map[string]any{
		"name":         shareName(sh),
		"is_dir":       sh.IsDir,
		"needs_pass":   !unlocked,
		"allow_upload": sh.AllowUpload && s.cfg.ShareUploads && !s.cfg.ReadOnly,
		"upload_only":  sh.UploadOnly,
		"title":        s.cfg.Title,
		"chunk_size":   s.cfg.ChunkSize,
		"has_password": sh.PassHash != "",
	}
	if !sh.Expires.IsZero() {
		out["expires"] = sh.Expires
	}
	if unlocked {
		out["note"] = sh.Note
		if !sh.IsDir {
			if st, err := s.root.Stat(fsPath(sh.Path)); err == nil {
				out["size"] = st.Size()
				out["mtime"] = st.ModTime()
			}
		}
		_ = s.st.Update(func(st *State) error {
			if v, ok := st.Shares[sh.ID]; ok {
				v.Views++
			}
			return nil
		})
	}
	writeJSON(w, out)
}

func shareName(sh *Share) string {
	if sh.Path == "" {
		return "Shared files"
	}
	return path.Base("/" + sh.Path)
}

func (s *Server) handlePubUnlock(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		jsonErr(w, http.StatusForbidden, "missing CSRF header")
		return
	}
	sh, ok := s.getShare(w, r, false)
	if !ok {
		return
	}
	key := s.clientIP(r) + "|" + sh.ID
	if s.shareLimit.Blocked(key) {
		jsonErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if sh.PassHash != "" && !verifySecret(sh.PassHash, req.Password) {
		s.shareLimit.Fail(key)
		time.Sleep(400 * time.Millisecond)
		jsonErr(w, 401, "wrong password")
		return
	}
	exp := time.Now().Add(24 * time.Hour)
	if !sh.Expires.IsZero() && sh.Expires.Before(exp) {
		exp = sh.Expires
	}
	http.SetCookie(w, &http.Cookie{
		Name: shareCookieName(sh.ID), Value: s.shareToken(sh, exp), Path: "/s/" + sh.ID,
		Expires: exp, HttpOnly: true, Secure: s.isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// subPath resolves a path inside a directory share.
func subPath(sh *Share, p string) (string, bool) {
	rel, err := joinRel(sh.Path, p)
	if err != nil || !isWithin(rel, sh.Path) {
		return "", false
	}
	return rel, true
}

func (s *Server) handlePubList(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.getShare(w, r, true)
	if !ok {
		return
	}
	if !sh.IsDir || sh.UploadOnly {
		jsonErr(w, 403, "listing not allowed")
		return
	}
	sub := r.URL.Query().Get("path")
	rel, ok := subPath(sh, sub)
	if !ok {
		jsonErr(w, 400, "invalid path")
		return
	}
	entries, err := s.listDir(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	shown, _ := cleanRel(sub)
	writeJSON(w, map[string]any{"path": shown, "entries": entries})
}

func (s *Server) countDownload(sh *Share) bool {
	allowed := true
	_ = s.st.Update(func(st *State) error {
		v, ok := st.Shares[sh.ID]
		if !ok {
			allowed = false
			return nil
		}
		if v.MaxDownload > 0 && v.Downloads >= v.MaxDownload {
			allowed = false
			return nil
		}
		v.Downloads++
		return nil
	})
	return allowed
}

func (s *Server) handlePubDownload(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.getShare(w, r, true)
	if !ok {
		return
	}
	if sh.UploadOnly {
		jsonErr(w, 403, "downloads not allowed")
		return
	}
	rel := sh.Path
	if sh.IsDir {
		if rel, ok = subPath(sh, r.URL.Query().Get("path")); !ok || rel == sh.Path {
			jsonErr(w, 400, "invalid path")
			return
		}
	}
	inline := r.URL.Query().Get("inline") == "1"
	// count only the first request of a (possibly ranged/resumed) download
	if rg := r.Header.Get("Range"); !inline && (rg == "" || strings.HasPrefix(rg, "bytes=0-")) {
		if !s.countDownload(sh) {
			jsonErr(w, 410, "download limit reached")
			return
		}
	}
	s.serveFile(w, r, rel, inline)
}

func (s *Server) handlePubZip(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.getShare(w, r, true)
	if !ok {
		return
	}
	if sh.UploadOnly || !sh.IsDir {
		jsonErr(w, 403, "not allowed")
		return
	}
	q := r.URL.Query()
	dir, ok := subPath(sh, q.Get("dir"))
	if !ok {
		jsonErr(w, 400, "invalid path")
		return
	}
	names := q["names"]
	zipName := shareName(sh) + ".zip"
	if len(names) == 0 {
		// whole folder
		if dir == sh.Path {
			entries, err := s.listDir(dir)
			if err != nil {
				fsErr(w, err)
				return
			}
			for _, e := range entries {
				names = append(names, e.Name)
			}
		} else {
			names = []string{path.Base("/" + dir)}
			dir = strings.TrimPrefix(path.Dir("/"+dir), "/")
			zipName = names[0] + ".zip"
		}
	} else if len(names) == 1 {
		zipName = names[0] + ".zip"
	}
	if !s.countDownload(sh) {
		jsonErr(w, 410, "download limit reached")
		return
	}
	s.streamZip(w, r, dir, names, zipName)
}

func (s *Server) handlePubText(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.getShare(w, r, true)
	if !ok {
		return
	}
	if sh.UploadOnly {
		jsonErr(w, 403, "not allowed")
		return
	}
	rel := sh.Path
	if sh.IsDir {
		if rel, ok = subPath(sh, r.URL.Query().Get("path")); !ok || rel == sh.Path {
			jsonErr(w, 400, "invalid path")
			return
		}
	}
	content, st, ok := s.readText(w, rel)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"content": content, "mtime": st.ModTime(), "size": st.Size()})
}

func (s *Server) handlePubUploadInit(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		jsonErr(w, http.StatusForbidden, "missing CSRF header")
		return
	}
	sh, ok := s.getShare(w, r, true)
	if !ok {
		return
	}
	if !sh.AllowUpload || !sh.IsDir || !s.cfg.ShareUploads || s.cfg.ReadOnly {
		jsonErr(w, 403, "uploads not allowed")
		return
	}
	var req initReq
	if !readJSON(w, r, &req) {
		return
	}
	if sh.UploadOnly {
		req.Dir = "" // drop box: always into the shared folder root
	}
	// public uploaders may never overwrite existing files
	s.initUpload(w, req, sh.Path, "share:"+sh.ID, false)
}

func (s *Server) handlePubUploadChunk(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		jsonErr(w, http.StatusForbidden, "missing CSRF header")
		return
	}
	sh, ok := s.getShare(w, r, true)
	if !ok {
		return
	}
	if !sh.AllowUpload || !s.cfg.ShareUploads || s.cfg.ReadOnly {
		jsonErr(w, 403, "uploads not allowed")
		return
	}
	s.writeChunk(w, r, r.PathValue("uid"), "share:"+sh.ID)
}
