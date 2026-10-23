package main

// Video probing (ffprobe). The browser decides from this whether it can play
// a file directly; if not, it asks for an on-demand HLS stream (hls.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ffmpegPath  = lookTool("ffmpeg")
	ffprobePath = lookTool("ffprobe")
)

func lookTool(name string) string {
	p, _ := exec.LookPath(name)
	return p
}

type mediaStream struct {
	Codec    string `json:"codec"`
	Profile  string `json:"profile,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Lang     string `json:"lang,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type mediaInfo struct {
	Container string        `json:"container"`
	Duration  float64       `json:"duration"`
	Video     *mediaStream  `json:"video,omitempty"`
	Audio     []mediaStream `json:"audio"`
}

type probeCache struct {
	mu sync.Mutex
	m  map[string]*mediaInfo
}

var mediaProbes = &probeCache{m: map[string]*mediaInfo{}}

// openMedia opens a file through os.Root (no escaping the served folder).
// ffmpeg reads it via an inherited file descriptor, never by path.
func (s *Server) openMedia(rel string) (*os.File, os.FileInfo, error) {
	f, err := s.root.Open(fsPath(rel))
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		if err == nil {
			err = errBadPath
		}
		return nil, nil, err
	}
	return f, st, nil
}

func probeMedia(f *os.File, name string) (*mediaInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", "/dev/fd/3")
	cmd.ExtraFiles = []*os.File{f}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var raw struct {
		Streams []struct {
			Type        string `json:"codec_type"`
			Codec       string `json:"codec_name"`
			Profile     string `json:"profile"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			Channels    int    `json:"channels"`
			Disposition struct {
				Default     int `json:"default"`
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	info := &mediaInfo{Audio: []mediaStream{}}
	info.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	fn := raw.Format.Name
	switch {
	case strings.Contains(fn, "matroska"):
		info.Container = "mkv"
		if strings.EqualFold(path.Ext(name), ".webm") {
			info.Container = "webm"
		}
	case strings.HasPrefix(fn, "mov"):
		info.Container = "mp4"
	default:
		info.Container = strings.Split(fn, ",")[0]
	}
	for _, st := range raw.Streams {
		ms := mediaStream{Codec: st.Codec, Profile: st.Profile, Width: st.Width, Height: st.Height, Channels: st.Channels,
			Lang: st.Tags["language"], Title: st.Tags["title"], Default: st.Disposition.Default == 1}
		switch st.Type {
		case "video":
			if info.Video == nil && st.Disposition.AttachedPic == 0 {
				v := ms
				info.Video = &v
			}
		case "audio":
			info.Audio = append(info.Audio, ms)
		}
	}
	return info, nil
}

func (s *Server) handleMediaProbe(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	if ffprobePath == "" || ffmpegPath == "" {
		writeJSON(w, map[string]any{"ffmpeg": false})
		return
	}
	f, st, err := s.openMedia(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	defer f.Close()
	key := fmt.Sprintf("%s\x00%d\x00%d", rel, st.Size(), st.ModTime().UnixNano())
	mediaProbes.mu.Lock()
	info := mediaProbes.m[key]
	mediaProbes.mu.Unlock()
	if info == nil {
		if info, err = probeMedia(f, rel); err != nil {
			writeJSON(w, map[string]any{"ffmpeg": true, "error": err.Error()})
			return
		}
		mediaProbes.mu.Lock()
		if len(mediaProbes.m) > 1000 {
			mediaProbes.m = map[string]*mediaInfo{}
		}
		mediaProbes.m[key] = info
		mediaProbes.mu.Unlock()
	}
	writeJSON(w, map[string]any{"ffmpeg": true, "info": info})
}

type limitedBuf struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuf) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}
