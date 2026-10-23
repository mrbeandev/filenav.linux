package main

// Clients for TMDB (movie metadata, posters) and OpenSubtitles.com
// (subtitle search + download). Keys and the OpenSubtitles login live in
// the state file and are set from Settings; they never leave the server
// except to talk to those two services.

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// variables (not consts) so tests can point them at fake servers;
// FILENAV_TEST_*_URL env vars do the same for manual testing.
var (
	tmdbAPI = envOr("FILENAV_TEST_TMDB_URL", "https://api.themoviedb.org/3")
	tmdbImg = envOr("FILENAV_TEST_TMDB_IMG_URL", "https://image.tmdb.org/t/p/")
	osAPI   = envOr("FILENAV_TEST_OS_URL", "https://api.opensubtitles.com/api/v1")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// MediaSettings holds the credentials for the movie library features.
type MediaSettings struct {
	TMDBKey    string    `json:"tmdb_key,omitempty"`
	OSKey      string    `json:"os_key,omitempty"`
	OSUser     string    `json:"os_user,omitempty"`
	OSPass     string    `json:"os_pass,omitempty"`
	OSToken    string    `json:"os_token,omitempty"`
	OSTokenExp time.Time `json:"os_token_exp,omitempty"`
	OSBaseURL  string    `json:"os_base_url,omitempty"` // VIP accounts get another host
	MoviesDir  string    `json:"movies_dir,omitempty"`  // root-relative
	SubLangs   string    `json:"sub_langs,omitempty"`   // e.g. "en"
}

// mediaHTTP talks to TMDB/OpenSubtitles. Some networks advertise IPv6 but
// black-hole it; a short per-family dial timeout makes "happy eyeballs"
// fall back to IPv4 within ~1 s instead of hanging until the request timeout.
var mediaHTTP = &http.Client{
	Timeout: 45 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, FallbackDelay: 300 * time.Millisecond}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// osHTTP is HTTP/1.1 only: api.opensubtitles.com (behind Cloudflare) hangs on
// HTTP/2 POST requests such as /login and /download, while HTTP/1.1 answers
// immediately. A non-nil empty TLSNextProto map disables HTTP/2.
var osHTTP = &http.Client{
	Timeout: 45 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, FallbackDelay: 300 * time.Millisecond}).DialContext,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// ---------- TMDB ----------

type tmdbMovie struct {
	ID          int64   `json:"id"`
	IMDbID      string  `json:"imdb_id"`
	Title       string  `json:"title"`
	Original    string  `json:"original_title"`
	ReleaseDate string  `json:"release_date"`
	Overview    string  `json:"overview"`
	Poster      string  `json:"poster_path"`
	Backdrop    string  `json:"backdrop_path"`
	Vote        float64 `json:"vote_average"`
	Votes       int     `json:"vote_count"`
	Runtime     int     `json:"runtime"`
	Tagline     string  `json:"tagline"`
	Popularity  float64 `json:"popularity"`
	Genres      []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Credits struct {
		Cast []struct {
			Name      string `json:"name"`
			Character string `json:"character"`
			Profile   string `json:"profile_path"`
		} `json:"cast"`
		Crew []struct {
			Name string `json:"name"`
			Job  string `json:"job"`
		} `json:"crew"`
	} `json:"credits"`
}

func (m tmdbMovie) Year() int {
	if len(m.ReleaseDate) >= 4 {
		y, _ := strconv.Atoi(m.ReleaseDate[:4])
		return y
	}
	return 0
}

func tmdbGet(key, path string, q url.Values, out any) error {
	if key == "" {
		return errors.New("TMDB API key is not set (Settings → Movies)")
	}
	if q == nil {
		q = url.Values{}
	}
	req, _ := http.NewRequest("GET", tmdbAPI+path+"?"+q.Encode(), nil)
	// v4 "read access tokens" are long JWTs; v3 keys are 32 hex chars
	if len(key) > 40 {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		q.Set("api_key", key)
		req.URL.RawQuery = q.Encode()
	}
	req.Header.Set("Accept", "application/json")
	resp, err := doRetry(mediaHTTP, req)
	if err != nil {
		return fmt.Errorf("TMDB: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return errors.New("TMDB rejected the API key")
	}
	if resp.StatusCode == 404 {
		return errNotFound
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("TMDB: %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

var errNotFound = errors.New("not found")

func tmdbSearch(key, title string, year int) ([]tmdbMovie, error) {
	q := url.Values{"query": {title}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var r struct {
		Results []tmdbMovie `json:"results"`
	}
	if err := tmdbGet(key, "/search/movie", q, &r); err != nil {
		return nil, err
	}
	if len(r.Results) == 0 && year > 0 { // year in filenames is often off by one
		q.Del("year")
		if err := tmdbGet(key, "/search/movie", q, &r); err != nil {
			return nil, err
		}
	}
	return r.Results, nil
}

type tmdbVideo struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Site      string `json:"site"`
	Type      string `json:"type"`
	Official  bool   `json:"official"`
	Lang      string `json:"iso_639_1"`
	Size      int    `json:"size"`
	Published string `json:"published_at"`
}

// tmdbTrailer returns the best YouTube trailer: official > trailer type >
// preferred language > highest resolution > oldest (the original release trailer).
func tmdbTrailer(key string, id int64, lang string) (*tmdbVideo, error) {
	var r struct {
		Results []tmdbVideo `json:"results"`
	}
	q := url.Values{"include_video_language": {lang + ",en,null"}}
	if err := tmdbGet(key, "/movie/"+strconv.FormatInt(id, 10)+"/videos", q, &r); err != nil {
		return nil, err
	}
	score := func(v tmdbVideo) int {
		s := 0
		if v.Type == "Trailer" {
			s += 100
		} else if v.Type == "Teaser" {
			s += 40
		}
		if v.Official {
			s += 50
		}
		if v.Lang == lang {
			s += 20
		}
		// skip re-release / festival / subtitled-international cuts and teasers in the name
		n := strings.ToLower(v.Name)
		for _, bad := range []string{"teaser", "fest", "re-release", "anniversary", "4k restoration", "subtitled", "int'l", "international", "tv spot", "clip"} {
			if strings.Contains(n, bad) {
				s -= 30
			}
		}
		if strings.Contains(n, "official trailer") {
			s += 10
		}
		return s + v.Size/360 // resolution only breaks ties (1080 -> +3, 2160 -> +6)
	}
	var best *tmdbVideo
	for i := range r.Results {
		v := r.Results[i]
		if v.Site != "YouTube" || v.Key == "" || (v.Type != "Trailer" && v.Type != "Teaser") {
			continue
		}
		if best == nil || score(v) > score(*best) || score(v) == score(*best) && v.Published < best.Published {
			best = &v
		}
	}
	return best, nil
}

func tmdbDetails(key string, id int64) (*tmdbMovie, error) {
	var m tmdbMovie
	err := tmdbGet(key, "/movie/"+strconv.FormatInt(id, 10), url.Values{"append_to_response": {"credits"}}, &m)
	return &m, err
}

// tmdbFindIMDb resolves an IMDb id (tt123...) to a TMDB movie.
func tmdbFindIMDb(key, imdb string) (*tmdbMovie, error) {
	var r struct {
		Movies []tmdbMovie `json:"movie_results"`
	}
	if err := tmdbGet(key, "/find/"+url.PathEscape(imdb), url.Values{"external_source": {"imdb_id"}}, &r); err != nil {
		return nil, err
	}
	if len(r.Movies) == 0 {
		return nil, fmt.Errorf("no movie found for IMDb id %s", imdb)
	}
	return tmdbDetails(key, r.Movies[0].ID)
}

// doRetry retries idempotent GETs on connection-level failures (resets,
// timeouts, TLS errors): flaky networks and CDN edges cause these often.
func doRetry(c *http.Client, req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 300 * time.Millisecond)
			c.CloseIdleConnections()
		}
		r := req.Clone(req.Context())
		if req.GetBody != nil { // fresh copy of the body for each attempt
			if r.Body, err = req.GetBody(); err != nil {
				return nil, err
			}
		}
		resp, err = c.Do(r)
		if err == nil {
			if resp.StatusCode < 500 && resp.StatusCode != 429 {
				return resp, nil
			}
			if attempt == 3 {
				return resp, nil
			}
			resp.Body.Close()
			continue
		}
		// POSTs are retried only for failures before the server could have
		// processed them (connection refused/reset during setup, TLS errors)
		if req.Method != http.MethodGet && !safeToRetry(err) {
			return nil, err
		}
	}
	return resp, err
}

// safeToRetry reports connection-level errors where the request almost
// certainly never reached the application (login/download are fine to repeat).
func safeToRetry(err error) bool {
	s := err.Error()
	for _, m := range []string{"connection reset", "connection refused", "handshake", "EOF", "broken pipe", "no such host", "i/o timeout"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func downloadTo(rawURL string, w io.Writer, limit int64) error {
	req, _ := http.NewRequest("GET", rawURL, nil)
	resp, err := doRetry(mediaHTTP, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: %s", resp.Status)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, limit))
	return err
}

// ---------- OpenSubtitles.com ----------

type osClient struct {
	mu  sync.Mutex
	srv *Server
}

func (c *osClient) settings() MediaSettings {
	var m MediaSettings
	c.srv.st.View(func(s *State) { m = s.Media })
	return m
}

func (c *osClient) base(m MediaSettings) string {
	if m.OSBaseURL != "" {
		return m.OSBaseURL
	}
	return osAPI
}

func (c *osClient) do(method, path string, body any, token string, out any) (int, error) {
	m := c.settings()
	if m.OSKey == "" {
		return 0, errors.New("OpenSubtitles API key is not set (Settings → Movies)")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base(m)+path, rd)
	req.Header.Set("Api-Key", m.OSKey)
	req.Header.Set("User-Agent", "filenav v"+strings.TrimPrefix(version, "v"))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := doRetry(osHTTP, req) // retries GETs only; POSTs (login, download) are not repeated
	if err != nil {
		return 0, fmt.Errorf("OpenSubtitles: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Message string `json:"message"`
			Errors  []string
		}
		_ = json.Unmarshal(b, &e)
		msg := e.Message
		if msg == "" && len(e.Errors) > 0 {
			msg = strings.Join(e.Errors, "; ")
		}
		if msg == "" {
			msg = resp.Status
		}
		return resp.StatusCode, fmt.Errorf("OpenSubtitles: %s", msg)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("OpenSubtitles: bad response: %w", err)
		}
	}
	return 200, nil
}

// token returns a cached login token, logging in when needed.
func (c *osClient) token(force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.settings()
	if !force && m.OSToken != "" && time.Now().Before(m.OSTokenExp) {
		return m.OSToken, nil
	}
	if m.OSUser == "" || m.OSPass == "" {
		return "", errors.New("OpenSubtitles username/password are not set (needed to download subtitles)")
	}
	var r struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
		User    struct {
			AllowedDownloads int  `json:"allowed_downloads"`
			VIP              bool `json:"vip"`
		} `json:"user"`
	}
	if _, err := c.do("POST", "/login", map[string]string{"username": m.OSUser, "password": m.OSPass}, "", &r); err != nil {
		return "", err
	}
	base := ""
	if r.BaseURL != "" && r.BaseURL != "api.opensubtitles.com" {
		base = "https://" + strings.TrimPrefix(strings.TrimPrefix(r.BaseURL, "https://"), "http://") + "/api/v1"
	}
	_ = c.srv.st.Update(func(s *State) error {
		s.Media.OSToken, s.Media.OSTokenExp, s.Media.OSBaseURL = r.Token, time.Now().Add(23*time.Hour), base
		return nil
	})
	return r.Token, nil
}

type osSub struct {
	FileID     int64
	FileName   string
	Downloads  int
	HashMatch  bool
	Release    string
	MachineTr  bool
	HearingImp bool
}

func (c *osClient) search(imdb string, tmdbID int64, hash, lang string) ([]osSub, error) {
	q := url.Values{"languages": {lang}}
	if imdb != "" {
		q.Set("imdb_id", strings.TrimLeft(strings.TrimPrefix(strings.ToLower(imdb), "tt"), "0"))
	} else if tmdbID > 0 {
		q.Set("tmdb_id", strconv.FormatInt(tmdbID, 10))
	}
	if hash != "" {
		q.Set("moviehash", hash)
	}
	q.Set("type", "movie")
	var r struct {
		Data []struct {
			Attributes struct {
				Downloads   int    `json:"download_count"`
				HashMatch   bool   `json:"moviehash_match"`
				Release     string `json:"release"`
				MachineTr   bool   `json:"machine_translated"`
				AITr        bool   `json:"ai_translated"`
				HearingImpd bool   `json:"hearing_impaired"`
				Files       []struct {
					FileID   int64  `json:"file_id"`
					FileName string `json:"file_name"`
				} `json:"files"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if _, err := c.do("GET", "/subtitles?"+sortedQuery(q), nil, "", &r); err != nil {
		return nil, err
	}
	var out []osSub
	for _, d := range r.Data {
		a := d.Attributes
		if len(a.Files) == 0 || a.MachineTr || a.AITr {
			continue
		}
		out = append(out, osSub{FileID: a.Files[0].FileID, FileName: a.Files[0].FileName, Downloads: a.Downloads,
			HashMatch: a.HashMatch, Release: a.Release, HearingImp: a.HearingImpd})
	}
	return out, nil
}

// OpenSubtitles asks for sorted, lowercase query parameters (avoids redirects).
func sortedQuery(q url.Values) string {
	return q.Encode() // url.Values.Encode sorts by key
}

var errQuota = errors.New("daily subtitle download limit reached")

// download returns the subtitle bytes and the remaining daily quota.
func (c *osClient) download(fileID int64) ([]byte, int, error) {
	tok, err := c.token(false)
	if err != nil {
		return nil, 0, err
	}
	var r struct {
		Link      string `json:"link"`
		Remaining int    `json:"remaining"`
		Message   string `json:"message"`
		ResetTime string `json:"reset_time"`
	}
	code, err := c.do("POST", "/download", map[string]any{"file_id": fileID, "sub_format": "srt"}, tok, &r)
	if code == 401 { // token expired early
		if tok, err = c.token(true); err != nil {
			return nil, 0, err
		}
		code, err = c.do("POST", "/download", map[string]any{"file_id": fileID, "sub_format": "srt"}, tok, &r)
	}
	if code == 406 || code == 429 && strings.Contains(strings.ToLower(fmt.Sprint(err)), "download") {
		return nil, 0, errQuota
	}
	if err != nil {
		return nil, 0, err
	}
	if r.Link == "" {
		return nil, r.Remaining, errQuota
	}
	var buf bytes.Buffer
	if err := downloadTo(r.Link, &buf, 5<<20); err != nil {
		return nil, r.Remaining, err
	}
	return buf.Bytes(), r.Remaining, nil
}

func (c *osClient) userInfo() (allowed, remaining int, vip bool, err error) {
	tok, err := c.token(false)
	if err != nil {
		return 0, 0, false, err
	}
	var r struct {
		Data struct {
			Allowed   int  `json:"allowed_downloads"`
			Remaining int  `json:"remaining_downloads"`
			VIP       bool `json:"vip"`
		} `json:"data"`
	}
	if _, err := c.do("GET", "/infos/user", nil, tok, &r); err != nil {
		return 0, 0, false, err
	}
	return r.Data.Allowed, r.Data.Remaining, r.Data.VIP, nil
}

// osHash computes the OpenSubtitles movie hash: size + 64-bit sums of the
// first and last 64 KiB.
func osHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const chunk = 64 << 10
	size := st.Size()
	if size < chunk*2 {
		return "", errors.New("file too small for hashing")
	}
	h := uint64(size)
	buf := make([]byte, chunk)
	for _, off := range []int64{0, size - chunk} {
		if _, err := f.ReadAt(buf, off); err != nil {
			return "", err
		}
		for i := 0; i < chunk; i += 8 {
			h += binary.LittleEndian.Uint64(buf[i:])
		}
	}
	return fmt.Sprintf("%016x", h), nil
}
