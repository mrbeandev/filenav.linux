package main

// Resumable chunked uploads.
//
//  1. POST .../upload/init {dir, name, size, key}  -> {uid, offset, chunk_size}
//     "key" is a client fingerprint (e.g. name+size+lastModified); if a partial
//     upload with the same key exists the server returns its current offset so
//     the client resumes instead of starting over.
//  2. PUT .../upload/{uid}  header "Upload-Offset: N", body = next chunk.
//     The server appends and returns {offset, done, path}.
//
// Data is streamed straight to disk (never buffered in RAM), so file size is
// limited only by disk space. Partial files live in <root>/.filenav-uploads/,
// on the same filesystem, so finishing is an atomic rename.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type uploadMeta struct {
	UID       string    `json:"uid"`
	Key       string    `json:"key"`
	Dir       string    `json:"dir"`  // destination dir, root-relative
	Name      string    `json:"name"` // final file name (may include sub dirs for folder uploads)
	Size      int64     `json:"size"`
	Share     string    `json:"share,omitempty"` // share id for public uploads
	Overwrite bool      `json:"overwrite"`
	Updated   time.Time `json:"updated"`
}

func partPath(uid string) string { return path.Join(uploadTmpDir, uid+".part") }
func metaPath(uid string) string { return path.Join(uploadTmpDir, uid+".json") }

func (s *Server) loadUploads() {
	f, err := s.root.Open(uploadTmpDir)
	if err != nil {
		return
	}
	defer f.Close()
	names, _ := f.Readdirnames(-1)
	for _, n := range names {
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := s.root.ReadFile(path.Join(uploadTmpDir, n))
		if err != nil {
			continue
		}
		var m uploadMeta
		if json.Unmarshal(b, &m) == nil && m.UID != "" {
			s.uploads[m.UID] = &m
		}
	}
}

func (s *Server) cleanStaleUploads(maxAge time.Duration) int {
	s.upMu.Lock()
	defer s.upMu.Unlock()
	n := 0
	cut := time.Now().Add(-maxAge)
	for uid, m := range s.uploads {
		if m.Updated.Before(cut) {
			_ = s.root.Remove(partPath(uid))
			_ = s.root.Remove(metaPath(uid))
			delete(s.uploads, uid)
			delete(s.upLocks, uid)
			n++
		}
	}
	// remove orphaned temp files
	if f, err := s.root.Open(uploadTmpDir); err == nil {
		names, _ := f.Readdirnames(-1)
		f.Close()
		for _, name := range names {
			uid := strings.TrimSuffix(strings.TrimSuffix(name, ".part"), ".json")
			if _, ok := s.uploads[uid]; ok {
				continue
			}
			p := path.Join(uploadTmpDir, name)
			if st, err := s.root.Stat(p); err == nil && st.ModTime().Before(cut) {
				_ = s.root.Remove(p)
				n++
			}
		}
	}
	return n
}

func (s *Server) saveMeta(m *uploadMeta) error {
	b, _ := json.Marshal(m)
	return s.root.WriteFile(metaPath(m.UID), b, 0o600)
}

func (s *Server) uploadLock(uid string) *sync.Mutex {
	s.upMu.Lock()
	defer s.upMu.Unlock()
	l, ok := s.upLocks[uid]
	if !ok {
		l = &sync.Mutex{}
		s.upLocks[uid] = l
	}
	return l
}

type initReq struct {
	Dir       string `json:"dir"`
	Name      string `json:"name"` // may be "sub/dir/file.txt" for folder uploads
	Size      int64  `json:"size"`
	Key       string `json:"key"`
	Overwrite bool   `json:"overwrite"`
}

// validRelName checks a (possibly nested) relative file name from a folder upload.
func validRelName(n string) bool {
	if n == "" || len(n) > 4096 {
		return false
	}
	for _, part := range strings.Split(n, "/") {
		if !validName(part) {
			return false
		}
	}
	return true
}

// initUpload validates a request and returns an existing or new upload.
// baseDir is the directory the caller is allowed to write into.
func (s *Server) initUpload(w http.ResponseWriter, req initReq, baseDir, shareID string, allowOverwrite bool) {
	if req.Size < 0 || !validRelName(req.Name) {
		jsonErr(w, 400, "invalid name or size")
		return
	}
	dir, err := joinRel(baseDir, req.Dir)
	if err != nil || !isWithin(dir, baseDir) {
		jsonErr(w, 400, "invalid directory")
		return
	}
	if final, err := joinRel(dir, req.Name); err != nil || !isWithin(final, baseDir) {
		jsonErr(w, 400, "invalid name")
		return
	}
	if max := s.cfg.MaxUploadMB << 20; max > 0 && req.Size > max {
		jsonErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file is larger than the %d MB upload limit", s.cfg.MaxUploadMB))
		return
	}
	if free, _, ok := s.diskFree(); ok && uint64(req.Size) > free {
		jsonErr(w, http.StatusInsufficientStorage, "not enough disk space")
		return
	}
	overwrite := req.Overwrite && allowOverwrite
	// fingerprint scoped to destination + caller so different shares can't collide
	fp := sha256.Sum256([]byte(shareID + "\x00" + dir + "\x00" + req.Name + "\x00" + strconv.FormatInt(req.Size, 10) + "\x00" + req.Key))
	key := hex.EncodeToString(fp[:])

	s.upMu.Lock()
	var m *uploadMeta
	if req.Key != "" {
		for _, u := range s.uploads {
			if u.Key == key {
				m = u
				break
			}
		}
	}
	if m == nil {
		m = &uploadMeta{UID: randomToken(18), Key: key, Dir: dir, Name: req.Name, Size: req.Size,
			Share: shareID, Overwrite: overwrite, Updated: time.Now()}
		s.uploads[m.UID] = m
	}
	s.upMu.Unlock()

	l := s.uploadLock(m.UID)
	l.Lock()
	defer l.Unlock()
	var offset int64
	if st, err := s.root.Stat(partPath(m.UID)); err == nil {
		offset = st.Size()
	} else {
		f, err := s.root.OpenFile(partPath(m.UID), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fsErr(w, err)
			return
		}
		f.Close()
		if err := s.saveMeta(m); err != nil {
			fsErr(w, err)
			return
		}
	}
	if m.Size == 0 { // empty file: finish immediately
		p, err := s.finishUpload(m)
		if err != nil {
			fsErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"uid": m.UID, "offset": 0, "done": true, "path": p, "chunk_size": s.cfg.ChunkSize})
		return
	}
	writeJSON(w, map[string]any{"uid": m.UID, "offset": offset, "chunk_size": s.cfg.ChunkSize})
}

// writeChunk appends the request body at Upload-Offset.
func (s *Server) writeChunk(w http.ResponseWriter, r *http.Request, uid, shareID string) {
	s.upMu.Lock()
	m, ok := s.uploads[uid]
	s.upMu.Unlock()
	if !ok || m.Share != shareID {
		jsonErr(w, 404, "unknown upload (expired?) — start again")
		return
	}
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off < 0 {
		jsonErr(w, 400, "missing Upload-Offset header")
		return
	}
	l := s.uploadLock(uid)
	l.Lock()
	defer l.Unlock()

	f, err := s.root.OpenFile(partPath(uid), os.O_WRONLY, 0o600)
	if err != nil {
		fsErr(w, err)
		return
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		fsErr(w, err)
		return
	}
	cur := st.Size()
	if off != cur {
		f.Close()
		w.Header().Set("Upload-Offset", strconv.FormatInt(cur, 10))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "offset mismatch", "offset": cur})
		return
	}
	remaining := m.Size - cur
	limit := s.cfg.ChunkSize + 1<<20
	if remaining < limit {
		limit = remaining
	}
	if _, err := f.Seek(cur, io.SeekStart); err != nil {
		f.Close()
		fsErr(w, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	n, cerr := io.Copy(f, body)
	if cerr != nil {
		// keep only what was fully written; client will resume from there
		_ = f.Truncate(cur + n)
	}
	f.Close()
	cur += n
	m.Updated = time.Now()
	if cerr != nil {
		jsonErr(w, 400, "chunk interrupted: "+cerr.Error())
		return
	}
	if cur < m.Size {
		writeJSON(w, map[string]any{"offset": cur, "done": false})
		return
	}
	p, err := s.finishUpload(m)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"offset": cur, "done": true, "path": p})
}

// finishUpload moves the completed part file into place. Caller holds the upload lock.
func (s *Server) finishUpload(m *uploadMeta) (string, error) {
	final, err := joinRel(m.Dir, m.Name)
	if err != nil {
		return "", err
	}
	if d := path.Dir(final); d != "." && d != "" {
		if err := s.root.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}
	if st, err := s.root.Lstat(final); err == nil {
		if m.Overwrite && !st.IsDir() {
			// rename replaces atomically
		} else {
			final = s.uniqueName(final)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := s.root.Rename(partPath(m.UID), final); err != nil {
		return "", err
	}
	_ = s.root.Chmod(final, 0o644)
	_ = s.root.Remove(metaPath(m.UID))
	s.upMu.Lock()
	delete(s.uploads, m.UID)
	delete(s.upLocks, m.UID)
	s.upMu.Unlock()
	return final, nil
}

func (s *Server) cancelUpload(uid, shareID string) {
	s.upMu.Lock()
	m, ok := s.uploads[uid]
	if ok && m.Share == shareID {
		delete(s.uploads, uid)
	}
	s.upMu.Unlock()
	if ok && m.Share == shareID {
		l := s.uploadLock(uid)
		l.Lock()
		_ = s.root.Remove(partPath(uid))
		_ = s.root.Remove(metaPath(uid))
		l.Unlock()
		s.upMu.Lock()
		delete(s.upLocks, uid)
		s.upMu.Unlock()
	}
}

// ---------- admin handlers ----------

func (s *Server) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	var req initReq
	if !readJSON(w, r, &req) {
		return
	}
	s.initUpload(w, req, "", "", true)
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	s.writeChunk(w, r, r.PathValue("uid"), "")
}

func (s *Server) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	s.cancelUpload(r.PathValue("uid"), "")
	writeJSON(w, map[string]any{"ok": true})
}
