package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const uploadTmpDir = ".filenav-uploads"

var errBadPath = errors.New("invalid path")

// cleanRel normalises a user supplied path to a root-relative path.
// "" means the root itself. The internal upload directory is never reachable.
func cleanRel(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", errBadPath
	}
	c := strings.TrimPrefix(path.Clean("/"+p), "/")
	if c == uploadTmpDir || strings.HasPrefix(c, uploadTmpDir+"/") {
		return "", errBadPath
	}
	return c, nil
}

// joinRel joins base and a user-supplied sub path, guaranteeing the result
// stays within base.
func joinRel(base, sub string) (string, error) {
	c, err := cleanRel(sub)
	if err != nil {
		return "", err
	}
	j := strings.TrimPrefix(path.Join("/"+base, c), "/")
	return cleanRel(j)
}

func validName(n string) bool {
	return n != "" && n != "." && n != ".." && len(n) <= 255 &&
		!strings.ContainsAny(n, "/\x00") && utf8.ValidString(n)
}

// fsPath converts a root-relative path to the name os.Root expects.
func fsPath(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func isWithin(child, parent string) bool {
	if parent == "" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

// ---------- listing ----------

type Entry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Link    bool      `json:"link,omitempty"`
}

func (s *Server) listDir(rel string) ([]Entry, error) {
	f, err := s.root.Open(fsPath(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		if rel == "" && de.Name() == uploadTmpDir {
			continue
		}
		e := Entry{Name: de.Name(), IsDir: de.IsDir()}
		if de.Type()&fs.ModeSymlink != 0 {
			e.Link = true
			st, err := s.root.Stat(path.Join(fsPath(rel), de.Name()))
			if err != nil {
				continue // dangling or escaping link: hide
			}
			e.IsDir, e.Size, e.ModTime = st.IsDir(), st.Size(), st.ModTime()
		} else if info, err := de.Info(); err == nil {
			e.Size, e.ModTime = info.Size(), info.ModTime()
		}
		if e.IsDir {
			e.Size = 0
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		fsErr(w, err)
		return
	}
	entries, err := s.listDir(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": rel, "entries": entries})
}

// ---------- downloads ----------

// inlineSafe returns a content type that is safe to render inline on our origin.
func inlineSafe(name string) (ctype string, sandbox bool) {
	ct := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	base, _, _ := strings.Cut(ct, ";")
	switch {
	case base == "image/svg+xml":
		return ct, true
	case strings.HasPrefix(base, "image/"), strings.HasPrefix(base, "video/"),
		strings.HasPrefix(base, "audio/"), base == "application/pdf":
		return ct, false
	}
	return "text/plain; charset=utf-8", true
}

func contentDisposition(kind, name string) string {
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}

// serveFile streams a file with full HTTP Range / resume support.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, rel string, inline bool) {
	f, err := s.root.Open(fsPath(rel))
	if err != nil {
		fsErr(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fsErr(w, err)
		return
	}
	if st.IsDir() {
		jsonErr(w, 400, "is a directory")
		return
	}
	name := path.Base("/" + rel)
	h := w.Header()
	if inline {
		ct, sandbox := inlineSafe(name)
		h.Set("Content-Type", ct)
		if sandbox {
			h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		}
		h.Set("Content-Disposition", contentDisposition("inline", name))
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", contentDisposition("attachment", name))
	}
	h.Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	s.serveFile(w, r, rel, r.URL.Query().Get("inline") == "1")
}

// streamZip writes the given entries (relative to dir) as an uncompressed
// (store) zip — almost no CPU, and works for any size thanks to zip64.
func (s *Server) streamZip(w http.ResponseWriter, r *http.Request, dir string, names []string, zipName string) {
	for _, n := range names {
		if !validName(n) {
			jsonErr(w, 400, "invalid name")
			return
		}
		if dir == "" && n == uploadTmpDir {
			jsonErr(w, 400, "invalid name")
			return
		}
		if _, err := s.root.Stat(path.Join(fsPath(dir), n)); err != nil {
			fsErr(w, err)
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", contentDisposition("attachment", zipName))
	h.Set("Cache-Control", "no-store")
	zw := newZipStream(w)
	defer zw.Close()
	ctx := r.Context()
	rfs := s.root.FS()
	for _, n := range names {
		start := path.Join(fsPath(dir), n)
		_ = fs.WalkDir(rfs, start, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil // skip unreadable
			}
			rel, _ := filepath.Rel(fsPath(dir), p)
			rel = filepath.ToSlash(rel)
			if p == uploadTmpDir {
				return fs.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				info, err = s.root.Stat(p) // follow links that stay inside root
				if err != nil || info.IsDir() {
					return nil
				}
			}
			if info.IsDir() {
				_, err := zw.Create(rel+"/", info.ModTime(), true)
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			fw, err := zw.Create(rel, info.ModTime(), false)
			if err != nil {
				return err
			}
			f, err := s.root.Open(p)
			if err != nil {
				return nil
			}
			_, err = io.Copy(fw, f)
			f.Close()
			return err
		})
	}
}

func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir, err := cleanRel(q.Get("dir"))
	if err != nil {
		fsErr(w, err)
		return
	}
	names := q["names"]
	if len(names) == 0 {
		jsonErr(w, 400, "nothing selected")
		return
	}
	zipName := "download.zip"
	if len(names) == 1 {
		zipName = names[0] + ".zip"
	} else if dir != "" {
		zipName = path.Base(dir) + ".zip"
	}
	s.streamZip(w, r, dir, names, zipName)
}

// ---------- search ----------

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	base, err := cleanRel(q.Get("path"))
	if err != nil {
		fsErr(w, err)
		return
	}
	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))
	if needle == "" {
		jsonErr(w, 400, "empty query")
		return
	}
	type hit struct {
		Entry
		Path string `json:"path"`
	}
	hits := []hit{}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	visited := 0
	truncated := false
	_ = fs.WalkDir(s.root.FS(), fsPath(base), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			truncated = true
			return fs.SkipAll
		}
		if p == uploadTmpDir {
			return fs.SkipDir
		}
		visited++
		if visited > 500_000 || len(hits) >= 500 {
			truncated = true
			return fs.SkipAll
		}
		if p == fsPath(base) {
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), needle) {
			e := Entry{Name: d.Name(), IsDir: d.IsDir()}
			if info, err := d.Info(); err == nil {
				e.ModTime = info.ModTime()
				if !d.IsDir() {
					e.Size = info.Size()
				}
			}
			hits = append(hits, hit{e, p})
		}
		return nil
	})
	writeJSON(w, map[string]any{"results": hits, "truncated": truncated})
}

// ---------- text editing ----------

const maxTextEdit = 5 << 20

func (s *Server) handleTextGet(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	st, err := s.root.Stat(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	if st.IsDir() || st.Size() > maxTextEdit {
		jsonErr(w, 400, "not an editable file (max 5 MiB)")
		return
	}
	b, err := s.root.ReadFile(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	if !utf8.Valid(b) {
		jsonErr(w, 400, "file is not UTF-8 text")
		return
	}
	writeJSON(w, map[string]any{"content": string(b), "mtime": st.ModTime()})
}

func (s *Server) handleTextPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTextEdit+(1<<20))
	if !readJSON(w, r, &req) {
		return
	}
	rel, err := cleanRel(req.Path)
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	mode := os.FileMode(0o644)
	if st, err := s.root.Stat(rel); err == nil {
		if st.IsDir() {
			jsonErr(w, 400, "is a directory")
			return
		}
		mode = st.Mode().Perm()
	}
	// write to temp + rename so a crash never leaves a half-written file
	tmp := path.Join(uploadTmpDir, "edit-"+randomToken(8))
	if err := s.root.WriteFile(tmp, []byte(req.Content), mode); err != nil {
		fsErr(w, err)
		return
	}
	if err := s.root.Rename(tmp, rel); err != nil {
		_ = s.root.Remove(tmp)
		fsErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- mutations ----------

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
		File bool   `json:"file"` // create an empty file instead
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validName(req.Name) {
		jsonErr(w, 400, "invalid name")
		return
	}
	rel, err := joinRel(req.Path, req.Name)
	if err != nil {
		fsErr(w, err)
		return
	}
	if req.File {
		f, err := s.root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			fsErr(w, err)
			return
		}
		f.Close()
	} else if err := s.root.Mkdir(rel, 0o755); err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": rel})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	src, err := cleanRel(req.Path)
	if err != nil || src == "" || !validName(req.Name) {
		jsonErr(w, 400, "invalid path or name")
		return
	}
	dst, err := joinRel(path.Dir("/"+src), req.Name)
	if err != nil {
		fsErr(w, err)
		return
	}
	if dst == src {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if _, err := s.root.Lstat(dst); err == nil && !strings.EqualFold(dst, src) {
		jsonErr(w, 409, "a file with that name already exists")
		return
	}
	if err := s.root.Rename(src, dst); err != nil {
		fsErr(w, err)
		return
	}
	s.repointShares(src, dst)
	writeJSON(w, map[string]any{"ok": true, "path": dst})
}

func (s *Server) handleMoveCopy(isCopy bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paths []string `json:"paths"`
			Dest  string   `json:"dest"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		dest, err := cleanRel(req.Dest)
		if err != nil {
			fsErr(w, err)
			return
		}
		if st, err := s.root.Stat(fsPath(dest)); err != nil || !st.IsDir() {
			jsonErr(w, 400, "destination is not a folder")
			return
		}
		var errs []string
		for _, p := range req.Paths {
			src, err := cleanRel(p)
			if err != nil || src == "" {
				errs = append(errs, p+": invalid path")
				continue
			}
			if isWithin(dest, src) {
				errs = append(errs, p+": cannot move/copy a folder into itself")
				continue
			}
			name := path.Base("/" + src)
			target := strings.TrimPrefix(path.Join("/"+dest, name), "/")
			if target == src {
				if !isCopy {
					continue
				}
			}
			target = s.uniqueName(target)
			if isCopy {
				err = s.copyTree(r.Context(), src, target)
			} else {
				err = s.root.Rename(src, target)
				if err != nil && errors.Is(err, syscall.EXDEV) {
					// different filesystem: copy then delete
					if err = s.copyTree(r.Context(), src, target); err == nil {
						err = s.root.RemoveAll(src)
					}
				}
				if err == nil {
					s.repointShares(src, target)
				}
			}
			if err != nil {
				errs = append(errs, p+": "+errString(err))
			}
		}
		if len(errs) > 0 {
			jsonErr(w, 500, strings.Join(errs, "\n"))
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}

func errString(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	return err.Error()
}

func (s *Server) copyTree(ctx context.Context, src, dst string) error {
	st, err := s.root.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return s.copyFile(src, dst, st.Mode().Perm())
	}
	return fs.WalkDir(s.root.FS(), src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, src), "/")
		target := path.Join(dst, rel)
		info, err := s.root.Stat(p)
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if d.Type()&fs.ModeSymlink != 0 {
				return nil // don't follow directory symlinks (loops)
			}
			return s.root.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return s.copyFile(p, target, info.Mode().Perm())
	})
}

func (s *Server) copyFile(src, dst string, mode os.FileMode) error {
	in, err := s.root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := s.root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil { // uses copy_file_range on Linux
		out.Close()
		_ = s.root.Remove(dst)
		return err
	}
	return out.Close()
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var errs []string
	for _, p := range req.Paths {
		rel, err := cleanRel(p)
		if err != nil || rel == "" {
			errs = append(errs, p+": invalid path")
			continue
		}
		if err := s.root.RemoveAll(rel); err != nil {
			errs = append(errs, p+": "+errString(err))
		}
	}
	if len(errs) > 0 {
		jsonErr(w, 500, strings.Join(errs, "\n"))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// uniqueName returns rel, or "name (n).ext" if rel already exists.
func (s *Server) uniqueName(rel string) string {
	if _, err := s.root.Lstat(rel); errors.Is(err, fs.ErrNotExist) {
		return rel
	}
	dir, file := path.Split(rel)
	ext := path.Ext(file)
	base := strings.TrimSuffix(file, ext)
	if ext == file { // dotfile like ".bashrc"
		base, ext = file, ""
	}
	for i := 1; i < 10000; i++ {
		c := fmt.Sprintf("%s%s (%d)%s", dir, base, i, ext)
		if _, err := s.root.Lstat(c); errors.Is(err, fs.ErrNotExist) {
			return c
		}
	}
	return fmt.Sprintf("%s%s-%s%s", dir, base, randomToken(4), ext)
}

// ---------- disk usage ----------

func (s *Server) diskFree() (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.absRoot, &st); err != nil {
		return 0, 0, false
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), true
}

func (s *Server) handleDisk(w http.ResponseWriter, r *http.Request) {
	free, total, ok := s.diskFree()
	if !ok {
		jsonErr(w, 500, "statfs failed")
		return
	}
	writeJSON(w, map[string]any{"free": free, "total": total})
}
