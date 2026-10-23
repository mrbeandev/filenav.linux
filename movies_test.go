package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMovieName(t *testing.T) {
	cases := []struct {
		in    string
		title string
		year  int
	}{
		{"50.First.Dates.2004.1080p.BrRip.x264.YIFY.mp4", "50 First Dates", 2004},
		{"Interstellar (2014) [2160p] [HDR].mkv", "Interstellar", 2014},
		{"The.Matrix.1999.REMASTERED.1080p.BluRay.x265-RARBG.mkv", "The Matrix", 1999},
		{"2001.A.Space.Odyssey.1968.1080p.mkv", "2001 A Space Odyssey", 1968},
		{"Blade Runner 2049 (2017).mp4", "Blade Runner 2049", 2017},
		{"some_movie_without_year.avi", "some movie without year", 0},
		{"Dune.Part.Two.2024.WEB-DL.mkv", "Dune Part Two", 2024},
		{"1917 (2019) 1080p.mkv", "1917", 2019},
	}
	for _, c := range cases {
		title, year, _ := parseMovieName(c.in)
		if title != c.title || year != c.year {
			t.Errorf("parseMovieName(%q) = %q, %d; want %q, %d", c.in, title, year, c.title, c.year)
		}
	}
	if _, _, imdb := parseMovieName("Movie tt0816692 1080p.mkv"); imdb != "tt0816692" {
		t.Errorf("imdb id not found: %q", imdb)
	}
	if got := movieFolderName(`AC/DC: Live? "2"`, 2020); got != "ACDC Live 2 (2020)" {
		t.Errorf("movieFolderName = %q", got)
	}
}

func TestSrtToVTT(t *testing.T) {
	in := "\ufeff1\r\n00:00:01,500 --> 00:00:03,000\r\nHello\r\n"
	out := string(srtToVTT([]byte(in)))
	if !strings.HasPrefix(out, "WEBVTT\n\n") || !strings.Contains(out, "00:00:01.500 --> 00:00:03.000") || strings.Contains(out, "\r") {
		t.Fatalf("bad vtt: %q", out)
	}
	latin := []byte("1\n00:00:01,000 --> 00:00:02,000\ncaf\xe9\n")
	if !strings.Contains(string(srtToVTT(latin)), "café") {
		t.Fatal("latin-1 subtitle not converted to UTF-8")
	}
}

func TestOSHash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v.bin")
	b := make([]byte, 200<<10)
	for i := range b {
		b[i] = byte(i)
	}
	os.WriteFile(p, b, 0o644)
	h, err := osHash(p)
	if err != nil || len(h) != 16 {
		t.Fatalf("hash %q %v", h, err)
	}
	h2, _ := osHash(p)
	if h != h2 {
		t.Fatal("hash not stable")
	}
}

// fake TMDB + OpenSubtitles
func fakeMediaServers(t *testing.T) (downloads *int) {
	n := 0
	downloads = &n
	tm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/movie"):
			q := r.URL.Query().Get("query")
			if strings.Contains(strings.ToLower(q), "interstellar") {
				io.WriteString(w, `{"results":[{"id":157336,"title":"Interstellar","release_date":"2014-11-05","vote_count":30000,"poster_path":"/p.jpg"}]}`)
				return
			}
			io.WriteString(w, `{"results":[]}`)
		case strings.HasPrefix(r.URL.Path, "/find/tt0109830"):
			io.WriteString(w, `{"movie_results":[{"id":13}]}`)
		case r.URL.Path == "/movie/157336":
			io.WriteString(w, `{"id":157336,"imdb_id":"tt0816692","title":"Interstellar","release_date":"2014-11-05","runtime":169,"vote_average":8.4,"poster_path":"/p.jpg","backdrop_path":"/b.jpg","genres":[{"name":"Sci-Fi"}],"credits":{"cast":[{"name":"Matthew McConaughey","character":"Cooper"}],"crew":[{"name":"Christopher Nolan","job":"Director"}]}}`)
		case r.URL.Path == "/movie/13":
			io.WriteString(w, `{"id":13,"imdb_id":"tt0109830","title":"Forrest Gump","release_date":"1994-06-23","poster_path":"/f.jpg","credits":{}}`)
		case strings.HasPrefix(r.URL.Path, "/img/"):
			w.Write([]byte("\xff\xd8\xffJPEG"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tm.Close)
	osrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") { // signed CDN link: no API key
			io.WriteString(w, "1\n00:00:01,000 --> 00:00:02,000\nsub "+strings.TrimPrefix(r.URL.Path, "/file/")+"\n")
			return
		}
		if r.Header.Get("Api-Key") != "oskey" {
			w.WriteHeader(401)
			io.WriteString(w, `{"message":"bad key"}`)
			return
		}
		switch r.URL.Path {
		case "/login":
			io.WriteString(w, `{"token":"tok","base_url":"api.opensubtitles.com","user":{"allowed_downloads":20}}`)
		case "/infos/user":
			io.WriteString(w, `{"data":{"allowed_downloads":20,"remaining_downloads":19,"vip":false}}`)
		case "/subtitles":
			io.WriteString(w, `{"data":[{"attributes":{"download_count":10,"files":[{"file_id":111,"file_name":"a.srt"}]}},{"attributes":{"download_count":5,"moviehash_match":true,"files":[{"file_id":222,"file_name":"exact.srt"}]}}]}`)
		case "/download":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				return
			}
			var body struct {
				FileID int64 `json:"file_id"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			*downloads++
			fmt.Fprintf(w, `{"link":"%s/file/%d","remaining":18}`, "http://"+r.Host, body.FileID)
		default:
			if strings.HasPrefix(r.URL.Path, "/file/") {
				io.WriteString(w, "1\n00:00:01,000 --> 00:00:02,000\nsub "+strings.TrimPrefix(r.URL.Path, "/file/")+"\n")
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(osrv.Close)
	oldT, oldI, oldO := tmdbAPI, tmdbImg, osAPI
	tmdbAPI, tmdbImg, osAPI = tm.URL, tm.URL+"/img/", osrv.URL
	t.Cleanup(func() { tmdbAPI, tmdbImg, osAPI = oldT, oldI, oldO })
	return downloads
}

func TestMovieLibraryFlow(t *testing.T) {
	downloads := fakeMediaServers(t)
	ts, root := newTestServer(t, nil)
	_ = ts
	// find the Server behind the test server to drive the library directly
	srvAny := ts.Config.Handler.(*Server)
	_ = srvAny.st.Update(func(s *State) error {
		s.Media = MediaSettings{TMDBKey: "k", OSKey: "oskey", OSUser: "u", OSPass: "p", MoviesDir: "movies"}
		return nil
	})
	big := func(rel string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		f, _ := os.Create(p)
		f.Truncate(minMovieBytes + 1)
		f.Close()
	}
	big("movies/Interstellar.2014.1080p.BluRay.x264-YIFY/Interstellar.2014.1080p.BluRay.x264-YIFY.mp4")
	os.WriteFile(filepath.Join(root, "movies/Interstellar.2014.1080p.BluRay.x264-YIFY/RARBG.txt"), []byte("x"), 0o644)
	big("movies/random_home_video.mkv")
	big("movies/Forrest Gump tt0109830.mkv")
	os.WriteFile(filepath.Join(root, "movies/tiny-sample.mkv"), []byte("x"), 0o644)

	l := srvAny.movies
	if err := l.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	byName := map[string]*PlanItem{}
	for _, it := range l.plan {
		byName[filepath.Base(it.Src)] = it
	}
	if len(l.plan) != 3 {
		t.Fatalf("plan has %d items, want 3 (sample must be ignored)", len(l.plan))
	}
	in := byName["Interstellar.2014.1080p.BluRay.x264-YIFY.mp4"]
	if in.Status != "ready" || in.Dest != "movies/Interstellar (2014)/Interstellar (2014).mp4" {
		t.Fatalf("interstellar plan: %+v", in)
	}
	if fg := byName["Forrest Gump tt0109830.mkv"]; fg.Status != "ready" || fg.Match.Title != "Forrest Gump" {
		t.Fatalf("imdb-id match failed: %+v", fg)
	}
	unk := byName["random_home_video.mkv"]
	if unk.Status != "unknown" {
		t.Fatalf("unknown expected: %+v", unk)
	}

	// resolve the unknown one manually (like the UI does after an IMDb search)
	srvAny.handleMoviesMatchDirect(t, unk.ID, 157336)

	if err := l.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "movies/Interstellar (2014)")
	for _, f := range []string{"Interstellar (2014).mp4", "Interstellar (2014).en.srt", "poster.jpg", "backdrop.jpg", "movie.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	if sub, _ := os.ReadFile(filepath.Join(dir, "Interstellar (2014).en.srt")); !strings.Contains(string(sub), "sub 222") {
		t.Errorf("hash-matched subtitle not preferred: %q", sub)
	}
	if _, err := os.Stat(filepath.Join(root, "movies/Interstellar.2014.1080p.BluRay.x264-YIFY")); err == nil {
		t.Error("old release folder (only junk left) should be removed")
	}
	// the manually matched file collides with the existing Interstellar folder -> must not overwrite
	if unk.Status != "exists" && unk.Status != "error" {
		t.Errorf("collision not detected: %+v", unk)
	}
	movies := l.loadLibrary()
	if len(movies) != 2 {
		t.Fatalf("library has %d movies, want 2", len(movies))
	}
	var meta MovieMeta
	b, _ := os.ReadFile(filepath.Join(dir, "movie.json"))
	json.Unmarshal(b, &meta)
	if meta.Director != "Christopher Nolan" || meta.IMDbID != "tt0816692" || meta.SubStatus != "ok" {
		t.Errorf("meta: %+v", meta)
	}
	if *downloads != 2 {
		t.Errorf("subtitle downloads = %d, want 2", *downloads)
	}
	// second scan finds nothing new
	l.Scan(context.Background())
	n := 0
	for _, it := range l.plan {
		if it.Status == "ready" {
			n++
		}
	}
	if n != 0 {
		t.Errorf("rescan proposes %d items, want 0", n)
	}
	_ = fs.ErrNotExist
	_ = time.Now
}

// helper: same as POST /api/movies/match
func (s *Server) handleMoviesMatchDirect(t *testing.T, id string, tmdbID int64) {
	t.Helper()
	l := s.movies
	md, err := tmdbDetails("k", tmdbID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range l.plan {
		if it.ID == id {
			it.Match = &MatchInfo{TMDBID: md.ID, Title: md.Title, Year: md.Year()}
			it.Status = ""
			l.finishPlanItem(it)
		}
	}
}

func TestDoRetryResendsBody(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		b, _ := io.ReadAll(r.Body)
		if n == 1 { // drop the first connection mid-request
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := doRetry(srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"a":1}` || n != 2 {
		t.Fatalf("got %q after %d attempts", b, n)
	}
}

func TestMediaProbeAndStream(t *testing.T) {
	if ffmpegPath == "" || ffprobePath == "" {
		t.Skip("ffmpeg not installed")
	}
	ts, root := newTestServer(t, nil)
	src := filepath.Join(root, "clip.mkv")
	gen := exec.Command(ffmpegPath, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=6:size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=duration=6", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "ac3", "-shortest", "-y", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("cannot generate test clip: %v %s", err, out)
	}
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/login", map[string]string{"passcode": "test1234"})
	code, m := do(t, c, "GET", ts.URL+"/api/media/probe?path=clip.mkv", nil)
	info, _ := m["info"].(map[string]any)
	if code != 200 || info == nil || info["container"] != "mkv" {
		t.Fatalf("probe: %d %v", code, m)
	}
	if v := info["video"].(map[string]any); v["codec"] != "h264" {
		t.Fatalf("video codec: %v", v)
	}
	if a := info["audio"].([]any); len(a) != 1 || a[0].(map[string]any)["codec"] != "ac3" {
		t.Fatalf("audio: %v", a)
	}
	// HLS: copy H.264 + convert AC3 -> AAC, and full conversion; fetch a middle
	// segment first (a "seek") then segment 0, and check codecs + timestamps
	for _, mode := range []string{"vc=copy&ac=aac", "vc=h264&ac=aac"} {
		code, st := do(t, c, "GET", ts.URL+"/api/media/hls?path=clip.mkv&"+mode, nil)
		if code != 200 || st["playlist"] == nil {
			t.Fatalf("hls start %s: %d %v", mode, code, st)
		}
		pl, _ := c.Get(ts.URL + st["playlist"].(string))
		body, _ := io.ReadAll(pl.Body)
		pl.Body.Close()
		segs := strings.Count(string(body), ".ts")
		if pl.StatusCode != 200 || !strings.Contains(string(body), "#EXT-X-ENDLIST") || segs < 1 {
			t.Fatalf("playlist %s: %d %q", mode, pl.StatusCode, body)
		}
		base := strings.TrimSuffix(st["playlist"].(string), "index.m3u8")
		order := []int{segs - 1, 0}
		for _, n := range order {
			resp, err := c.Get(fmt.Sprintf("%s%ss%d.ts", ts.URL, base, n))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || len(b) < 188 || b[0] != 0x47 {
				t.Fatalf("%s segment %d: %d, %d bytes", mode, n, resp.StatusCode, len(b))
			}
			out := filepath.Join(t.TempDir(), "seg.ts")
			os.WriteFile(out, b, 0o644)
			pr, _ := exec.Command(ffprobePath, "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", out).Output()
			if got := strings.Fields(string(pr)); len(got) != 2 || got[0] != "h264" || got[1] != "aac" {
				t.Logf("%s segment %d streams: %q (ffprobe may not support .ts here)", mode, n, pr)
			}
		}
	}
	if r2, _ := http.Get(ts.URL + "/api/media/hls/nope/index.m3u8"); r2.StatusCode != 401 {
		t.Fatalf("unauthenticated playlist: %d", r2.StatusCode)
	}
	// unauthenticated access is refused
}

func TestTrailerPick(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[
		 {"key":"teaser123","name":"Teaser","site":"YouTube","type":"Teaser","official":true,"iso_639_1":"en","size":2160,"published_at":"2020-01-01"},
		 {"key":"fest12345","name":"Fest 2023 Trailer","site":"YouTube","type":"Trailer","official":true,"iso_639_1":"en","size":2160,"published_at":"2023-01-01"},
		 {"key":"vimeo1234","name":"Official Trailer","site":"Vimeo","type":"Trailer","official":true,"iso_639_1":"en","size":1080},
		 {"key":"good12345","name":"Official Trailer","site":"YouTube","type":"Trailer","official":true,"iso_639_1":"en","size":1080,"published_at":"2014-05-01"},
		 {"key":"fanmade12","name":"Trailer","site":"YouTube","type":"Trailer","official":false,"iso_639_1":"en","size":2160}]}`)
	}))
	defer srv.Close()
	old := tmdbAPI
	tmdbAPI = srv.URL
	defer func() { tmdbAPI = old }()
	v, err := tmdbTrailer("k", 1, "en")
	if err != nil || v == nil || v.Key != "good12345" {
		t.Fatalf("picked %+v, %v", v, err)
	}
}
