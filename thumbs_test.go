package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestThumbnails(t *testing.T) {
	ts, root := newTestServer(t, nil)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/login", map[string]string{"passcode": "test1234"})
	im := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 1600; x++ {
			im.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var pb, jb bytes.Buffer
	png.Encode(&pb, im)
	jpeg.Encode(&jb, im, nil)
	os.WriteFile(filepath.Join(root, "a.png"), pb.Bytes(), 0o644)
	os.WriteFile(filepath.Join(root, "b.jpg"), jb.Bytes(), 0o644)
	os.WriteFile(filepath.Join(root, "c.txt"), []byte("hi"), 0o644)

	get := func(p string) (int, []byte, http.Header) {
		t.Helper()
		resp, err := c.Get(ts.URL + "/api/thumb?path=" + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header
	}
	check := func(name string, wantW int) {
		t.Helper()
		code, b, hd := get(name)
		if code != 200 || hd.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("%s: %d %s %s", name, code, hd.Get("Content-Type"), b)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width != wantW {
			t.Fatalf("%s: decode %v width %d want %d", name, err, cfg.Width, wantW)
		}
	}
	for _, withFF := range []bool{true, false} {
		if withFF && ffmpegPath == "" {
			continue
		}
		saved := ffmpegPath
		if !withFF {
			ffmpegPath = ""
		}
		os.RemoveAll(filepath.Join(filepath.Dir(root), "data", "thumbs"))
		check("a.png", thumbMax)
		check("b.jpg", thumbMax)
		ffmpegPath = saved
	}
	// cached: same bytes, conditional request -> 304
	_, b1, hd := get("a.png")
	_, b2, _ := get("a.png")
	if !bytes.Equal(b1, b2) {
		t.Fatal("cached thumbnail differs")
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/thumb?path=a.png", nil)
	req.Header.Set("If-None-Match", hd.Get("ETag"))
	if resp, err := c.Do(req); err != nil || resp.StatusCode != 304 {
		t.Fatalf("etag: %v %v", err, resp.StatusCode)
	}
	// file changes -> new thumbnail (different etag)
	small := image.NewRGBA(image.Rect(0, 0, 200, 100))
	var sb bytes.Buffer
	png.Encode(&sb, small)
	os.WriteFile(filepath.Join(root, "a.png"), sb.Bytes(), 0o644)
	os.Chtimes(filepath.Join(root, "a.png"), time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	if _, _, hd2 := get("a.png"); hd2.Get("ETag") == hd.Get("ETag") {
		t.Fatal("thumbnail not refreshed after file change")
	}
	check("a.png", 200) // smaller than the limit: not upscaled
	if code, _, _ := get("c.txt"); code != 415 {
		t.Fatalf("txt: %d", code)
	}
	if code, _, _ := get("../x.jpg"); code == 200 {
		t.Fatal("path escape")
	}
	// video: first frame
	if ffmpegPath != "" {
		v := filepath.Join(root, "v.mp4")
		out, err := exec.Command(ffmpegPath, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=3:size=1280x720:rate=10",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-y", v).CombinedOutput()
		if err != nil {
			t.Skipf("cannot make test video: %v %s", err, out)
		}
		check("v.mp4", thumbMax)
	}
	// unauthenticated
	if resp, _ := http.Get(ts.URL + "/api/thumb?path=b.jpg"); resp.StatusCode != 401 {
		t.Fatalf("unauth: %d", resp.StatusCode)
	}
}
