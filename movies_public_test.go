package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicMovies(t *testing.T) {
	ts, root := newTestServer(t, nil)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/login", map[string]string{"passcode": "test1234"})
	mk := func(dir string, m MovieMeta, size int) {
		d := filepath.Join(root, "movies", dir)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, m.Video), make([]byte, size), 0o644)
		for _, s := range m.Subtitles {
			os.WriteFile(filepath.Join(d, s), []byte("1\n"), 0o644)
		}
		b, _ := json.Marshal(m)
		os.WriteFile(filepath.Join(d, "movie.json"), b, 0o644)
	}
	mk("Arrival (2016)", MovieMeta{TMDBID: 329865, IMDbID: "tt2543164", Title: "Arrival", Year: 2016, Rating: 7.6, Genres: []string{"Drama", "Science Fiction"},
		Director: "Denis Villeneuve", Cast: []Actor{{Name: "Amy Adams", Character: "Louise Banks", Profile: "/a.jpg"}}, Poster: "/p.jpg", Backdrop: "/b.jpg",
		Video: "Arrival (2016).mkv", Subtitles: []string{"Arrival (2016).en.srt"}, Added: time.Now().Add(-time.Hour)}, 1234)
	mk("Whiplash (2014)", MovieMeta{TMDBID: 244786, Title: "Whiplash", Year: 2014, Rating: 8.4, Genres: []string{"Drama", "Music"},
		Video: "Whiplash (2014).mp4", Added: time.Now()}, 99)

	pub := &http.Client{} // no cookies
	get := func() (int, http.Header, map[string]any) {
		resp, err := pub.Get(ts.URL + "/api/public/movies?sort=rating")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, resp.Header, v
	}
	if code, _, _ := get(); code != 404 {
		t.Fatalf("off by default: got %d", code)
	}
	if code, _ := do(t, pub, "PUT", ts.URL+"/api/movies/settings", map[string]any{"public_list": true}); code == 200 {
		t.Fatal("enabled without login")
	}
	if code, d := do(t, c, "PUT", ts.URL+"/api/movies/settings", map[string]any{"public_list": true}); code != 200 || d["public_list"] != true {
		t.Fatalf("enable: %d %v", code, d)
	}
	code, hd, v := get()
	if code != 200 || hd.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("public: %d cors=%q", code, hd.Get("Access-Control-Allow-Origin"))
	}
	ms := v["movies"].([]any)
	if v["count"].(float64) != 2 || len(ms) != 2 {
		t.Fatalf("count: %v", v["count"])
	}
	first := ms[0].(map[string]any)
	if first["title"] != "Whiplash" { // sort=rating, highest first
		t.Fatalf("sort: %v", first["title"])
	}
	a := ms[1].(map[string]any)
	if a["imdb_url"] != "https://www.imdb.com/title/tt2543164/" || a["director"] != "Denis Villeneuve" || a["format"] != "mkv" || a["size_bytes"].(float64) != 1234 ||
		!strings.HasPrefix(a["poster"].(string), "https://image.tmdb.org/t/p/w500/") || a["subtitle_languages"].([]any)[0] != "en" ||
		a["cast"].([]any)[0].(map[string]any)["photo"] != "https://image.tmdb.org/t/p/w185/a.jpg" {
		t.Fatalf("fields: %v", a)
	}
	raw, _ := json.Marshal(v)
	for _, leak := range []string{"movies/", ".mkv\"", ".srt", "/api/dl", "dir\""} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("leaks %q: %s", leak, raw)
		}
	}
	for q, want := range map[string]int{"genre=music": 1, "q=arr": 1, "limit=1": 1, "genre=horror": 0} {
		resp, _ := pub.Get(ts.URL + "/api/public/movies?" + q)
		var r struct{ Movies []any }
		json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if len(r.Movies) != want {
			t.Fatalf("%s: %d want %d", q, len(r.Movies), want)
		}
	}
	// hide one movie
	if code, d := do(t, c, "POST", ts.URL+"/api/movies/visibility", map[string]any{"dir": "movies/Whiplash (2014)", "public": false}); code != 200 || d["public"] != false {
		t.Fatalf("hide: %d %v", code, d)
	}
	if _, _, v := get(); v["count"].(float64) != 1 || v["movies"].([]any)[0].(map[string]any)["title"] != "Arrival" {
		t.Fatalf("hidden movie still listed: %v", v["count"])
	}
	if code, _ := do(t, c, "POST", ts.URL+"/api/movies/visibility", map[string]any{"dir": "other", "public": false}); code != 400 {
		t.Fatalf("outside library: %d", code)
	}
	do(t, c, "POST", ts.URL+"/api/movies/visibility", map[string]any{"dir": "movies/Whiplash (2014)", "public": true})
	if _, _, v := get(); v["count"].(float64) != 2 {
		t.Fatal("unhide failed")
	}
	do(t, c, "PUT", ts.URL+"/api/movies/settings", map[string]any{"public_list": false})
	if code, _, _ := get(); code != 404 {
		t.Fatalf("after disable: %d", code)
	}
}
