package main

// On-demand HLS for videos the browser can't play directly.
//
// The player gets a full-length VOD playlist up front, so its own seek bar,
// duration and subtitles behave exactly like a normal file. Segments are
// produced lazily by one ffmpeg process per session that starts at the
// requested segment and runs ahead; seeking far away restarts it there.
//
// CPU is only spent while someone is watching:
//   - ffmpeg is paused (SIGSTOP) once it is ~60 s ahead of the player and
//     resumed when the player catches up
//   - a session with no requests for 90 s is killed and its files deleted
//   - only one video *conversion* runs at a time server-wide (audio-only
//     conversion is nearly free and not limited)
//
// Modes:
//   - copy: H.264 video copied, audio converted to AAC (or copied). Segments
//     are cut at the file's real keyframes (scanned once, cached).
//   - transcode: video converted to H.264 <=720p with forced keyframes on a
//     fixed 6 s grid.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var hlsDebug = os.Getenv("FILENAV_HLS_DEBUG") != ""

const (
	hlsSegLen    = 6.0              // target segment length (transcode grid)
	hlsAhead     = 10               // pause ffmpeg when this many segments ahead
	hlsResume    = 6                // resume when fewer than this many ahead
	hlsIdle      = 90 * time.Second // drop sessions nobody requested for this long
	hlsKeepBack  = 12               // keep this many segments behind the player on disk
	hlsWaitLimit = 60 * time.Second // max wait for one segment
)

type hlsManager struct {
	dir      string
	mu       sync.Mutex
	sessions map[string]*hlsSession
	byKey    map[string]*hlsSession
	kfCache  map[string][]float64 // file key -> segment start times (copy mode)
	active   *hlsSession          // session currently running a video conversion
}

type hlsRunner struct {
	start  int
	cancel context.CancelFunc
	pid    int
	paused bool
	done   chan struct{}
}

type hlsSession struct {
	m        *hlsManager
	id, key  string
	rel      string
	dir      string
	f        *os.File
	copyV    bool
	acopy    bool
	aidx     int
	segs     []float64 // segment start times; len = segments+1 (last = duration)
	mu       sync.Mutex
	produced map[int]bool
	changed  chan struct{} // closed and replaced whenever produced/runner changes
	run      *hlsRunner
	lastReq  int
	lastSeen time.Time
	closed   bool
}

func newHLSManager(dataDir string) *hlsManager {
	dir := filepath.Join(dataDir, "hls")
	_ = os.RemoveAll(dir) // stale segments from a previous run
	m := &hlsManager{dir: dir, sessions: map[string]*hlsSession{}, byKey: map[string]*hlsSession{}, kfCache: map[string][]float64{}}
	go m.janitor()
	return m
}

func (m *hlsManager) janitor() {
	for range time.Tick(time.Second) {
		m.mu.Lock()
		list := make([]*hlsSession, 0, len(m.sessions))
		for _, s := range m.sessions {
			list = append(list, s)
		}
		m.mu.Unlock()
		for _, s := range list {
			s.tick()
		}
	}
}

// tick: pause/resume the encoder around the playback position; expire idle sessions.
func (s *hlsSession) tick() {
	s.mu.Lock()
	if time.Since(s.lastSeen) > hlsIdle {
		s.mu.Unlock()
		s.close()
		return
	}
	r := s.run
	if r != nil && r.pid > 0 {
		ahead := 0
		for i := s.lastReq + 1; s.produced[i]; i++ {
			ahead++
		}
		if !r.paused && ahead >= hlsAhead {
			if syscall.Kill(r.pid, syscall.SIGSTOP) == nil {
				r.paused = true
			}
		} else if r.paused && ahead < hlsResume {
			if syscall.Kill(r.pid, syscall.SIGCONT) == nil {
				r.paused = false
			}
		}
	}
	s.mu.Unlock()
}

func (s *hlsSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.stopLocked()
	s.mu.Unlock()
	m := s.m
	m.mu.Lock()
	delete(m.sessions, s.id)
	if m.byKey[s.key] == s {
		delete(m.byKey, s.key)
	}
	if m.active == s {
		m.active = nil
	}
	m.mu.Unlock()
	s.f.Close()
	_ = os.RemoveAll(s.dir)
}

func (s *hlsSession) stopLocked() {
	if r := s.run; r != nil {
		if r.paused && r.pid > 0 {
			_ = syscall.Kill(r.pid, syscall.SIGCONT)
		}
		r.cancel()
		s.run = nil
	}
}

func (s *hlsSession) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *hlsSession) nseg() int { return len(s.segs) - 1 }

// startLocked (re)starts ffmpeg at segment n.
func (s *hlsSession) startLocked(n int) error {
	s.stopLocked()
	// segments from n on are rewritten by the new run: never serve a half-written one
	for k := range s.produced {
		if k >= n {
			delete(s.produced, k)
		}
	}
	if !s.copyV { // one video conversion at a time across the server
		m := s.m
		m.mu.Lock()
		prev := m.active
		m.active = s
		m.mu.Unlock()
		if prev != nil && prev != s {
			prev.mu.Lock()
			prev.stopLocked()
			prev.notifyLocked()
			prev.mu.Unlock()
		}
	}
	list := filepath.Join(s.dir, fmt.Sprintf("run-%d.csv", n))
	args := []string{"-hide_banner", "-v", "error", "-nostdin"}
	if s.copyV {
		args = append(args, "-noaccurate_seek")
	}
	if n > 0 {
		args = append(args, "-ss", strconv.FormatFloat(s.segs[n]+0.01, 'f', 3, 64))
	}
	args = append(args, "-i", "/dev/fd/3", "-copyts", "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", s.aidx), "-sn", "-dn")
	if s.copyV {
		// cut exactly at the next scanned keyframes; times are relative to the
		// first packet, which is the keyframe at segs[n]
		var cuts []string
		for j := n + 1; j < s.nseg(); j++ {
			cuts = append(cuts, strconv.FormatFloat(s.segs[j]-s.segs[n]-0.05, 'f', 3, 64))
		}
		args = append(args, "-c:v", "copy")
		if len(cuts) > 0 {
			args = append(args, "-segment_times", strings.Join(cuts, ","))
		} else {
			args = append(args, "-segment_time", "100000")
		}
	} else {
		args = append(args, "-avoid_negative_ts", "disabled",
			"-c:v", "libx264", "-preset", "ultrafast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-vf", "scale=w='min(1280,iw)':h=-2", "-g", "600", "-sc_threshold", "0",
			"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", hlsSegLen),
			"-segment_time", strconv.FormatFloat(hlsSegLen, 'f', -1, 64))
	}
	if s.acopy {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "160k")
	}
	args = append(args, "-max_muxing_queue_size", "4096", "-muxdelay", "0", "-muxpreload", "0",
		"-f", "segment", "-segment_format", "mpegts", "-segment_start_number", strconv.Itoa(n),
		"-segment_list", list, "-segment_list_type", "csv", filepath.Join(s.dir, "s%d.ts"))

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.ExtraFiles = []*os.File{s.f}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuf{b: &stderr, max: 4096}
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10) // browsing stays responsive
	if hlsDebug {
		log.Printf("hls %s start ffmpeg at segment %d (copy=%v)", s.id[:6], n, s.copyV)
	}
	r := &hlsRunner{start: n, cancel: cancel, pid: cmd.Process.Pid, done: make(chan struct{})}
	s.run = r
	go s.watch(r, cmd, list, &stderr)
	return nil
}

// watch follows the segment list ffmpeg appends to after each finished segment.
func (s *hlsSession) watch(r *hlsRunner, cmd *exec.Cmd, list string, stderr *bytes.Buffer) {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var off int64
	read := func() {
		f, err := os.Open(list)
		if err != nil {
			return
		}
		defer f.Close()
		if _, err := f.Seek(off, 0); err != nil {
			return
		}
		br := bufio.NewReader(f)
		var got []int
		for {
			line, err := br.ReadString('\n')
			if err != nil { // partial line: re-read next time
				break
			}
			off += int64(len(line))
			name := strings.SplitN(line, ",", 2)[0]
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "s"), ".ts")); err == nil {
				got = append(got, n)
			}
		}
		if len(got) > 0 {
			s.mu.Lock()
			if s.run == r { // a replaced run's late output must not count
				for _, n := range got {
					s.produced[n] = true
				}
				s.notifyLocked()
			}
			s.mu.Unlock()
		}
	}
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case err := <-exited:
			read()
			if r.paused { // never leave a stopped process behind
				_ = syscall.Kill(r.pid, syscall.SIGCONT)
			}
			s.mu.Lock()
			if s.run == r {
				s.run = nil
				if err != nil && !s.closed {
					log.Printf("media: ffmpeg failed for %s at segment %d: %s", s.rel, r.start, strings.TrimSpace(stderr.String()))
				}
			}
			close(r.done)
			s.notifyLocked()
			s.mu.Unlock()
			return
		case <-t.C:
			read()
		}
	}
}

// segment waits until segment n exists (starting/restarting ffmpeg if needed).
func (s *hlsSession) segment(ctx context.Context, n int) (string, error) {
	deadline := time.After(hlsWaitLimit)
	restarted := false
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return "", errors.New("session closed")
		}
		s.lastSeen = time.Now()
		s.lastReq = n
		if s.produced[n] {
			// free disk: drop segments far behind the player
			for k := range s.produced {
				if k < n-hlsKeepBack {
					delete(s.produced, k)
					_ = os.Remove(filepath.Join(s.dir, fmt.Sprintf("s%d.ts", k)))
				}
			}
			s.mu.Unlock()
			s.tick() // resume the encoder right away if the player caught up
			return filepath.Join(s.dir, fmt.Sprintf("s%d.ts", n)), nil
		}
		r := s.run
		// is the running encoder going to reach n soon?
		near := false
		if r != nil && n >= r.start {
			next := r.start
			for s.produced[next] {
				next++
			}
			near = n <= next+2
		}
		if !near {
			if restarted && r == nil {
				s.mu.Unlock()
				return "", errors.New("conversion failed")
			}
			if err := s.startLocked(n); err != nil {
				s.mu.Unlock()
				return "", err
			}
			restarted = true
		} else if r.paused {
			_ = syscall.Kill(r.pid, syscall.SIGCONT)
			r.paused = false
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", errors.New("timed out waiting for the converted segment")
		}
	}
}

// keyframeSegments scans keyframes cheaply (one seek per ~6 s window) and
// returns segment start times aligned to them.
func keyframeSegments(f *os.File, duration float64) ([]float64, error) {
	var iv []string
	for t := 0.0; t < duration; t += hlsSegLen {
		iv = append(iv, fmt.Sprintf("%.3f%%+#1", t))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-select_streams", "v:0", "-read_intervals", strings.Join(iv, ","),
		"-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", "/dev/fd/3")
	cmd.ExtraFiles = []*os.File{f}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("keyframe scan: %w", err)
	}
	seen := map[int64]bool{}
	var kf []float64
	for _, ln := range strings.Split(string(out), "\n") {
		p := strings.Split(strings.TrimSpace(ln), ",")
		if len(p) < 2 || !strings.Contains(p[1], "K") {
			continue
		}
		t, err := strconv.ParseFloat(p[0], 64)
		if err != nil || t < 0 || t >= duration {
			continue
		}
		ms := int64(math.Round(t * 1000))
		if !seen[ms] {
			seen[ms] = true
			kf = append(kf, t)
		}
	}
	sortFloats(kf)
	segs := []float64{0}
	for _, t := range kf {
		if t-segs[len(segs)-1] >= 1.0 { // skip keyframes too close together
			segs = append(segs, t)
		}
	}
	if duration-segs[len(segs)-1] < 0.5 && len(segs) > 1 {
		segs = segs[:len(segs)-1]
	}
	return append(segs, duration), nil
}

func sortFloats(a []float64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func gridSegments(duration float64) []float64 {
	var segs []float64
	for t := 0.0; t < duration-0.5; t += hlsSegLen {
		segs = append(segs, t)
	}
	if len(segs) == 0 {
		segs = []float64{0}
	}
	return append(segs, duration)
}

// GET /api/media/hls?path=&vc=copy|h264&ac=copy|aac&a=0 -> {id, playlist}
func (s *Server) handleHLSStart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rel, err := cleanRel(q.Get("path"))
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	if ffmpegPath == "" || ffprobePath == "" {
		jsonErr(w, http.StatusNotImplemented, "ffmpeg is not installed on the server")
		return
	}
	aidx, _ := strconv.Atoi(q.Get("a"))
	if aidx < 0 || aidx > 63 {
		aidx = 0
	}
	f, st, err := s.openMedia(rel)
	if err != nil {
		fsErr(w, err)
		return
	}
	info, err := probeMedia(f, rel)
	if err != nil || info.Duration <= 0 {
		f.Close()
		jsonErr(w, 422, "cannot read this video")
		return
	}
	// copying video only works for H.264 in MPEG-TS; anything else is converted
	copyV := q.Get("vc") == "copy" && info.Video != nil && info.Video.Codec == "h264"
	acopy := q.Get("ac") == "copy" && aidx < len(info.Audio) && (info.Audio[aidx].Codec == "aac" || info.Audio[aidx].Codec == "mp3")
	fileKey := fmt.Sprintf("%s\x00%d\x00%d", rel, st.Size(), st.ModTime().UnixNano())
	key := fmt.Sprintf("%s\x00%v\x00%v\x00%d", fileKey, copyV, acopy, aidx)

	m := s.hls
	m.mu.Lock()
	if old := m.byKey[key]; old != nil && !old.closed {
		m.mu.Unlock()
		f.Close()
		old.mu.Lock()
		old.lastSeen = time.Now()
		old.mu.Unlock()
		writeJSON(w, map[string]any{"id": old.id, "playlist": "/api/media/hls/" + old.id + "/index.m3u8", "mode": hlsMode(old.copyV)})
		return
	}
	segs := m.kfCache[fileKey]
	m.mu.Unlock()

	if !copyV {
		segs = gridSegments(info.Duration)
	} else if segs == nil {
		if segs, err = keyframeSegments(f, info.Duration); err != nil {
			f.Close()
			jsonErr(w, 500, err.Error())
			return
		}
		m.mu.Lock()
		if len(m.kfCache) > 200 {
			m.kfCache = map[string][]float64{}
		}
		m.kfCache[fileKey] = segs
		m.mu.Unlock()
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	dir := filepath.Join(m.dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.Close()
		jsonErr(w, 500, err.Error())
		return
	}
	sess := &hlsSession{m: m, id: id, key: key, rel: rel, dir: dir, f: f, copyV: copyV, acopy: acopy, aidx: aidx,
		segs: segs, produced: map[int]bool{}, changed: make(chan struct{}), lastSeen: time.Now()}
	m.mu.Lock()
	m.sessions[id] = sess
	m.byKey[key] = sess
	m.mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "playlist": "/api/media/hls/" + id + "/index.m3u8", "mode": hlsMode(copyV)})
}

func hlsMode(copyV bool) string {
	if copyV {
		return "audio"
	}
	return "video"
}

func (s *Server) hlsSession(w http.ResponseWriter, r *http.Request) *hlsSession {
	s.hls.mu.Lock()
	sess := s.hls.sessions[r.PathValue("id")]
	s.hls.mu.Unlock()
	if sess == nil {
		jsonErr(w, 404, "stream expired, reopen the video")
	}
	return sess
}

func (s *Server) handleHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	sess := s.hlsSession(w, r)
	if sess == nil {
		return
	}
	max := 0.0
	var b strings.Builder
	for i := 0; i < sess.nseg(); i++ {
		max = math.Max(max, sess.segs[i+1]-sess.segs[i])
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n", int(math.Ceil(max)))
	for i := 0; i < sess.nseg(); i++ {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\ns%d.ts\n", sess.segs[i+1]-sess.segs[i], i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	sess.mu.Lock()
	sess.lastSeen = time.Now()
	sess.mu.Unlock()
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleHLSSegment(w http.ResponseWriter, r *http.Request) {
	sess := s.hlsSession(w, r)
	if sess == nil {
		return
	}
	if hlsDebug {
		t0 := time.Now()
		defer func() {
			log.Printf("hls %s %s %v", sess.id[:6], r.PathValue("seg"), time.Since(t0).Round(time.Millisecond))
		}()
	}
	name := r.PathValue("seg")
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "s"), ".ts"))
	if err != nil || n < 0 || n >= sess.nseg() || name != fmt.Sprintf("s%d.ts", n) {
		jsonErr(w, 404, "no such segment")
		return
	}
	p, err := sess.segment(r.Context(), n)
	if err != nil {
		if r.Context().Err() == nil {
			jsonErr(w, 503, err.Error())
		}
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, p)
}
