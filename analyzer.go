package main

// Storage analyzer: a cached, incrementally maintained index of the file tree
// (recursive folder sizes, file counts, categories, largest files).
//
// Speed: directories are read in parallel (worker pool sized to the CPU,
// I/O-bound so oversubscribed), each file is stat'ed with one fstatat()
// relative to its open directory, and symlinks are never followed.
//
// Memory: only directories are kept (own size, own file count, size per
// category, the 10 largest own files). Individual files are not stored;
// a folder's file list is read live when you open it (one readdir).
//
// Staying fresh without rescanning:
//   - while filenav runs, inotify watches every directory; a changed
//     directory is re-read on its own and totals are propagated upward;
//   - on startup the cached index is loaded and validated: one stat per
//     directory; only directories whose mtime changed are re-read.
//     (A directory's mtime changes when entries are created, deleted or
//     renamed in it. Files that grew in place while filenav was stopped are
//     only picked up by a full rescan.)
//   - if inotify can't watch everything (watch limit), a cheap validation
//     pass runs every 10 minutes instead.

import (
	"compress/gzip"
	"context"
	"encoding/gob"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	catImage = iota
	catVideo
	catAudio
	catDoc
	catArchive
	catCode
	catOther
	numCats
)

var catNames = [numCats]string{"Images", "Videos", "Audio", "Documents", "Archives", "Code", "Other"}

var extCat = func() map[string]uint8 {
	m := map[string]uint8{}
	add := func(c uint8, exts string) {
		for _, e := range strings.Fields(exts) {
			m["."+e] = c
		}
	}
	add(catImage, "jpg jpeg jfif png gif webp avif bmp ico svg heic heif tif tiff raw cr2 nef arw dng psd")
	add(catVideo, "mp4 m4v mkv mov avi wmv flv webm ogv 3gp m2ts mts vob")
	add(catAudio, "mp3 wav flac ogg oga opus m4a aac wma aiff")
	add(catDoc, "pdf doc docx odt rtf txt md xls xlsx ods csv ppt pptx odp epub mobi pages numbers key")
	add(catArchive, "zip tar gz tgz bz2 xz 7z rar zst iso dmg img deb rpm apk jar")
	add(catCode, "go js mjs cjs ts tsx jsx py rs c h cpp hpp java kt rb php sh json yml yaml toml xml html css scss sql lua swift")
	return m
}()

func categorize(name string) uint8 {
	if c, ok := extCat[strings.ToLower(path.Ext(name))]; ok {
		return c
	}
	return catOther
}

const topPerDir = 3 // per folder; enough to find the largest files overall in practice

type aTop struct {
	Name string
	Size int64
}

// aNode is one directory. Exported fields are persisted; the rest is derived.
type aNode struct {
	Name     string
	MTime    int64
	Dirs     []*aNode
	OwnSize  int64
	OwnFiles int64
	OwnCat   [numCats]int64
	Top      []aTop // largest own files, descending

	parent *aNode
	size   int64
	files  int64
	dirs   int64
}

type Analyzer struct {
	root    string
	idxPath string
	skip    string // top-level name to ignore

	mu       sync.RWMutex
	tree     *aNode
	lastScan time.Time
	scanDur  time.Duration
	status   string
	lastMode string

	scanning   atomic.Bool
	cancelMu   sync.Mutex
	cancelScan context.CancelFunc
	scanMode   string
	lastCancel time.Time
	progress   atomic.Int64

	watch       *inotify
	watchAll    atomic.Bool // every folder is watched
	rewatching  atomic.Bool
	limitLogged atomic.Bool

	dirtyMu  sync.Mutex
	dirty    map[string]struct{}
	saveTick chan struct{}
}

func NewAnalyzer(root, dataDir string) *Analyzer {
	return &Analyzer{root: root, idxPath: filepath.Join(dataDir, "analyzer.idx"), skip: uploadTmpDir,
		dirty: map[string]struct{}{}, saveTick: make(chan struct{}, 1), status: "starting"}
}

// ---------- reading one directory ----------

type dirInfo struct {
	mtime    int64
	dirs     []string
	ownSize  int64
	ownFiles int64
	ownCat   [numCats]int64
	top      []aTop
}

func fstatat(dirfd int, name string, st *syscall.Stat_t) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall6(sysFstatat, uintptr(dirfd), uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(st)), 0x100 /* AT_SYMLINK_NOFOLLOW */, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

func addTop(top []aTop, name string, size int64) []aTop {
	if len(top) == topPerDir && size <= top[topPerDir-1].Size {
		return top
	}
	i := sort.Search(len(top), func(i int) bool { return top[i].Size < size })
	top = append(top, aTop{})
	copy(top[i+1:], top[i:])
	top[i] = aTop{strings.Clone(name), size}
	if len(top) > topPerDir {
		top = top[:topPerDir]
	}
	return top
}

func (a *Analyzer) readDir(abs string, isRoot bool) (dirInfo, error) {
	var di dirInfo
	f, err := os.Open(abs)
	if err != nil {
		return di, err
	}
	defer f.Close()
	fd := int(f.Fd())
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return di, err
	}
	di.mtime = st.Mtim.Nano()
	for {
		des, err := f.ReadDir(4096)
		for _, de := range des {
			name := de.Name()
			if isRoot && name == a.skip {
				continue
			}
			t := de.Type()
			if t.IsDir() {
				di.dirs = append(di.dirs, strings.Clone(name))
				continue
			}
			if t&fs.ModeSymlink != 0 || !t.IsRegular() {
				continue
			}
			var fst syscall.Stat_t
			if fstatat(fd, name, &fst) != nil {
				continue
			}
			c := categorize(name)
			di.ownSize += fst.Size
			di.ownFiles++
			di.ownCat[c] += fst.Size
			di.top = addTop(di.top, name, fst.Size)
		}
		if err != nil || len(des) == 0 {
			break
		}
	}
	return di, nil
}

// ---------- (re)building the tree ----------

type scanStats struct{ reread, reused atomic.Int64 }

// scanNode returns the node for abs. If old is given and the directory's
// mtime is unchanged, old's own data is reused (no readdir/stat of files);
// children are always checked the same way, recursively and in parallel.
func (a *Analyzer) scanNode(ctx context.Context, old *aNode, abs, name string, isRoot bool, sem chan struct{}, ss *scanStats) *aNode {
	if ctx.Err() != nil {
		return old
	}
	var n *aNode
	var childNames []string
	if old != nil {
		var st syscall.Stat_t
		if syscall.Lstat(abs, &st) != nil {
			return nil // gone
		}
		if st.Mtim.Nano() == old.MTime {
			ss.reused.Add(1)
			n = old
			for _, d := range old.Dirs {
				childNames = append(childNames, d.Name)
			}
		}
	}
	if n == nil {
		di, err := a.readDir(abs, isRoot)
		if err != nil {
			return nil
		}
		ss.reread.Add(1)
		n = &aNode{Name: name, MTime: di.mtime, OwnSize: di.ownSize, OwnFiles: di.ownFiles, OwnCat: di.ownCat, Top: di.top}
		childNames = di.dirs
	}
	a.progress.Add(1)
	oldKids := map[string]*aNode{}
	if old != nil {
		for _, d := range old.Dirs {
			oldKids[d.Name] = d
		}
	}
	kids := make([]*aNode, len(childNames))
	var wg sync.WaitGroup
	for i, cn := range childNames {
		cabs := abs + "/" + cn
		oc := oldKids[cn]
		select {
		case sem <- struct{}{}:
			wg.Add(1)
			go func(i int, oc *aNode, cabs, cn string) {
				defer func() { <-sem; wg.Done() }()
				kids[i] = a.scanNode(ctx, oc, cabs, cn, false, sem, ss)
			}(i, oc, cabs, cn)
		default: // pool busy: recurse inline
			kids[i] = a.scanNode(ctx, oc, cabs, cn, false, sem, ss)
		}
	}
	wg.Wait()
	live := 0
	for _, k := range kids {
		if k != nil {
			live++
		}
	}
	n.Dirs = nil
	if live > 0 {
		n.Dirs = make([]*aNode, 0, live) // exact size: no slack per folder
		for _, k := range kids {
			if k != nil {
				n.Dirs = append(n.Dirs, k)
			}
		}
	}
	return n
}

func (a *Analyzer) build(parent context.Context, old *aNode, mode string) error {
	if !a.scanning.CompareAndSwap(false, true) {
		return errors.New("a scan is already running")
	}
	// every scan can be cancelled from the UI; the previous index stays in place
	ctx, cancel := context.WithCancel(parent)
	a.cancelMu.Lock()
	a.cancelScan, a.scanMode = cancel, mode
	a.cancelMu.Unlock()
	done := func() { // scan phase over: no longer cancellable / "running"
		a.cancelMu.Lock()
		a.cancelScan, a.scanMode = nil, ""
		a.cancelMu.Unlock()
		cancel()
		a.scanning.Store(false)
	}
	if mode == "full" {
		a.setStatus("scanning")
	} else {
		a.setStatus("checking for changes")
	}
	a.progress.Store(0)
	t0 := time.Now()
	workers := max(runtime.NumCPU()*4, 16)
	ss := &scanStats{}
	tree := a.scanNode(ctx, old, a.root, "", true, make(chan struct{}, workers), ss)
	cancelled := ctx.Err() != nil
	done()
	if cancelled {
		// keep the previous (still valid) index; drop the partial result
		a.mu.Lock()
		a.status = "ready"
		if a.tree == nil {
			a.status = "cancelled"
		}
		a.lastCancel = time.Now()
		a.mu.Unlock()
		log.Printf("analyzer: %s scan cancelled after %v", mode, time.Since(t0).Round(time.Millisecond))
		runtime.GC()
		debug.FreeOSMemory()
		return context.Canceled
	}
	if tree == nil {
		a.setStatus("error: cannot read root")
		return errors.New("cannot read root")
	}
	finalize(tree, nil)
	dur := time.Since(t0)
	a.mu.Lock()
	a.tree, a.lastScan, a.scanDur, a.status, a.lastMode = tree, time.Now(), dur, "ready", mode
	a.lastCancel = time.Time{}
	a.mu.Unlock()
	log.Printf("analyzer: %s scan: %d files in %d folders (%s) in %v [%d folders read, %d unchanged]",
		mode, tree.files, tree.dirs+1, fmtBytes(tree.size), dur.Round(time.Millisecond), ss.reread.Load(), ss.reused.Load())
	a.rewatch()
	a.requestSave()
	runtime.GC()
	debug.FreeOSMemory()
	return nil
}

// FullScan rebuilds the index from disk, ignoring the cache.
func (a *Analyzer) FullScan(ctx context.Context) error { return a.build(ctx, nil, "full") }

// Refresh validates the cached index and re-reads only changed folders.
func (a *Analyzer) Refresh(ctx context.Context) error {
	a.mu.RLock()
	old := a.tree
	a.mu.RUnlock()
	if old == nil {
		return a.FullScan(ctx)
	}
	return a.build(ctx, old, "quick")
}

func sumOwn(n *aNode) {
	n.size, n.files, n.dirs = n.OwnSize, n.OwnFiles, int64(len(n.Dirs))
}

func addChild(n, d *aNode) {
	n.size += d.size
	n.files += d.files
	n.dirs += d.dirs
}

// finalize sets parent links and recursive totals bottom-up.
func finalize(n, parent *aNode) {
	n.parent = parent
	sumOwn(n)
	for _, d := range n.Dirs {
		finalize(d, n)
		addChild(n, d)
	}
}

// propagate recomputes totals from n up to the root.
func propagate(n *aNode) {
	for p := n; p != nil; p = p.parent {
		sumOwn(p)
		for _, d := range p.Dirs {
			addChild(p, d)
		}
	}
}

func (a *Analyzer) setStatus(s string) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

// lookup finds a folder by root-relative path; caller holds a.mu.
func (a *Analyzer) lookup(rel string) *aNode {
	n := a.tree
	if n == nil || rel == "" {
		return n
	}
	for _, part := range strings.Split(rel, "/") {
		var next *aNode
		for _, d := range n.Dirs {
			if d.Name == part {
				next = d
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

func relJoin(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func (a *Analyzer) abs(rel string) string {
	if rel == "" {
		return a.root
	}
	return a.root + "/" + rel
}

// ---------- live updates ----------

// updateDir re-reads one folder after a change and splices it into the tree.
func (a *Analyzer) updateDir(rel string) {
	abs := a.abs(rel)
	di, err := a.readDir(abs, rel == "")
	// new subfolders (created or moved in) are scanned outside the lock
	a.mu.RLock()
	n := a.lookup(rel)
	var fresh []string
	if n != nil && err == nil {
		have := map[string]bool{}
		for _, d := range n.Dirs {
			have[d.Name] = true
		}
		for _, name := range di.dirs {
			if !have[name] {
				fresh = append(fresh, name)
			}
		}
	}
	a.mu.RUnlock()
	if n == nil {
		return
	}
	scanned := map[string]*aNode{}
	for _, name := range fresh {
		ss := &scanStats{}
		if sub := a.scanNode(context.Background(), nil, abs+"/"+name, name, false, make(chan struct{}, 8), ss); sub != nil {
			scanned[name] = sub
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	n = a.lookup(rel) // tree may have been replaced meanwhile
	if n == nil {
		return
	}
	if err != nil { // folder vanished
		if p := n.parent; p != nil {
			for i, d := range p.Dirs {
				if d == n {
					p.Dirs = append(p.Dirs[:i], p.Dirs[i+1:]...)
					break
				}
			}
			propagate(p)
		}
		return
	}
	n.MTime, n.OwnSize, n.OwnFiles, n.OwnCat, n.Top = di.mtime, di.ownSize, di.ownFiles, di.ownCat, di.top
	existing := map[string]*aNode{}
	for _, d := range n.Dirs {
		existing[d.Name] = d
	}
	n.Dirs = n.Dirs[:0:0]
	for _, name := range di.dirs {
		if d, ok := existing[name]; ok {
			n.Dirs = append(n.Dirs, d)
		} else if d, ok := scanned[name]; ok {
			finalize(d, n)
			n.Dirs = append(n.Dirs, d)
			if a.watch != nil {
				a.watchTree(d, relJoin(rel, name))
			}
		}
	}
	propagate(n)
}

func (a *Analyzer) markDirty(rel string) {
	a.dirtyMu.Lock()
	a.dirty[rel] = struct{}{}
	a.dirtyMu.Unlock()
}

// Run loads the cache, validates it and keeps the index up to date.
func (a *Analyzer) Run(ctx context.Context) {
	if w, err := newInotify(); err == nil {
		a.watch = w
		go a.watchLoop(ctx)
	} else {
		log.Printf("analyzer: inotify unavailable (%v); using periodic checks", err)
	}
	if err := a.load(); err == nil {
		a.mu.RLock()
		t := a.tree
		a.mu.RUnlock()
		log.Printf("analyzer: loaded cached index (%d files, %s)", t.files, fmtBytes(t.size))
		_ = a.Refresh(ctx)
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("analyzer: cache unusable (%v), rescanning", err)
		}
		_ = a.FullScan(ctx)
	}
	apply := time.NewTicker(700 * time.Millisecond) // debounce bursts
	defer apply.Stop()
	periodic := time.NewTicker(10 * time.Minute)
	defer periodic.Stop()
	saveT := time.NewTimer(time.Hour)
	saveT.Stop()
	for {
		select {
		case <-ctx.Done():
			a.save()
			if a.watch != nil {
				a.watch.Close()
			}
			return
		case <-a.saveTick:
			saveT.Reset(5 * time.Second)
		case <-saveT.C:
			a.save()
		case <-periodic.C:
			if !a.watchAll.Load() {
				_ = a.Refresh(ctx)
			}
		case <-apply.C:
			if a.scanning.Load() {
				continue // the running scan sees the changes; keep them queued
			}
			a.dirtyMu.Lock()
			batch := a.dirty
			a.dirty = map[string]struct{}{}
			a.dirtyMu.Unlock()
			if len(batch) == 0 {
				continue
			}
			for rel := range batch {
				a.updateDir(rel)
			}
			a.requestSave()
		}
	}
}

func (a *Analyzer) requestSave() {
	select {
	case a.saveTick <- struct{}{}:
	default:
	}
}

// ---------- persistence ----------

const idxVersion = 2

type idxFile struct {
	Version  int
	Root     string
	Tree     *aNode
	LastScan time.Time
	ScanDur  time.Duration
	Mode     string
}

func (a *Analyzer) save() {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.tree == nil {
		return
	}
	tmp := a.idxPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	err = gob.NewEncoder(zw).Encode(idxFile{Version: idxVersion, Root: a.root, Tree: a.tree, LastScan: a.lastScan, ScanDur: a.scanDur, Mode: a.lastMode})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		log.Printf("analyzer: saving index failed: %v", err)
		return
	}
	_ = os.Rename(tmp, a.idxPath)
}

func (a *Analyzer) load() error {
	f, err := os.Open(a.idxPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	var x idxFile
	if err := gob.NewDecoder(zr).Decode(&x); err != nil {
		return err
	}
	if x.Version != idxVersion || x.Root != a.root || x.Tree == nil {
		return errors.New("index is for another folder or version")
	}
	finalize(x.Tree, nil)
	debug.FreeOSMemory()
	a.mu.Lock()
	a.tree, a.lastScan, a.scanDur, a.lastMode, a.status = x.Tree, x.LastScan, x.ScanDur, x.Mode, "ready"
	a.mu.Unlock()
	return nil
}

// ---------- HTTP API ----------

type aEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Files int64  `json:"files,omitempty"`
}

type aLarge struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	a := s.analyzer
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		fsErr(w, err)
		return
	}
	a.mu.RLock()
	out := map[string]any{
		"status": a.status, "scanning": a.scanning.Load(), "progress": a.progress.Load(),
		"last_scan": a.lastScan, "scan_ms": a.scanDur.Milliseconds(), "mode": a.lastMode,
		"live": a.watch != nil && a.watchAll.Load(),
	}
	if !a.lastCancel.IsZero() && time.Since(a.lastCancel) < 15*time.Second {
		out["cancelled"] = true
	}
	a.cancelMu.Lock()
	out["scan_mode"] = a.scanMode
	a.cancelMu.Unlock()
	n := a.lookup(rel)
	var entries []aEntry
	var largest []aLarge
	if n != nil {
		out["path"] = rel
		out["size"], out["files"], out["dirs"] = n.size, n.files, n.dirs
		var cb [numCats]int64
		var sumCats func(n *aNode)
		sumCats = func(n *aNode) {
			for c := range n.OwnCat {
				cb[c] += n.OwnCat[c]
			}
			for _, d := range n.Dirs {
				sumCats(d)
			}
		}
		sumCats(n)
		cats := make([]map[string]any, 0, numCats)
		for c := 0; c < numCats; c++ {
			if cb[c] > 0 {
				cats = append(cats, map[string]any{"name": catNames[c], "bytes": cb[c]})
			}
		}
		out["categories"] = cats
		for _, d := range n.Dirs {
			entries = append(entries, aEntry{Name: d.Name, Dir: true, Size: d.size, Files: d.files})
		}
		largest = largestFiles(n, rel, 20)
	}
	a.mu.RUnlock()
	if free, total, ok := s.diskFree(); ok {
		out["disk_free"], out["disk_total"] = free, total
	}
	if n != nil {
		// this folder's own files are read live (one readdir, not stored in the index)
		if des, err := s.root.FS().(fs.ReadDirFS).ReadDir(fsPath(rel)); err == nil {
			for _, de := range des {
				if !de.Type().IsRegular() {
					continue
				}
				if info, err := de.Info(); err == nil {
					entries = append(entries, aEntry{Name: de.Name(), Size: info.Size()})
				}
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Size > entries[j].Size })
		if len(entries) > 300 {
			entries = entries[:300]
		}
		out["entries"] = entries
		out["largest"] = largest
	}
	writeJSON(w, out)
}

// largestFiles merges the per-folder top lists under n (exact for k <= topPerDir*... any k:
// every globally largest file is in its own folder's top list as long as k <= topPerDir).
func largestFiles(n *aNode, rel string, k int) []aLarge {
	var top []aLarge
	var walk func(n *aNode, rel string)
	walk = func(n *aNode, rel string) {
		for _, f := range n.Top {
			if len(top) == k && f.Size <= top[k-1].Size {
				break
			}
			i := sort.Search(len(top), func(i int) bool { return top[i].Size < f.Size })
			top = append(top, aLarge{})
			copy(top[i+1:], top[i:])
			top[i] = aLarge{Path: relJoin(rel, f.Name), Size: f.Size}
			if len(top) > k {
				top = top[:k]
			}
		}
		for _, d := range n.Dirs {
			walk(d, relJoin(rel, d.Name))
		}
	}
	walk(n, rel)
	return top
}

func (s *Server) handleAnalyzeRescan(w http.ResponseWriter, r *http.Request) {
	if s.analyzer.scanning.Load() {
		jsonErr(w, http.StatusConflict, "a scan is already running")
		return
	}
	go func() { _ = s.analyzer.FullScan(context.Background()) }()
	writeJSON(w, map[string]any{"ok": true})
}

// Cancel stops the running scan, if any. Returns false when nothing was running.
func (a *Analyzer) Cancel() bool {
	a.cancelMu.Lock()
	defer a.cancelMu.Unlock()
	if a.cancelScan == nil {
		return false
	}
	a.cancelScan()
	return true
}

func (s *Server) handleAnalyzeCancel(w http.ResponseWriter, r *http.Request) {
	if !s.analyzer.Cancel() {
		jsonErr(w, http.StatusConflict, "no scan is running")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func fmtBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	f, i := float64(n), -1
	for f >= 1024 && i < 5 {
		f /= 1024
		i++
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " " + string("KMGTPE"[i]) + "iB"
}
