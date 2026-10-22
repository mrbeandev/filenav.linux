package main

// Movie library: a folder of movies that filenav identifies (TMDB),
// organises into "Title (Year)/Title (Year).ext" and supplies with
// subtitles (OpenSubtitles). Work only happens when you press Scan: there
// is no background worker.
//
// Flow: Scan -> plan (identify every video not yet in the library, propose
// a destination) -> you review -> Apply (move/rename, fetch poster +
// subtitles, write movie.json). Files that can't be identified land in
// "Unknown", where you can search TMDB or paste an IMDb id.
//
// Each organised movie folder holds a movie.json with its metadata, so the
// library survives restarts, reinstalls and being moved to another server.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

var videoExts = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".webm": true,
	".wmv": true, ".ts": true, ".m2ts": true, ".mpg": true, ".mpeg": true, ".flv": true, ".ogv": true}

const minMovieBytes = 50 << 20 // ignore samples/trailers

// MovieMeta is stored as movie.json inside each organised movie folder.
type MovieMeta struct {
	TMDBID    int64     `json:"tmdb_id"`
	IMDbID    string    `json:"imdb_id,omitempty"`
	Title     string    `json:"title"`
	Year      int       `json:"year,omitempty"`
	Overview  string    `json:"overview,omitempty"`
	Tagline   string    `json:"tagline,omitempty"`
	Runtime   int       `json:"runtime,omitempty"`
	Rating    float64   `json:"rating,omitempty"`
	Genres    []string  `json:"genres,omitempty"`
	Director  string    `json:"director,omitempty"`
	Cast      []Actor   `json:"cast,omitempty"`
	Poster    string    `json:"poster_path,omitempty"`   // TMDB path, for re-downloading
	Backdrop  string    `json:"backdrop_path,omitempty"` // TMDB path
	Video     string    `json:"video"`                   // file name inside the folder
	Subtitles []string  `json:"subtitles,omitempty"`     // file names inside the folder
	SubStatus string    `json:"sub_status,omitempty"`    // "", "ok", "pending", "none"
	Added     time.Time `json:"added"`
}

type Actor struct {
	Name      string `json:"name"`
	Character string `json:"character,omitempty"`
	Profile   string `json:"profile_path,omitempty"`
}

// ---------- filename parsing ----------

var (
	reYear     = regexp.MustCompile(`(?:^|[\s.\-_(\[])((?:19|20)\d{2})(?:$|[\s.\-_)\]])`)
	reJunk     = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k|uhd|hdr10?\+?|dv|dolby.?vision|bluray|blu-ray|brrip|bdrip|web-?dl|webrip|web|hdrip|dvdrip|hdtv|remux|x26[45]|h\.?26[45]|hevc|avc|xvid|divx|aac|ac3|eac3|dts(?:-?hd)?|ddp?5\.1|atmos|truehd|10bit|8bit|proper|repack|extended|unrated|remastered|imax|internal|limited|multi|dual|yify|yts|rarbg|psa|amzn|nf|dsnp|hmax|atvp)\b`)
	reBrackets = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	reIMDb     = regexp.MustCompile(`\btt\d{7,9}\b`)
	reSpaces   = regexp.MustCompile(`\s+`)
)

// parseMovieName guesses title and year from a file or folder name.
func parseMovieName(name string) (title string, year int, imdb string) {
	imdb = reIMDb.FindString(strings.ToLower(name))
	base := strings.TrimSuffix(name, path.Ext(name))
	if !videoExts[strings.ToLower(path.Ext(name))] {
		base = name // folder names: keep dots-as-decimals like "2001.A.Space.Odyssey"
	}
	s := reBrackets.ReplaceAllString(base, " ")
	// the LAST plausible year is the release year ("2001 A Space Odyssey 1968")
	cut := len(s)
	if m := reYear.FindAllStringSubmatchIndex(s, -1); len(m) > 0 {
		last := m[len(m)-1]
		if last[2] > 0 { // a year at the very start is part of the title
			year, _ = strconv.Atoi(s[last[2]:last[3]])
			cut = last[2]
		}
	}
	if j := reJunk.FindStringIndex(s); j != nil && j[0] < cut && j[0] > 0 {
		cut = j[0]
	}
	s = s[:cut]
	s = strings.Map(func(r rune) rune {
		if r == '.' || r == '_' {
			return ' '
		}
		return r
	}, s)
	s = strings.Trim(reSpaces.ReplaceAllString(s, " "), " -([")
	return s, year, imdb
}

// safeName makes a title usable as a file/folder name on any OS.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimRight(strings.TrimSpace(reSpaces.ReplaceAllString(s, " ")), ".")
}

func movieFolderName(title string, year int) string {
	n := safeName(title)
	if year > 0 {
		n = fmt.Sprintf("%s (%d)", n, year)
	}
	return n
}

// ---------- library ----------

type Movie struct {
	MovieMeta
	Dir string `json:"dir"` // root-relative folder
}

type PlanItem struct {
	ID         string      `json:"id"`
	Src        string      `json:"src"` // root-relative video path
	Size       int64       `json:"size"`
	Guess      string      `json:"guess"` // parsed title (year)
	Match      *MatchInfo  `json:"match,omitempty"`
	Candidates []MatchInfo `json:"candidates,omitempty"`
	Dest       string      `json:"dest,omitempty"` // root-relative destination video path
	Status     string      `json:"status"`         // ready | unknown | exists | done | error
	Error      string      `json:"error,omitempty"`
	SubsFrom   []string    `json:"subs_from,omitempty"` // existing subtitle files next to the video
}

type MatchInfo struct {
	TMDBID int64   `json:"tmdb_id"`
	Title  string  `json:"title"`
	Year   int     `json:"year,omitempty"`
	Poster string  `json:"poster,omitempty"`
	Votes  int     `json:"votes,omitempty"`
	Score  float64 `json:"score,omitempty"`
	Over   string  `json:"overview,omitempty"`
}

type MovieLib struct {
	srv *Server
	os  *osClient

	mu      sync.Mutex
	movies  []Movie
	plan    []*PlanItem
	planAt  time.Time
	loaded  bool
	busy    atomic.Bool
	job     string // "scanning" | "applying" | ""
	jobDone atomic.Int64
	jobAll  atomic.Int64
	jobMsg  string
	cancel  context.CancelFunc
	lastLog []string
}

func NewMovieLib(s *Server) *MovieLib {
	return &MovieLib{srv: s, os: &osClient{srv: s}}
}

func (l *MovieLib) settings() MediaSettings {
	var m MediaSettings
	l.srv.st.View(func(s *State) { m = s.Media })
	if m.MoviesDir == "" {
		m.MoviesDir = "movies"
	}
	if m.SubLangs == "" {
		m.SubLangs = "en"
	}
	return m
}

func (l *MovieLib) logf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	log.Print("movies: " + msg)
	l.mu.Lock()
	l.lastLog = append(l.lastLog, time.Now().Format("15:04:05 ")+msg)
	if len(l.lastLog) > 200 {
		l.lastLog = l.lastLog[len(l.lastLog)-200:]
	}
	l.jobMsg = msg
	l.mu.Unlock()
}

// loadLibrary reads every movie.json under the movies folder.
func (l *MovieLib) loadLibrary() []Movie {
	dir := l.settings().MoviesDir
	var out []Movie
	_ = fs.WalkDir(l.srv.root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(p, dir), "/") > 2 {
			return fs.SkipDir
		}
		if d.Name() != "movie.json" {
			return nil
		}
		b, err := l.srv.root.ReadFile(p)
		if err != nil {
			return nil
		}
		var m Movie
		if json.Unmarshal(b, &m.MovieMeta) == nil && m.Title != "" {
			m.Dir = path.Dir(p)
			if _, err := l.srv.root.Stat(path.Join(m.Dir, m.Video)); err == nil {
				out = append(out, m)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Added.After(out[j].Added) })
	l.mu.Lock()
	l.movies, l.loaded = out, true
	l.mu.Unlock()
	return out
}

func (l *MovieLib) list() []Movie {
	l.mu.Lock()
	loaded := l.loaded
	l.mu.Unlock()
	if !loaded {
		return l.loadLibrary()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Movie(nil), l.movies...)
}

// ---------- scan: build a plan ----------

// findCandidates returns video files under the movies folder that are not
// part of an organised movie folder yet.
func (l *MovieLib) findCandidates() ([]PlanItem, error) {
	dir := l.settings().MoviesDir
	if _, err := l.srv.root.Stat(fsPath(dir)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if err := l.srv.root.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, err
	}
	organised := map[string]bool{}
	for _, m := range l.loadLibrary() {
		organised[path.Join(m.Dir, m.Video)] = true
	}
	var out []PlanItem
	err := fs.WalkDir(l.srv.root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(name, ".") || strings.EqualFold(name, "sample") || strings.EqualFold(name, "extras") || strings.EqualFold(name, "featurettes")) {
				return fs.SkipDir
			}
			return nil
		}
		if !videoExts[strings.ToLower(path.Ext(name))] || organised[p] || strings.Contains(strings.ToLower(name), "sample") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < minMovieBytes {
			return nil
		}
		out = append(out, PlanItem{Src: p, Size: info.Size()})
		return nil
	})
	return out, err
}

// nameForMatching picks the most informative name: the parent folder if
// the file sits in its own folder (YIFY style), else the file name.
func (l *MovieLib) nameForMatching(src string) string {
	dir := l.settings().MoviesDir
	parent := path.Dir(src)
	if parent != dir && parent != "." {
		pt, py, _ := parseMovieName(path.Base(parent))
		ft, fy, _ := parseMovieName(path.Base(src))
		if py > 0 && (fy == 0 || len(pt) >= 2) {
			return path.Base(parent)
		}
		if fy == 0 && pt != "" && ft == "" {
			return path.Base(parent)
		}
	}
	return path.Base(src)
}

func titleScore(guess string, m tmdbMovie, year int) float64 {
	norm := func(s string) string {
		s = strings.ToLower(s)
		var b strings.Builder
		for _, r := range s {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	g := norm(guess)
	score := 0.0
	if g != "" && (g == norm(m.Title) || g == norm(m.Original)) {
		score += 60
	} else if g != "" && (strings.Contains(norm(m.Title), g) || strings.Contains(g, norm(m.Title))) {
		score += 30
	}
	if year > 0 && m.Year() == year {
		score += 30
	} else if year > 0 && (m.Year() == year-1 || m.Year() == year+1) {
		score += 15
	}
	if m.Votes > 50 {
		score += 10
	}
	return score
}

func (l *MovieLib) identify(it *PlanItem, key string) {
	name := l.nameForMatching(it.Src)
	title, year, imdb := parseMovieName(name)
	it.Guess = title
	if year > 0 {
		it.Guess = fmt.Sprintf("%s (%d)", title, year)
	}
	if imdb != "" {
		if m, err := tmdbFindIMDb(key, imdb); err == nil {
			mi := MatchInfo{TMDBID: m.ID, Title: m.Title, Year: m.Year(), Poster: m.Poster, Votes: m.Votes, Score: 100, Over: m.Overview}
			it.Match = &mi
			return
		}
	}
	if title == "" {
		return
	}
	res, err := tmdbSearch(key, title, year)
	if err != nil {
		it.Status, it.Error = "error", err.Error()
		return
	}
	for i, m := range res {
		if i >= 6 {
			break
		}
		it.Candidates = append(it.Candidates, MatchInfo{TMDBID: m.ID, Title: m.Title, Year: m.Year(), Poster: m.Poster, Votes: m.Votes, Score: titleScore(title, m, year), Over: m.Overview})
	}
	sort.SliceStable(it.Candidates, func(i, j int) bool { return it.Candidates[i].Score > it.Candidates[j].Score })
	// auto-accept only confident matches
	if len(it.Candidates) > 0 && it.Candidates[0].Score >= 70 {
		c := it.Candidates[0]
		if len(it.Candidates) == 1 || it.Candidates[1].Score < c.Score || year > 0 {
			it.Match = &c
		}
	}
}

func (l *MovieLib) destFor(it *PlanItem) string {
	m := it.Match
	folder := movieFolderName(m.Title, m.Year)
	return path.Join(l.settings().MoviesDir, folder, folder+strings.ToLower(path.Ext(it.Src)))
}

func (l *MovieLib) finishPlanItem(it *PlanItem) {
	if it.Match == nil {
		if it.Status == "" {
			it.Status = "unknown"
		}
		return
	}
	it.Dest = l.destFor(it)
	it.Status = "ready"
	if it.Dest != it.Src {
		_, e1 := l.srv.root.Stat(it.Dest)
		_, e2 := l.srv.root.Stat(path.Join(path.Dir(it.Dest), "movie.json"))
		if e1 == nil || e2 == nil {
			it.Status = "exists" // duplicate of a movie already in the library
		}
	}
	// existing subtitles next to the source get carried along
	dir := path.Dir(it.Src)
	base := strings.TrimSuffix(path.Base(it.Src), path.Ext(it.Src))
	if des, err := fs.ReadDir(l.srv.root.FS(), dir); err == nil {
		for _, de := range des {
			n := de.Name()
			ext := strings.ToLower(path.Ext(n))
			if (ext == ".srt" || ext == ".vtt" || ext == ".ass") && (strings.HasPrefix(n, base) || path.Dir(it.Src) != l.settings().MoviesDir) {
				it.SubsFrom = append(it.SubsFrom, path.Join(dir, n))
			}
		}
	}
}

func (l *MovieLib) Scan(ctx context.Context) error {
	key := l.settings().TMDBKey
	if key == "" {
		return errors.New("add your TMDB API key in Settings first")
	}
	cands, err := l.findCandidates()
	if err != nil {
		return err
	}
	l.jobAll.Store(int64(len(cands)))
	l.jobDone.Store(0)
	l.logf("found %d new video file(s)", len(cands))
	plan := make([]*PlanItem, 0, len(cands))
	for i := range cands {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		it := cands[i]
		it.ID = randomToken(6)
		l.identify(&it, key)
		l.finishPlanItem(&it)
		plan = append(plan, &it)
		l.jobDone.Add(1)
		if it.Match != nil {
			l.logf("matched %s → %s (%d)", path.Base(it.Src), it.Match.Title, it.Match.Year)
		} else {
			l.logf("could not identify %s", path.Base(it.Src))
		}
	}
	l.mu.Lock()
	l.plan, l.planAt = plan, time.Now()
	l.mu.Unlock()
	return nil
}

// ---------- apply ----------

func (l *MovieLib) applyOne(ctx context.Context, it *PlanItem, budget *int) error {
	set := l.settings()
	md, err := tmdbDetails(set.TMDBKey, it.Match.TMDBID)
	if err != nil {
		return err
	}
	dest := it.Dest
	destDir := path.Dir(dest)
	// a movie folder that already holds a library movie is never reused for another file
	if dest != it.Src {
		if _, err := l.srv.root.Stat(path.Join(destDir, "movie.json")); err == nil {
			return fmt.Errorf("%s is already in the library (%s)", it.Match.Title, destDir)
		}
	}
	if err := l.srv.root.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	if dest != it.Src {
		if _, err := l.srv.root.Stat(dest); err == nil {
			return fmt.Errorf("%s already exists", dest)
		}
		if err := l.srv.root.Rename(it.Src, dest); err != nil {
			return fmt.Errorf("move: %w", err)
		}
		l.srv.repointShares(it.Src, dest)
	}
	base := strings.TrimSuffix(path.Base(dest), path.Ext(dest))
	meta := MovieMeta{TMDBID: md.ID, IMDbID: md.IMDbID, Title: md.Title, Year: md.Year(), Overview: md.Overview,
		Tagline: md.Tagline, Runtime: md.Runtime, Rating: md.Vote, Poster: md.Poster, Backdrop: md.Backdrop,
		Video: path.Base(dest), Added: time.Now()}
	for _, g := range md.Genres {
		meta.Genres = append(meta.Genres, g.Name)
	}
	for _, c := range md.Credits.Crew {
		if c.Job == "Director" {
			meta.Director = c.Name
			break
		}
	}
	for i, c := range md.Credits.Cast {
		if i >= 12 {
			break
		}
		meta.Cast = append(meta.Cast, Actor{Name: c.Name, Character: c.Character, Profile: c.Profile})
	}
	// carry existing subtitles along
	for _, sp := range it.SubsFrom {
		ext := strings.ToLower(path.Ext(sp))
		lang := subLangFromName(path.Base(sp), set.SubLangs)
		nn := base + "." + lang + ext
		target := path.Join(destDir, nn)
		if target == sp {
			meta.Subtitles = append(meta.Subtitles, nn)
			continue
		}
		if _, err := l.srv.root.Stat(target); err == nil {
			nn = base + "." + lang + "." + randomToken(2) + ext
			target = path.Join(destDir, nn)
		}
		if l.srv.root.Rename(sp, target) == nil {
			meta.Subtitles = append(meta.Subtitles, nn)
		}
	}
	// images
	if md.Poster != "" {
		_ = l.saveImage(tmdbImg+"w500"+md.Poster, path.Join(destDir, "poster.jpg"))
	}
	if md.Backdrop != "" {
		_ = l.saveImage(tmdbImg+"w1280"+md.Backdrop, path.Join(destDir, "backdrop.jpg"))
	}
	// subtitles
	meta.SubStatus = "ok"
	if !hasLang(meta.Subtitles, firstLang(set.SubLangs)) {
		meta.SubStatus = l.fetchSubtitle(ctx, &meta, destDir, dest, budget)
	}
	if err := l.writeMeta(destDir, meta); err != nil {
		return err
	}
	// tidy: remove the now-empty source folder (only junk left)
	if srcDir := path.Dir(it.Src); srcDir != destDir && srcDir != set.MoviesDir {
		l.removeIfJunk(srcDir)
	}
	return nil
}

func (l *MovieLib) writeMeta(dir string, meta MovieMeta) error {
	b, _ := json.MarshalIndent(meta, "", "  ")
	return l.srv.root.WriteFile(path.Join(dir, "movie.json"), b, 0o644)
}

func firstLang(langs string) string {
	return strings.TrimSpace(strings.Split(langs, ",")[0])
}

func hasLang(subs []string, lang string) bool {
	for _, s := range subs {
		if strings.Contains(strings.ToLower(s), "."+lang+".") {
			return true
		}
	}
	return false
}

func subLangFromName(name, def string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSuffix(name, path.Ext(name))), ".")
	if len(parts) > 1 {
		l := parts[len(parts)-1]
		switch l {
		case "en", "eng", "english":
			return "en"
		}
		if len(l) == 2 {
			return l
		}
	}
	return firstLang(def)
}

// fetchSubtitle downloads the best matching subtitle. Returns the new sub status.
func (l *MovieLib) fetchSubtitle(ctx context.Context, meta *MovieMeta, dir, videoRel string, budget *int) string {
	set := l.settings()
	if set.OSKey == "" {
		return "none"
	}
	if *budget == 0 {
		return "pending"
	}
	lang := firstLang(set.SubLangs)
	hash := ""
	if f, err := os.Open(l.srv.absRoot + "/" + videoRel); err == nil {
		f.Close()
		hash, _ = osHash(l.srv.absRoot + "/" + videoRel)
	}
	subs, err := l.os.search(meta.IMDbID, meta.TMDBID, hash, lang)
	if err != nil {
		l.logf("subtitles for %s: %v", meta.Title, err)
		return "pending"
	}
	if len(subs) == 0 {
		l.logf("no %s subtitles found for %s", lang, meta.Title)
		return "none"
	}
	sort.SliceStable(subs, func(i, j int) bool {
		if subs[i].HashMatch != subs[j].HashMatch {
			return subs[i].HashMatch // exact release sync first
		}
		if subs[i].HearingImp != subs[j].HearingImp {
			return !subs[i].HearingImp
		}
		return subs[i].Downloads > subs[j].Downloads
	})
	data, remaining, err := l.os.download(subs[0].FileID)
	if errors.Is(err, errQuota) {
		*budget = 0
		l.logf("OpenSubtitles daily download limit reached; remaining subtitles will be fetched on a later scan")
		return "pending"
	}
	if err != nil {
		l.logf("subtitle download for %s failed: %v", meta.Title, err)
		return "pending"
	}
	*budget = remaining
	base := strings.TrimSuffix(path.Base(videoRel), path.Ext(videoRel))
	name := base + "." + lang + ".srt"
	if err := l.srv.root.WriteFile(path.Join(dir, name), data, 0o644); err != nil {
		return "pending"
	}
	meta.Subtitles = append(meta.Subtitles, name)
	l.logf("subtitle saved for %s (%d downloads left today)", meta.Title, remaining)
	return "ok"
}

func (l *MovieLib) saveImage(u, rel string) error {
	if _, err := l.srv.root.Stat(rel); err == nil {
		return nil
	}
	tmp := rel + ".part"
	f, err := l.srv.root.Create(tmp)
	if err != nil {
		return err
	}
	err = downloadTo(u, f, 15<<20)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = l.srv.root.Remove(tmp)
		return err
	}
	return l.srv.root.Rename(tmp, rel)
}

// removeIfJunk deletes a folder only if nothing of value is left in it.
func (l *MovieLib) removeIfJunk(dir string) {
	des, err := fs.ReadDir(l.srv.root.FS(), dir)
	if err != nil {
		return
	}
	for _, de := range des {
		n := strings.ToLower(de.Name())
		ext := path.Ext(n)
		junk := ext == ".nfo" || ext == ".txt" || ext == ".url" || ext == ".jpg" || ext == ".png" || ext == ".exe" ||
			n == "sample" || strings.Contains(n, "sample") || strings.HasPrefix(n, "www.") || strings.HasPrefix(n, "rarbg")
		if de.IsDir() && n != "sample" || !junk {
			return
		}
	}
	_ = l.srv.root.RemoveAll(dir)
}

func (l *MovieLib) Apply(ctx context.Context, ids map[string]bool) error {
	l.mu.Lock()
	var todo []*PlanItem
	for _, it := range l.plan {
		if it.Status == "ready" && (ids == nil || ids[it.ID]) {
			todo = append(todo, it)
		}
	}
	l.mu.Unlock()
	l.jobAll.Store(int64(len(todo)))
	l.jobDone.Store(0)
	budget := -1 // unknown until the first download tells us
	if allowed, remaining, _, err := l.os.userInfo(); err == nil && allowed > 0 {
		budget = remaining
	}
	for _, it := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := l.applyOne(ctx, it, &budget); err != nil {
			l.mu.Lock()
			it.Status, it.Error = "error", err.Error()
			l.mu.Unlock()
			l.logf("%s: %v", path.Base(it.Src), err)
		} else {
			l.mu.Lock()
			it.Status = "done"
			l.mu.Unlock()
			l.logf("organised %s (%d)", it.Match.Title, it.Match.Year)
		}
		l.jobDone.Add(1)
	}
	// retry pending subtitles of already organised movies
	l.retryPendingSubs(ctx, &budget)
	l.loadLibrary()
	return nil
}

func (l *MovieLib) retryPendingSubs(ctx context.Context, budget *int) {
	for _, m := range l.loadLibrary() {
		if ctx.Err() != nil || *budget == 0 {
			return
		}
		if m.SubStatus != "pending" {
			continue
		}
		meta := m.MovieMeta
		meta.SubStatus = l.fetchSubtitle(ctx, &meta, m.Dir, path.Join(m.Dir, m.Video), budget)
		_ = l.writeMeta(m.Dir, meta)
	}
}

// ---------- job runner ----------

func (l *MovieLib) start(kind string, fn func(ctx context.Context) error) error {
	if !l.busy.CompareAndSwap(false, true) {
		return errors.New("the movie library is busy, try again in a moment")
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	l.job, l.cancel, l.jobMsg = kind, cancel, ""
	l.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			l.mu.Lock()
			l.job, l.cancel = "", nil
			l.mu.Unlock()
			l.busy.Store(false)
		}()
		if err := fn(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				l.logf("%s cancelled", kind)
			} else {
				l.logf("%s failed: %v", kind, err)
			}
		}
	}()
	return nil
}

// ---------- HTTP ----------

func (s *Server) handleMovies(w http.ResponseWriter, r *http.Request) {
	l := s.movies
	set := l.settings()
	l.mu.Lock()
	plan := append([]*PlanItem(nil), l.plan...)
	job, msg := l.job, l.jobMsg
	l.mu.Unlock()
	unknown, ready, other := []*PlanItem{}, []*PlanItem{}, []*PlanItem{}
	for _, it := range plan {
		switch it.Status {
		case "unknown", "error":
			unknown = append(unknown, it)
		case "ready", "exists":
			ready = append(ready, it)
		default:
			other = append(other, it)
		}
	}
	pending := 0
	movies := l.list()
	if movies == nil {
		movies = []Movie{}
	}
	for _, m := range movies {
		if m.SubStatus == "pending" {
			pending++
		}
	}
	writeJSON(w, map[string]any{
		"movies":       movies,
		"plan":         ready,
		"unknown":      unknown,
		"done":         other,
		"job":          job,
		"job_msg":      msg,
		"job_done":     l.jobDone.Load(),
		"job_all":      l.jobAll.Load(),
		"dir":          set.MoviesDir,
		"configured":   set.TMDBKey != "",
		"subs_on":      set.OSKey != "" && set.OSUser != "",
		"subs_pending": pending,
	})
}

func (s *Server) handleMoviesScan(w http.ResponseWriter, r *http.Request) {
	if err := s.movies.start("scan", s.movies.Scan); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleMoviesApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var ids map[string]bool
	if len(req.IDs) > 0 {
		ids = map[string]bool{}
		for _, id := range req.IDs {
			ids[id] = true
		}
	}
	if err := s.movies.start("organise", func(ctx context.Context) error { return s.movies.Apply(ctx, ids) }); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleMoviesCancel(w http.ResponseWriter, r *http.Request) {
	s.movies.mu.Lock()
	c := s.movies.cancel
	s.movies.mu.Unlock()
	if c == nil {
		jsonErr(w, http.StatusConflict, "nothing is running")
		return
	}
	c()
	writeJSON(w, map[string]any{"ok": true})
}

// handleMoviesSearch searches TMDB by name or resolves an IMDb id / TMDB link.
func (s *Server) handleMoviesSearch(w http.ResponseWriter, r *http.Request) {
	key := s.movies.settings().TMDBKey
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		jsonErr(w, 400, "empty search")
		return
	}
	out := []MatchInfo{}
	add := func(m tmdbMovie) {
		out = append(out, MatchInfo{TMDBID: m.ID, Title: m.Title, Year: m.Year(), Poster: m.Poster, Votes: m.Votes, Over: m.Overview})
	}
	if imdb := reIMDb.FindString(strings.ToLower(q)); imdb != "" {
		m, err := tmdbFindIMDb(key, imdb)
		if err != nil {
			jsonErr(w, 404, err.Error())
			return
		}
		add(*m)
	} else if m := regexp.MustCompile(`themoviedb\.org/movie/(\d+)`).FindStringSubmatch(q); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		md, err := tmdbDetails(key, id)
		if err != nil {
			jsonErr(w, 404, err.Error())
			return
		}
		add(*md)
	} else {
		title, year, _ := parseMovieName(q)
		if title == "" {
			title = q
		}
		res, err := tmdbSearch(key, title, year)
		if err != nil {
			jsonErr(w, 502, err.Error())
			return
		}
		for i, m := range res {
			if i < 12 {
				add(m)
			}
		}
	}
	writeJSON(w, map[string]any{"results": out})
}

// handleMoviesMatch assigns a TMDB movie to a plan item (from Unknown).
func (s *Server) handleMoviesMatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		TMDBID int64  `json:"tmdb_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	l := s.movies
	md, err := tmdbDetails(l.settings().TMDBKey, req.TMDBID)
	if err != nil {
		jsonErr(w, 502, err.Error())
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, it := range l.plan {
		if it.ID == req.ID {
			it.Match = &MatchInfo{TMDBID: md.ID, Title: md.Title, Year: md.Year(), Poster: md.Poster, Votes: md.Votes, Score: 100, Over: md.Overview}
			it.Status, it.Error = "", ""
			l.finishPlanItem(it)
			writeJSON(w, it)
			return
		}
	}
	jsonErr(w, 404, "item not found; scan again")
}

// handleMovieSubs re-downloads subtitles for one organised movie.
func (s *Server) handleMovieSubs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir string `json:"dir"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	l := s.movies
	dir, err := cleanRel(req.Dir)
	if err != nil {
		fsErr(w, err)
		return
	}
	b, err := s.root.ReadFile(path.Join(dir, "movie.json"))
	if err != nil {
		jsonErr(w, 404, "not a library movie")
		return
	}
	var meta MovieMeta
	if json.Unmarshal(b, &meta) != nil {
		jsonErr(w, 500, "bad movie.json")
		return
	}
	budget := -1
	meta.SubStatus = l.fetchSubtitle(r.Context(), &meta, dir, path.Join(dir, meta.Video), &budget)
	_ = l.writeMeta(dir, meta)
	l.loadLibrary()
	writeJSON(w, map[string]any{"status": meta.SubStatus, "subtitles": meta.Subtitles})
}

// handleMediaSettings reads/writes the movie-library credentials. Secrets
// are never returned; only whether they are set.
func (s *Server) handleMediaSettingsGet(w http.ResponseWriter, r *http.Request) {
	m := s.movies.settings()
	writeJSON(w, map[string]any{
		"tmdb_key_set": m.TMDBKey != "", "os_key_set": m.OSKey != "", "os_user": m.OSUser, "os_pass_set": m.OSPass != "",
		"movies_dir": m.MoviesDir, "sub_langs": m.SubLangs,
	})
}

func (s *Server) handleMediaSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TMDBKey   *string `json:"tmdb_key"`
		OSKey     *string `json:"os_key"`
		OSUser    *string `json:"os_user"`
		OSPass    *string `json:"os_pass"`
		MoviesDir *string `json:"movies_dir"`
		SubLangs  *string `json:"sub_langs"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.MoviesDir != nil {
		d, err := cleanRel(*req.MoviesDir)
		if err != nil || d == "" {
			jsonErr(w, 400, "choose a folder inside your files (not the root)")
			return
		}
		*req.MoviesDir = d
	}
	err := s.st.Update(func(st *State) error {
		m := &st.Media
		trim := func(p *string) string { return strings.TrimSpace(*p) }
		if req.TMDBKey != nil {
			m.TMDBKey = trim(req.TMDBKey)
		}
		if req.OSKey != nil {
			m.OSKey = trim(req.OSKey)
			m.OSToken = ""
		}
		if req.OSUser != nil {
			m.OSUser = trim(req.OSUser)
			m.OSToken = ""
		}
		if req.OSPass != nil {
			m.OSPass = *req.OSPass
			m.OSToken = ""
		}
		if req.MoviesDir != nil {
			m.MoviesDir = *req.MoviesDir
		}
		if req.SubLangs != nil {
			m.SubLangs = strings.ToLower(trim(req.SubLangs))
		}
		return nil
	})
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	s.movies.mu.Lock()
	s.movies.loaded = false
	s.movies.mu.Unlock()
	s.handleMediaSettingsGet(w, r)
}

// handleMediaTest checks the TMDB key and the OpenSubtitles login.
func (s *Server) handleMediaTest(w http.ResponseWriter, r *http.Request) {
	set := s.movies.settings()
	out := map[string]any{}
	if set.TMDBKey == "" {
		out["tmdb"] = "not set"
	} else if _, err := tmdbSearch(set.TMDBKey, "Inception", 2010); err != nil {
		out["tmdb"] = err.Error()
	} else {
		out["tmdb"] = "ok"
	}
	switch {
	case set.OSKey == "":
		out["opensubtitles"] = "not set"
	case set.OSUser == "" || set.OSPass == "":
		out["opensubtitles"] = "API key set; add username and password to download subtitles"
	default:
		if _, err := s.movies.os.token(true); err != nil {
			out["opensubtitles"] = err.Error()
		} else if allowed, remaining, vip, err := s.movies.os.userInfo(); err != nil {
			out["opensubtitles"] = err.Error()
		} else {
			kind := "free"
			if vip {
				kind = "VIP"
			}
			out["opensubtitles"] = "ok"
			out["os_info"] = fmt.Sprintf("%s account · %d of %d downloads left today", kind, remaining, allowed)
		}
	}
	writeJSON(w, out)
}

// handleMoviePoster serves a movie's poster or backdrop from its folder
// (falls back to proxying TMDB so search results don't leak the client IP).
func (s *Server) handleTMDBImage(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	size := r.URL.Query().Get("s")
	if !regexp.MustCompile(`^/[A-Za-z0-9_\-]+\.(jpg|png|webp)$`).MatchString(p) {
		jsonErr(w, 400, "bad image path")
		return
	}
	switch size {
	case "w92", "w185", "w342", "w500", "w780", "w1280":
	default:
		size = "w342"
	}
	ireq, _ := http.NewRequest("GET", tmdbImg+size+p, nil)
	resp, err := doRetry(mediaHTTP, ireq)
	if err != nil {
		jsonErr(w, 502, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		jsonErr(w, resp.StatusCode, "image unavailable")
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, max-age=604800")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 20<<20))
}

// handleSubVTT converts an .srt subtitle to WebVTT for the browser's <track>.
func (s *Server) handleSubVTT(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	b, err := s.root.ReadFile(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	if len(b) > 10<<20 {
		jsonErr(w, 413, "subtitle too large")
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	shift, _ := strconv.ParseFloat(r.URL.Query().Get("shift"), 64)
	_, _ = w.Write(shiftVTT(srtToVTT(b), shift))
}

var reSrtTime = regexp.MustCompile(`(\d{2}:\d{2}:\d{2}),(\d{3})`)

func srtToVTT(b []byte) []byte {
	s := string(b)
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.HasPrefix(s, "WEBVTT") {
		return []byte(s)
	}
	if !isUTF8(s) { // most old .srt files are Windows-1252
		s = latin1ToUTF8(b)
		s = strings.ReplaceAll(strings.TrimPrefix(s, "\ufeff"), "\r\n", "\n")
	}
	s = reSrtTime.ReplaceAllString(s, "$1.$2")
	return []byte("WEBVTT\n\n" + s)
}

func isUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == unicode.ReplacementChar && n == 1 {
			return false
		}
		i += n
	}
	return true
}

func latin1ToUTF8(b []byte) string {
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return string(rs)
}
