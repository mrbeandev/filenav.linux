package main

// Small cached preview images for the column view: a compressed JPEG of an
// image, or the first frame of a video. Generated on first request with
// ffmpeg (or the Go image decoders when ffmpeg is missing), stored in
// <data>/thumbs keyed by path+size+mtime, and reused afterwards. One
// generation runs at a time at low priority so a 1-core server stays usable.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	thumbMax       = 640       // longest edge, px
	thumbCacheMax  = 300 << 20 // total cache size before old thumbs are pruned
	thumbSourceMax = 200 << 20 // images bigger than this aren't decoded without ffmpeg
)

var thumbGen = struct {
	sem      chan struct{}
	mu       sync.Mutex
	inflight map[string]*thumbJob
}{sem: make(chan struct{}, 1), inflight: map[string]*thumbJob{}}

type thumbJob struct {
	done chan struct{}
	err  error
}

var thumbVideoExt = map[string]bool{}
var thumbImageExt = map[string]bool{}

func init() {
	for _, e := range strings.Fields("mp4 m4v webm mkv mov avi wmv flv 3gp ts m2ts mts ogv mpg mpeg") {
		thumbVideoExt["."+e] = true
	}
	for _, e := range strings.Fields("jpg jpeg jfif png gif webp bmp tif tiff avif heic heif") {
		thumbImageExt["."+e] = true
	}
}

func (s *Server) thumbDir() string { return filepath.Join(s.cfg.Data, "thumbs") }

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		jsonErr(w, 400, "invalid path")
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	video, img := thumbVideoExt[ext], thumbImageExt[ext]
	if !video && !img {
		jsonErr(w, 415, "no thumbnail for this file type")
		return
	}
	if video && ffmpegPath == "" {
		jsonErr(w, 501, "ffmpeg is not installed")
		return
	}
	st, err := s.root.Stat(fsPath(rel))
	if err != nil {
		fsErr(w, err)
		return
	}
	if st.IsDir() {
		jsonErr(w, 400, "not a file")
		return
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s\x00%d\x00%d", rel, st.Size(), st.ModTime().UnixNano())))
	key := hex.EncodeToString(sum[:])
	etag := `"` + key[:20] + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	file := filepath.Join(s.thumbDir(), key[:2], key+".jpg")
	if _, err := os.Stat(file); err != nil {
		if err := s.makeThumb(r.Context(), key, rel, file, video, st.Size()); err != nil {
			jsonErr(w, 422, "could not make a thumbnail: "+err.Error())
			return
		}
	} else {
		now := time.Now()
		_ = os.Chtimes(file, now, now) // recently used thumbs survive pruning
	}
	b, err := os.ReadFile(file)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "private, max-age=604800")
	h.Set("ETag", etag)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
}

// makeThumb generates the thumbnail once; concurrent requests for the same
// file wait for the first one instead of starting their own.
func (s *Server) makeThumb(ctx context.Context, key, rel, out string, video bool, size int64) error {
	thumbGen.mu.Lock()
	if j := thumbGen.inflight[key]; j != nil {
		thumbGen.mu.Unlock()
		select {
		case <-j.done:
			return j.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	j := &thumbJob{done: make(chan struct{})}
	thumbGen.inflight[key] = j
	thumbGen.mu.Unlock()
	defer func() {
		thumbGen.mu.Lock()
		delete(thumbGen.inflight, key)
		thumbGen.mu.Unlock()
		close(j.done)
	}()

	select {
	case thumbGen.sem <- struct{}{}:
	case <-ctx.Done():
		j.err = ctx.Err()
		return j.err
	}
	defer func() { <-thumbGen.sem }()

	var data []byte
	if ffmpegPath != "" {
		data, j.err = s.ffmpegThumb(rel, video)
		if j.err != nil && !video && size <= thumbSourceMax {
			data, j.err = s.goThumb(rel) // e.g. a format this ffmpeg build lacks
		}
	} else {
		if size > thumbSourceMax {
			j.err = errors.New("image too large")
			return j.err
		}
		data, j.err = s.goThumb(rel)
	}
	if j.err != nil {
		return j.err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		j.err = err
		return err
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		j.err = err
		return err
	}
	j.err = os.Rename(tmp, out)
	return j.err
}

func (s *Server) ffmpegThumb(rel string, video bool) ([]byte, error) {
	scale := fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", thumbMax, thumbMax)
	try := func(seek string) ([]byte, error) {
		f, err := s.root.Open(fsPath(rel))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		args := []string{"-hide_banner", "-v", "error", "-nostdin", "-threads", "1"}
		if seek != "" {
			args = append(args, "-ss", seek)
		}
		args = append(args, "-i", "/dev/fd/3", "-map", "0:v:0", "-frames:v", "1", "-an", "-sn",
			"-vf", scale+",format=yuvj420p", "-q:v", "5", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
		cmd := exec.CommandContext(ctx, ffmpegPath, args...)
		cmd.ExtraFiles = []*os.File{f}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10) // nice: playback and the UI come first
		if err := cmd.Wait(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if i := strings.IndexByte(msg, '\n'); i > 0 {
				msg = msg[:i]
			}
			return nil, fmt.Errorf("ffmpeg: %v %s", err, msg)
		}
		if stdout.Len() == 0 {
			return nil, errors.New("no frame")
		}
		return stdout.Bytes(), nil
	}
	if !video {
		return try("")
	}
	// a frame 1 s in skips black fade-ins; very short clips fall back to the start
	if b, err := try("1"); err == nil {
		return b, nil
	}
	return try("")
}

// goThumb decodes JPEG/PNG/GIF with the standard library and scales with a
// simple box filter. Used when ffmpeg isn't installed.
func (s *Server) goThumb(rel string) ([]byte, error) {
	f, err := s.root.Open(fsPath(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	dst := boxScale(src, thumbMax)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 78}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func boxScale(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	nw, nh := max, h*max/w
	if h > w {
		nw, nh = w*max/h, max
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint64
			// sample at most 4x4 points per output pixel to bound the cost
			sx, sy := max1((x1-x0)/4), max1((y1-y0)/4)
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), uint8(a/n>>8)
		}
	}
	return dst
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// pruneThumbs keeps the cache under thumbCacheMax, dropping the least
// recently used thumbnails first. Called from the janitor.
func (s *Server) pruneThumbs() {
	type tf struct {
		p string
		t time.Time
		n int64
	}
	var all []tf
	var total int64
	_ = filepath.WalkDir(s.thumbDir(), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			if strings.HasSuffix(p, ".tmp") && time.Since(info.ModTime()) > time.Hour {
				os.Remove(p)
				return nil
			}
			all = append(all, tf{p, info.ModTime(), info.Size()})
			total += info.Size()
		}
		return nil
	})
	if total <= thumbCacheMax {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for _, f := range all {
		if total <= thumbCacheMax*8/10 {
			break
		}
		if os.Remove(f.p) == nil {
			total -= f.n
		}
	}
}
