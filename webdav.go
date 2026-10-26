package main

// WebDAV access for share links, so rclone (and file managers, curl) can use
// a share like a folder. Each share gets a Nextcloud-shaped address because
// that's the only WebDAV vendor rclone uploads to in pieces:
//
//	/s/{id}/dav/files/share/...   PROPFIND, GET, HEAD, PUT, MKCOL
//	/s/{id}/dav/uploads/share/... MKCOL, PUT piece, MOVE .file, DELETE
//
// Pieced uploads keep every request under Cloudflare's 100 MB body limit.
// Login is HTTP Basic auth (any user name, the share password); shares
// without a password need none. The share's rules apply exactly as on the
// web page: read-only shares can't upload, drop boxes can't list or
// download, and visitors can never delete, rename or overwrite files.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	davUser     = "share"
	davMaxPiece = 512 << 20 // refuse absurd single pieces
)

type davCtx struct {
	s   *Server
	sh  *Share
	id  string
	sub string // path below the share root ("" = root), already cleaned
}

// handleDAV is the entry point for /s/{id}/dav/{kind}/share/{path...}.
func (s *Server) handleDAV(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Shares {
		http.Error(w, "sharing is disabled", http.StatusNotFound)
		return
	}
	id := r.PathValue("id")
	var sh *Share
	s.st.View(func(st *State) {
		if v, ok := st.Shares[id]; ok {
			c := *v
			sh = &c
		}
	})
	if sh == nil || sh.Expired(time.Now()) {
		http.Error(w, "this link does not exist or has expired", http.StatusNotFound)
		return
	}
	if !s.davAuth(w, r, sh) {
		return
	}
	rest := r.PathValue("rest")
	kind, sub, _ := strings.Cut(rest, "/")
	if sub != davUser && !strings.HasPrefix(sub, davUser+"/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	sub = strings.TrimPrefix(strings.TrimPrefix(sub, davUser), "/")
	clean, err := cleanRel(sub)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	c := &davCtx{s: s, sh: sh, id: id, sub: clean}
	w.Header().Set("DAV", "1")
	switch kind {
	case "files":
		c.files(w, r)
	case "uploads":
		c.uploads(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// davAuth checks the share password via Basic auth, with the same attempt
// limiter as the web unlock form.
func (s *Server) davAuth(w http.ResponseWriter, r *http.Request, sh *Share) bool {
	if sh.PassHash == "" || s.shareUnlocked(r, sh) {
		return true
	}
	key := s.clientIP(r) + "|" + sh.ID
	if s.shareLimit.Blocked(key) {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return false
	}
	_, pass, ok := r.BasicAuth()
	if ok && s.davPassOK(sh, pass) {
		return true
	}
	if ok {
		s.shareLimit.Fail(key)
		time.Sleep(400 * time.Millisecond)
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="filenav share", charset="UTF-8"`)
	http.Error(w, "password required", http.StatusUnauthorized)
	return false
}

// davPassOK verifies the password, caching successes briefly: rclone sends
// Basic auth on every request and PBKDF2 is deliberately slow.
var davPassCache sync.Map // sha256(id|hash|pass) -> expiry

func (s *Server) davPassOK(sh *Share, pass string) bool {
	sum := sha256.Sum256([]byte(sh.ID + "\x00" + sh.PassHash + "\x00" + pass))
	k := hex.EncodeToString(sum[:])
	if exp, ok := davPassCache.Load(k); ok && time.Now().Before(exp.(time.Time)) {
		return true
	}
	if !verifySecret(sh.PassHash, pass) {
		return false
	}
	davPassCache.Store(k, time.Now().Add(10*time.Minute))
	return true
}

func davPassGC() {
	now := time.Now()
	davPassCache.Range(func(k, v any) bool {
		if now.After(v.(time.Time)) {
			davPassCache.Delete(k)
		}
		return true
	})
}

func (c *davCtx) canUpload() bool {
	return c.sh.IsDir && c.sh.AllowUpload && c.s.cfg.ShareUploads && !c.s.cfg.ReadOnly
}

// rel maps a share sub-path to a root-relative path (inside the share).
func (c *davCtx) rel(sub string) (string, bool) {
	if !c.sh.IsDir {
		// a file share exposes a folder containing just that file
		if sub == "" {
			return path.Dir(c.sh.Path), true
		}
		if sub != path.Base(c.sh.Path) {
			return "", false
		}
		return c.sh.Path, true
	}
	return subPath(c.sh, sub)
}

func (c *davCtx) files(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, PUT, MKCOL, PATCH")
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		c.propfind(w, r)
	case http.MethodGet, http.MethodHead:
		c.get(w, r)
	case http.MethodPut:
		c.put(w, r)
	case "MKCOL":
		c.mkcol(w, r)
	case "PROPPATCH":
		// modification times are set by X-OC-Mtime on upload; accept and ignore
		c.proppatch(w, r)
	case http.MethodPatch:
		c.recalcHash(w, r)
	case http.MethodDelete, "MOVE", "COPY":
		http.Error(w, "share visitors can't delete, move or copy files", http.StatusForbidden)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, PUT, MKCOL")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- PROPFIND ----------

type davResp struct {
	XMLName  xml.Name `xml:"d:response"`
	Href     string   `xml:"d:href"`
	Propstat struct {
		Prop struct {
			Name     string `xml:"d:displayname,omitempty"`
			Modified string `xml:"d:getlastmodified,omitempty"`
			Length   *int64 `xml:"d:getcontentlength,omitempty"`
			Type     *struct {
				Coll *struct{} `xml:"d:collection,omitempty"`
			} `xml:"d:resourcetype"`
			CType string `xml:"d:getcontenttype,omitempty"`
			ETag  string `xml:"d:getetag,omitempty"`
			Sums  *struct {
				Sum string `xml:"oc:checksum"`
			} `xml:"oc:checksums,omitempty"`
		} `xml:"d:prop"`
		Status string `xml:"d:status"`
	} `xml:"d:propstat"`
}

func (c *davCtx) href(sub string, dir bool) string {
	p := "/s/" + c.id + "/dav/files/" + davUser + "/"
	if sub != "" {
		parts := strings.Split(sub, "/")
		for i, s := range parts {
			parts[i] = url.PathEscape(s)
		}
		p += strings.Join(parts, "/")
		if dir {
			p += "/"
		}
	}
	return p
}

func (c *davCtx) entry(sub string, st fs.FileInfo) davResp {
	var e davResp
	e.Href = c.href(sub, st.IsDir())
	e.Propstat.Status = "HTTP/1.1 200 OK"
	p := &e.Propstat.Prop
	p.Name = st.Name()
	p.Modified = st.ModTime().UTC().Format(http.TimeFormat)
	p.Type = &struct {
		Coll *struct{} `xml:"d:collection,omitempty"`
	}{}
	if st.IsDir() {
		p.Type.Coll = &struct{}{}
	} else {
		n := st.Size()
		p.Length = &n
		p.CType = mimeFor(st.Name())
		p.ETag = fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size())
		if sum := cachedSHA1(c.id, sub, st); sum != "" {
			p.Sums = &struct {
				Sum string `xml:"oc:checksum"`
			}{"SHA1:" + sum}
		}
	}
	return e
}

// SHA1 checksums of files uploaded or hashed through WebDAV, keyed by
// share+path and invalidated by size/mtime. Kept in memory only: hashing every
// file on listing would be far too expensive on a small server.
var davSums sync.Map // key -> davSum

type davSum struct {
	size  int64
	mtime int64
	sha1  string
}

func sumKey(id, sub string) string { return id + "\x00" + sub }

func cachedSHA1(id, sub string, st fs.FileInfo) string {
	if v, ok := davSums.Load(sumKey(id, sub)); ok {
		d := v.(davSum)
		if d.size == st.Size() && d.mtime == st.ModTime().UnixNano() {
			return d.sha1
		}
	}
	return ""
}

func (c *davCtx) storeSHA1(sub, rel, sum string) {
	if st, err := c.s.root.Stat(fsPath(rel)); err == nil {
		davSums.Store(sumKey(c.id, sub), davSum{st.Size(), st.ModTime().UnixNano(), sum})
	}
}

func (c *davCtx) propfind(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<16)) // we always answer with the same props
	rel, ok := c.rel(c.sub)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if c.sh.UploadOnly && c.sub != "" {
		rel, ok = subPath(c.sh, path.Base(c.sub)) // drop box is flat
	}
	st, err := c.s.root.Stat(fsPath(rel))
	if err != nil {
		davFsErr(w, err)
		return
	}
	isRoot := c.sub == ""
	if c.sh.UploadOnly && !isRoot && r.Header.Get("Depth") != "0" {
		http.Error(w, "this share only accepts uploads", http.StatusForbidden)
		return
	}
	var out []davResp
	self := c.entry(c.sub, st)
	if isRoot {
		self.Propstat.Prop.Name = shareName(c.sh)
	}
	out = append(out, self)
	depth := r.Header.Get("Depth")
	if st.IsDir() && depth != "0" && !c.sh.UploadOnly {
		if depth == "infinity" {
			http.Error(w, "Depth: infinity is not supported", http.StatusForbidden)
			return
		}
		ents, err := c.s.listDir(rel)
		if err != nil {
			davFsErr(w, err)
			return
		}
		for _, e := range ents {
			sub := path.Join(c.sub, e.Name)
			if !c.sh.IsDir && e.Name != path.Base(c.sh.Path) {
				continue
			}
			est, err := c.s.root.Stat(path.Join(fsPath(rel), e.Name))
			if err != nil {
				continue
			}
			out = append(out, c.entry(strings.TrimPrefix(sub, "/"), est))
		}
	}
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, xml.Header+`<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	enc := xml.NewEncoder(w)
	for _, e := range out {
		_ = enc.Encode(e)
	}
	_, _ = io.WriteString(w, "</d:multistatus>\n")
}

func (c *davCtx) proppatch(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<16))
	if !c.canUpload() {
		http.Error(w, "read-only share", http.StatusForbidden)
		return
	}
	rel, ok := c.rel(c.sub)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	st, err := c.s.root.Stat(fsPath(rel))
	if err != nil {
		davFsErr(w, err)
		return
	}
	resp := c.entry(c.sub, st)
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, xml.Header+`<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	_ = xml.NewEncoder(w).Encode(resp)
	_, _ = io.WriteString(w, "</d:multistatus>\n")
}

// recalcHash answers Nextcloud's "PATCH + X-Recalculate-Hash" which rclone
// sends after an upload to get a checksum it can verify.
func (c *davCtx) recalcHash(w http.ResponseWriter, r *http.Request) {
	algo := strings.ToLower(r.Header.Get("X-Recalculate-Hash"))
	if algo != "sha1" && algo != "md5" {
		http.Error(w, "unsupported", http.StatusNotImplemented)
		return
	}
	sub := c.sub
	if c.sh.UploadOnly {
		sub = path.Base(sub)
	}
	rel, ok := c.rel(sub)
	if !ok || sub == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := c.s.root.Open(fsPath(rel))
	if err != nil {
		davFsErr(w, err)
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() {
		http.Error(w, "not a file", http.StatusBadRequest)
		return
	}
	var h hash.Hash = sha1.New()
	name := "SHA1"
	if algo == "md5" {
		h, name = md5.New(), "MD5"
	}
	if _, err := io.Copy(h, f); err != nil {
		davFsErr(w, err)
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if name == "SHA1" {
		c.storeSHA1(sub, rel, sum)
	}
	w.Header().Set("OC-Checksum", name+":"+sum)
	w.WriteHeader(http.StatusOK)
}

// ---------- GET ----------

func (c *davCtx) get(w http.ResponseWriter, r *http.Request) {
	if c.sh.UploadOnly {
		http.Error(w, "this share only accepts uploads", http.StatusForbidden)
		return
	}
	rel, ok := c.rel(c.sub)
	if !ok || c.sub == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	st, err := c.s.root.Stat(fsPath(rel))
	if err != nil {
		davFsErr(w, err)
		return
	}
	if st.IsDir() {
		http.Error(w, "is a folder", http.StatusMethodNotAllowed)
		return
	}
	// count only the first request of a (possibly ranged/resumed) download
	if rg := r.Header.Get("Range"); r.Method == http.MethodGet && (rg == "" || strings.HasPrefix(rg, "bytes=0-")) {
		if !c.s.countDownload(c.sh) {
			http.Error(w, "download limit reached", http.StatusGone)
			return
		}
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size()))
	c.s.serveFile(w, r, rel, false)
}

// ---------- uploads ----------

// destFor returns where an upload to sub should go, refusing to overwrite.
func (c *davCtx) destFor(sub string) (string, int, string) {
	if !c.canUpload() {
		return "", http.StatusForbidden, "this share doesn't accept uploads"
	}
	if sub == "" {
		return "", http.StatusMethodNotAllowed, "can't upload to the share root itself"
	}
	if c.sh.UploadOnly {
		sub = path.Base(sub) // drop box: always into the shared folder root
	}
	rel, ok := subPath(c.sh, sub)
	if !ok || rel == c.sh.Path || isWithin(rel, uploadTmpDir) || strings.HasPrefix(path.Base(rel), ".filenav") {
		return "", http.StatusBadRequest, "invalid path"
	}
	if st, err := c.s.root.Lstat(fsPath(rel)); err == nil {
		if st.IsDir() {
			return "", http.StatusMethodNotAllowed, "a folder with that name exists"
		}
		// rclone uploads only files that differ; never replace an existing one
		return "", http.StatusPreconditionFailed, "a file with that name already exists (share visitors can't overwrite)"
	}
	return rel, 0, ""
}

func (c *davCtx) checkSize(n int64) (int, string) {
	if max := c.s.cfg.MaxUploadMB << 20; max > 0 && n > max {
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("file is larger than the %d MB upload limit", c.s.cfg.MaxUploadMB)
	}
	if free, _, ok := c.s.diskFree(); ok && n > 0 && uint64(n) > free {
		return http.StatusInsufficientStorage, "not enough free space on the server"
	}
	return 0, ""
}

func (c *davCtx) put(w http.ResponseWriter, r *http.Request) {
	rel, code, msg := c.destFor(c.sub)
	if code != 0 {
		http.Error(w, msg, code)
		return
	}
	if code, msg := c.checkSize(r.ContentLength); code != 0 {
		http.Error(w, msg, code)
		return
	}
	tmp, err := c.writeTemp(r.Body, r.ContentLength)
	if err != nil {
		davFsErr(w, err)
		return
	}
	if err := c.place(tmp, rel, r.Header.Get("X-OC-Mtime")); err != nil {
		_ = c.s.root.Remove(tmp)
		davFsErr(w, err)
		return
	}
	w.Header().Set("X-OC-Mtime", "accepted")
	w.WriteHeader(http.StatusCreated)
}

// writeTemp streams a body into the upload temp folder (never the final path,
// so a broken connection leaves no half-written file behind).
func (c *davCtx) writeTemp(body io.Reader, size int64) (string, error) {
	if err := c.s.root.MkdirAll(uploadTmpDir, 0o700); err != nil {
		return "", err
	}
	tmp := path.Join(uploadTmpDir, "dav-"+randomToken(12)+".part")
	f, err := c.s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	limit := int64(davMaxPiece) * 64
	if max := c.s.cfg.MaxUploadMB << 20; max > 0 {
		limit = max
	}
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("upload too large")
	}
	if err == nil && size >= 0 && n != size {
		err = fmt.Errorf("upload incomplete: got %d of %d bytes", n, size)
	}
	if err != nil {
		_ = c.s.root.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// place moves a finished temp file to rel (creating parent folders) and sets mtime.
func (c *davCtx) place(tmp, rel, mtime string) error {
	if d := path.Dir(rel); d != "." && d != "" {
		if err := c.s.root.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if _, err := c.s.root.Lstat(rel); err == nil {
		return fs.ErrExist
	}
	if err := c.s.root.Rename(tmp, rel); err != nil {
		return err
	}
	_ = c.s.root.Chmod(rel, 0o644)
	if t, err := strconv.ParseInt(mtime, 10, 64); err == nil && t > 0 {
		mt := time.Unix(t, 0)
		_ = c.s.root.Chtimes(rel, mt, mt)
	}
	return nil
}

func (c *davCtx) mkcol(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<16))
	if !c.canUpload() {
		http.Error(w, "this share doesn't accept uploads", http.StatusForbidden)
		return
	}
	if c.sh.UploadOnly {
		// drop box: everything lands in the root; pretend folders exist
		w.WriteHeader(http.StatusCreated)
		return
	}
	rel, ok := subPath(c.sh, c.sub)
	if !ok || rel == c.sh.Path {
		http.Error(w, "already exists", http.StatusMethodNotAllowed)
		return
	}
	if st, err := c.s.root.Lstat(fsPath(rel)); err == nil {
		if st.IsDir() {
			http.Error(w, "already exists", http.StatusMethodNotAllowed)
		} else {
			http.Error(w, "a file with that name exists", http.StatusConflict)
		}
		return
	}
	if parent := path.Dir(rel); parent != "." && parent != "" {
		if st, err := c.s.root.Stat(fsPath(parent)); err != nil || !st.IsDir() {
			http.Error(w, "parent folder missing", http.StatusConflict)
			return
		}
	}
	if err := c.s.root.Mkdir(rel, 0o755); err != nil {
		davFsErr(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// ---------- Nextcloud chunked uploads ----------
// MKCOL  uploads/share/<dir>/            start
// PUT    uploads/share/<dir>/<start>-<end>  one piece (any name works)
// MOVE   uploads/share/<dir>/.file  Destination: .../files/share/<path>  assemble
// DELETE uploads/share/<dir>/            abort / clean

func (c *davCtx) chunkDir(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." || len(name) > 200 {
		return "", false
	}
	sum := sha256.Sum256([]byte(c.id + "\x00" + name))
	return path.Join(uploadTmpDir, "dav-chunks-"+hex.EncodeToString(sum[:12])), true
}

func (c *davCtx) uploads(w http.ResponseWriter, r *http.Request) {
	if !c.canUpload() {
		http.Error(w, "this share doesn't accept uploads", http.StatusForbidden)
		return
	}
	dirName, piece, _ := strings.Cut(c.sub, "/")
	dir, ok := c.chunkDir(dirName)
	if !ok {
		http.Error(w, "invalid upload folder", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == "MKCOL" && piece == "":
		_ = c.s.root.RemoveAll(dir) // a previous attempt may have left pieces
		if err := c.s.root.MkdirAll(dir, 0o700); err != nil {
			davFsErr(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete && piece == "":
		if _, err := c.s.root.Stat(dir); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = c.s.root.RemoveAll(dir)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && piece != "" && !strings.Contains(piece, "/"):
		c.putPiece(w, r, dir, piece)
	case r.Method == "MOVE" && piece == ".file":
		c.assemble(w, r, dir)
	case r.Method == "PROPFIND":
		if _, err := c.s.root.Stat(dir); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, xml.Header+`<d:multistatus xmlns:d="DAV:"></d:multistatus>`)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// piece names: "<start>-<end>" (zero padded, inclusive) as rclone sends them;
// plain numbers (Nextcloud v1 style) are accepted too and ordered numerically.
func pieceStart(name string) (int64, bool) {
	a, _, ok := strings.Cut(name, "-")
	if !ok {
		a = name
	}
	n, err := strconv.ParseInt(a, 10, 64)
	return n, err == nil && n >= 0
}

func (c *davCtx) putPiece(w http.ResponseWriter, r *http.Request, dir, piece string) {
	if _, ok := pieceStart(piece); !ok {
		http.Error(w, "invalid piece name", http.StatusBadRequest)
		return
	}
	if _, err := c.s.root.Stat(dir); err != nil {
		http.Error(w, "upload not started (MKCOL first)", http.StatusConflict)
		return
	}
	if r.ContentLength > davMaxPiece {
		http.Error(w, "piece too large", http.StatusRequestEntityTooLarge)
		return
	}
	if code, msg := c.checkSize(r.ContentLength); code != 0 {
		http.Error(w, msg, code)
		return
	}
	tmp := path.Join(dir, piece+".tmp")
	f, err := c.s.root.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		davFsErr(w, err)
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, davMaxPiece+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && (n > davMaxPiece || (r.ContentLength >= 0 && n != r.ContentLength)) {
		err = errors.New("piece incomplete")
	}
	if err != nil {
		_ = c.s.root.Remove(tmp)
		davFsErr(w, err)
		return
	}
	if err := c.s.root.Rename(tmp, path.Join(dir, piece)); err != nil {
		davFsErr(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (c *davCtx) assemble(w http.ResponseWriter, r *http.Request, dir string) {
	u, err := url.Parse(r.Header.Get("Destination"))
	if err != nil {
		http.Error(w, "bad Destination", http.StatusBadRequest)
		return
	}
	prefix := "/s/" + c.id + "/dav/files/" + davUser + "/"
	if !strings.HasPrefix(u.Path, prefix) {
		http.Error(w, "Destination must be inside this share", http.StatusBadGateway)
		return
	}
	destSub, err := cleanRel(strings.TrimPrefix(u.Path, prefix))
	if err != nil {
		http.Error(w, "invalid Destination", http.StatusBadRequest)
		return
	}
	rel, code, msg := c.destFor(destSub)
	if code != 0 {
		http.Error(w, msg, code)
		return
	}
	des, err := rootReadDir(c.s.root, dir)
	if err != nil {
		http.Error(w, "upload not found", http.StatusNotFound)
		return
	}
	type pc struct {
		name  string
		start int64
	}
	var pieces []pc
	for _, d := range des {
		if d.IsDir() || strings.HasSuffix(d.Name(), ".tmp") {
			continue
		}
		if st, ok := pieceStart(d.Name()); ok {
			pieces = append(pieces, pc{d.Name(), st})
		}
	}
	if len(pieces) == 0 {
		http.Error(w, "no pieces uploaded", http.StatusBadRequest)
		return
	}
	sort.Slice(pieces, func(i, j int) bool { return pieces[i].start < pieces[j].start })
	out := path.Join(dir, ".assembled")
	f, err := c.s.root.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		davFsErr(w, err)
		return
	}
	var off int64
	byteRanges := strings.Contains(pieces[0].name, "-")
	for _, p := range pieces {
		if byteRanges && p.start != off { // a missing or overlapping piece
			f.Close()
			http.Error(w, fmt.Sprintf("piece starting at %d missing", off), http.StatusBadRequest)
			return
		}
		in, err := c.s.root.Open(path.Join(dir, p.name))
		if err != nil {
			f.Close()
			davFsErr(w, err)
			return
		}
		n, err := io.Copy(f, in)
		in.Close()
		if err != nil {
			f.Close()
			davFsErr(w, err)
			return
		}
		off += n
	}
	if err := f.Close(); err != nil {
		davFsErr(w, err)
		return
	}
	if want := r.Header.Get("OC-Total-Length"); want != "" {
		if n, err := strconv.ParseInt(want, 10, 64); err == nil && n != off {
			http.Error(w, fmt.Sprintf("size mismatch: assembled %d, expected %d", off, n), http.StatusBadRequest)
			return
		}
	}
	if code, msg := c.checkSize(0); code != 0 && off > 0 {
		http.Error(w, msg, code)
		return
	}
	if err := c.place(out, rel, r.Header.Get("X-OC-Mtime")); err != nil {
		davFsErr(w, err)
		return
	}
	_ = c.s.root.RemoveAll(dir)
	w.Header().Set("X-OC-Mtime", "accepted")
	w.WriteHeader(http.StatusCreated)
}

// cleanDavTemp removes abandoned upload pieces/temp files older than a day.
func (s *Server) cleanDavTemp() {
	des, err := rootReadDir(s.root, uploadTmpDir)
	if err != nil {
		return
	}
	cut := time.Now().Add(-24 * time.Hour)
	for _, d := range des {
		if !strings.HasPrefix(d.Name(), "dav-") {
			continue
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cut) {
			_ = s.root.RemoveAll(path.Join(uploadTmpDir, d.Name()))
		}
	}
	davPassGC()
}

func davFsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, fs.ErrExist):
		http.Error(w, "already exists", http.StatusPreconditionFailed)
	case errors.Is(err, fs.ErrPermission):
		http.Error(w, "permission denied", http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func rootReadDir(root *os.Root, dir string) ([]fs.DirEntry, error) {
	f, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func mimeFor(name string) string {
	if ct := mime.TypeByExtension(strings.ToLower(path.Ext(name))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// rcloneObscure produces rclone's "obscured" password form (AES-CTR with
// rclone's published key, random IV, base64url). It's encoding, not
// security: it only lets the config be pasted without running `rclone obscure`.
func rcloneObscure(pass string) string {
	key := []byte{
		0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d,
		0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
		0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb,
		0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
	}
	block, _ := aes.NewCipher(key)
	out := make([]byte, aes.BlockSize+len(pass))
	iv := out[:aes.BlockSize]
	_, _ = rand.Read(iv)
	cipher.NewCTR(block, iv).XORKeyStream(out[aes.BlockSize:], []byte(pass))
	return base64.RawURLEncoding.EncodeToString(out)
}

// rcloneConfig builds a copy-paste rclone remote for a share. pass is the
// plain password when known (it's stored obscured in the config); when the
// share has a password we don't know, the caller shows how to add it.
func rcloneConfig(origin string, sh *Share, pass string) (name, conf, cmd string) {
	name = "filenav-" + strings.ToLower(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, sh.ID))
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\ntype = webdav\nurl = %s/s/%s/dav/files/%s\nvendor = nextcloud\nuser = %s\n", name, origin, sh.ID, davUser, davUser)
	if sh.PassHash != "" && pass != "" {
		fmt.Fprintf(&b, "pass = %s\n", rcloneObscure(pass))
	}
	// 16 MiB pieces: every request stays far below proxy limits (Cloudflare: 100 MB)
	b.WriteString("nextcloud_chunk_size = 16Mi\n")
	switch {
	case sh.UploadOnly:
		cmd = fmt.Sprintf("rclone copy ./files %s:", name)
	case sh.AllowUpload && sh.IsDir:
		cmd = fmt.Sprintf("rclone copy ./files %s:     # upload\nrclone copy %s: ./download  # download", name, name)
	default:
		cmd = fmt.Sprintf("rclone copy %s: ./download", name)
	}
	return name, b.String(), cmd
}

func (s *Server) rcloneOut(r *http.Request, sh *Share, pass string) map[string]any {
	name, conf, cmd := rcloneConfig(s.origin(r), sh, pass)
	out := map[string]any{"name": name, "config": conf, "command": cmd, "min_version": "1.63",
		"url": s.origin(r) + "/s/" + sh.ID + "/dav/files/" + davUser + "/"}
	if sh.PassHash != "" && pass == "" {
		out["pass_command"] = fmt.Sprintf("rclone config update %s pass 'THE_SHARE_PASSWORD' --obscure", name)
	}
	return out
}

// GET /api/shares/{id}/rclone (owner)
func (s *Server) handleShareRclone(w http.ResponseWriter, r *http.Request) {
	var sh *Share
	s.st.View(func(st *State) {
		if v, ok := st.Shares[r.PathValue("id")]; ok {
			c := *v
			sh = &c
		}
	})
	if sh == nil {
		jsonErr(w, 404, "no such share")
		return
	}
	writeJSON(w, s.rcloneOut(r, sh, ""))
}

// POST /s/{id}/api/rclone {password} (visitors; password checked like unlock)
func (s *Server) handlePubRclone(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		jsonErr(w, http.StatusForbidden, "missing CSRF header")
		return
	}
	sh, ok := s.getShare(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pass := ""
	if sh.PassHash != "" && req.Password != "" {
		key := s.clientIP(r) + "|" + sh.ID
		if s.shareLimit.Blocked(key) {
			jsonErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return
		}
		if !verifySecret(sh.PassHash, req.Password) {
			s.shareLimit.Fail(key)
			jsonErr(w, 401, "wrong password")
			return
		}
		pass = req.Password
	}
	writeJSON(w, s.rcloneOut(r, sh, pass))
}
