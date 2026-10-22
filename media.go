package main

// Smart video playback. The browser plays files directly whenever it can;
// for formats it can't (HEVC on devices without a decoder, AC3/DTS/TrueHD
// audio, AVI, ...) filenav converts only the part that's needed while
// streaming, using ffmpeg if it's installed:
//
//   - video copied + audio converted to AAC: almost no CPU
//   - video converted to H.264 (max 720p, x264 ultrafast): ~0.2-0.5 core
//
// Output is fragmented MP4 piped straight to the client; nothing is written
// to disk. Seeking restarts the stream at the requested time (?t=).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// one video conversion at a time: a new one (e.g. after seeking) replaces the old
var (
	tcMu     sync.Mutex
	tcCancel context.CancelFunc
)

func (s *Server) handleMediaStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rel, err := cleanRel(q.Get("path"))
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	if ffmpegPath == "" {
		jsonErr(w, http.StatusNotImplemented, "ffmpeg is not installed on the server")
		return
	}
	t, _ := strconv.ParseFloat(q.Get("t"), 64)
	if t < 0 || t > 1e6 {
		t = 0
	}
	aidx, _ := strconv.Atoi(q.Get("a"))
	if aidx < 0 || aidx > 63 {
		aidx = 0
	}
	vcopy := q.Get("vc") == "copy"
	acopy := q.Get("ac") == "copy"
	f, _, err := s.openMedia(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if !vcopy {
		tcMu.Lock()
		if tcCancel != nil {
			tcCancel()
		}
		tcCancel = cancel
		tcMu.Unlock()
	}

	args := []string{"-hide_banner", "-v", "error", "-nostdin"}
	if t > 0 {
		args = append(args, "-ss", strconv.FormatFloat(t, 'f', 2, 64))
	}
	args = append(args, "-i", "/dev/fd/3", "-map", "0:v:0?", "-map", fmt.Sprintf("0:a:%d?", aidx))
	if vcopy {
		args = append(args, "-c:v", "copy", "-tag:v", "hvc1") // tag only applies to HEVC (needed by Safari/Chrome in MP4)
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-vf", "scale=w='min(1280,iw)':h=-2", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-threads", "0")
	}
	if acopy {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "160k")
	}
	args = append(args, "-sn", "-dn", "-max_muxing_queue_size", "1024",
		"-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "pipe:1")
	// HEVC tag must not be applied to H.264 copies
	if vcopy {
		args = stripHVC1UnlessHEVC(args, f)
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.ExtraFiles = []*os.File{f}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuf{b: &stderr, max: 4096}
	out, err := cmd.StdoutPipe()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		jsonErr(w, 500, "ffmpeg: "+err.Error())
		return
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10) // file browsing stays responsive
	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	rc := http.NewResponseController(w)
	buf := make([]byte, 128<<10)
	for {
		n, rerr := out.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				cancel()
				break
			}
			_ = rc.Flush()
		}
		if rerr != nil {
			break
		}
	}
	_ = cmd.Wait()
	if ctx.Err() == nil && cmd.ProcessState != nil && !cmd.ProcessState.Success() {
		log.Printf("media: ffmpeg failed for %s: %s", rel, strings.TrimSpace(stderr.String()))
	}
}

// stripHVC1UnlessHEVC removes "-tag:v hvc1" when the source video isn't HEVC.
func stripHVC1UnlessHEVC(args []string, f *os.File) []string {
	info, err := probeMedia(f, "")
	if err == nil && info.Video != nil && info.Video.Codec == "hevc" {
		return args
	}
	out := args[:0:0]
	for i := 0; i < len(args); i++ {
		if args[i] == "-tag:v" && i+1 < len(args) && args[i+1] == "hvc1" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
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

var reVTTTime = regexp.MustCompile(`(\d{2,}):(\d{2}):(\d{2})\.(\d{3})`)

// shiftVTT moves every cue earlier by sec seconds (for streams started at ?t=).
func shiftVTT(b []byte, sec float64) []byte {
	if sec <= 0 {
		return b
	}
	lines := strings.Split(string(b), "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, "-->") {
			continue
		}
		lines[i] = reVTTTime.ReplaceAllStringFunc(ln, func(m string) string {
			p := reVTTTime.FindStringSubmatch(m)
			hh, _ := strconv.Atoi(p[1])
			mm, _ := strconv.Atoi(p[2])
			ss, _ := strconv.Atoi(p[3])
			ms, _ := strconv.Atoi(p[4])
			t := float64(hh*3600+mm*60+ss) + float64(ms)/1000 - sec
			if t < 0 {
				t = 0
			}
			total := int(t*1000 + 0.5)
			return fmt.Sprintf("%02d:%02d:%02d.%03d", total/3600000, total/60000%60, total/1000%60, total%1000)
		})
	}
	return []byte(strings.Join(lines, "\n"))
}
