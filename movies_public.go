package main

// Public, read-only movie list for showing your library elsewhere (a website,
// a widget). Off by default; enabled in Settings → Movies. Only metadata is
// returned: no video/subtitle links, no folder paths. Images point straight at
// TMDB's CDN so they cost this server nothing.

import (
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type publicActor struct {
	Name      string `json:"name"`
	Character string `json:"character,omitempty"`
	Photo     string `json:"photo,omitempty"`
}

type publicMovie struct {
	ID        int64         `json:"id"` // TMDB id
	IMDbID    string        `json:"imdb_id,omitempty"`
	Title     string        `json:"title"`
	Year      int           `json:"year,omitempty"`
	Tagline   string        `json:"tagline,omitempty"`
	Overview  string        `json:"overview,omitempty"`
	Runtime   int           `json:"runtime_minutes,omitempty"`
	Rating    float64       `json:"rating,omitempty"` // TMDB average, 0-10
	Genres    []string      `json:"genres"`
	Director  string        `json:"director,omitempty"`
	Cast      []publicActor `json:"cast"`
	Poster    string        `json:"poster,omitempty"`
	PosterSm  string        `json:"poster_small,omitempty"`
	Backdrop  string        `json:"backdrop,omitempty"`
	Subtitles []string      `json:"subtitle_languages"`
	Format    string        `json:"format,omitempty"` // container, e.g. "mkv"
	Size      int64         `json:"size_bytes,omitempty"`
	Added     time.Time     `json:"added"`
	TMDBURL   string        `json:"tmdb_url"`
	IMDbURL   string        `json:"imdb_url,omitempty"`
}

func tmdbImageURL(size, p string) string {
	if p == "" {
		return ""
	}
	return tmdbImg + size + p
}

// subLang pulls the language code out of "Title (Year).en.srt".
func subLang(name string) string {
	parts := strings.Split(strings.TrimSuffix(name, path.Ext(name)), ".")
	if len(parts) >= 2 {
		l := strings.ToLower(parts[len(parts)-1])
		if len(l) >= 2 && len(l) <= 8 && !strings.ContainsAny(l, " ()") {
			return l
		}
	}
	return ""
}

func (s *Server) handlePublicMovies(w http.ResponseWriter, r *http.Request) {
	l := s.movies
	if !l.settings().PublicList {
		jsonErr(w, http.StatusNotFound, "the public movie list is not enabled")
		return
	}
	q := r.URL.Query()
	movies := l.list()
	out := make([]publicMovie, 0, len(movies))
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	genre := strings.ToLower(strings.TrimSpace(q.Get("genre")))
	for _, m := range movies {
		if m.Hidden {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(m.Title), search) {
			continue
		}
		if genre != "" {
			ok := false
			for _, g := range m.Genres {
				if strings.ToLower(g) == genre {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		pm := publicMovie{
			ID: m.TMDBID, IMDbID: m.IMDbID, Title: m.Title, Year: m.Year, Tagline: m.Tagline, Overview: m.Overview,
			Runtime: m.Runtime, Rating: m.Rating, Genres: append([]string{}, m.Genres...), Director: m.Director,
			Cast: []publicActor{}, Subtitles: []string{}, Added: m.Added,
			Poster: tmdbImageURL("w500", m.Poster), PosterSm: tmdbImageURL("w185", m.Poster), Backdrop: tmdbImageURL("w1280", m.Backdrop),
			TMDBURL: "https://www.themoviedb.org/movie/" + strconv.FormatInt(m.TMDBID, 10),
		}
		if m.IMDbID != "" {
			pm.IMDbURL = "https://www.imdb.com/title/" + m.IMDbID + "/"
		}
		for _, a := range m.Cast {
			pm.Cast = append(pm.Cast, publicActor{Name: a.Name, Character: a.Character, Photo: tmdbImageURL("w185", a.Profile)})
		}
		seen := map[string]bool{}
		for _, sub := range m.Subtitles {
			if lang := subLang(sub); lang != "" && !seen[lang] {
				seen[lang] = true
				pm.Subtitles = append(pm.Subtitles, lang)
			}
		}
		if m.Video != "" {
			pm.Format = strings.TrimPrefix(strings.ToLower(path.Ext(m.Video)), ".")
			if st, err := s.root.Stat(fsPath(path.Join(m.Dir, m.Video))); err == nil {
				pm.Size = st.Size()
			}
		}
		out = append(out, pm)
	}
	key, order := q.Get("sort"), q.Get("order")
	desc := order != "asc" // newest/highest first by default
	if key == "title" {
		desc = order == "desc" // A-Z by default
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if desc {
			a, b = b, a
		}
		switch key {
		case "title":
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		case "year":
			return a.Year < b.Year
		case "rating":
			return a.Rating < b.Rating
		default: // date added
			return a.Added.Before(b.Added)
		}
	})
	total := len(out)
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < len(out) {
		out = out[:n]
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*") // usable from any website
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "public, max-age=300")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"title":        s.cfg.Title,
		"count":        total,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"movies":       out,
	})
}
