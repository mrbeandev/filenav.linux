package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFlatYAML(t *testing.T) {
	kv, err := parseFlatYAML([]byte("# c\nroot: \"~/a b\" # x\nport: 9000 # comment\nauth: off\ntitle: 'My #1 files'\norigin: ~\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"root": "~/a b", "port": "9000", "auth": "off", "title": "My #1 files", "origin": ""}
	for k, v := range want {
		if kv[k] != v {
			t.Errorf("%s = %q, want %q", k, kv[k], v)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	c := defaultConfig()
	c.Root, c.Port, c.Title, c.Auth, c.Passcode = "/srv/x y", 9123, `quote "me"`, false, "s3cr3t"
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := writeConfigFile(p, c); err != nil {
		t.Fatal(err)
	}
	got := defaultConfig()
	if err := got.loadFile(p); err != nil {
		t.Fatal(err)
	}
	if got.Root != c.Root || got.Port != c.Port || got.Title != c.Title || got.Auth || got.Passcode != c.Passcode {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestCleanRel(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "a/../b": "b", "../../etc": "etc", "a//b/": "a/b"} {
		got, err := cleanRel(in)
		if err != nil || got != want {
			t.Errorf("cleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := cleanRel(uploadTmpDir + "/x"); err == nil {
		t.Error("upload dir must not be reachable")
	}
}

func TestLooksBinary(t *testing.T) {
	if looksBinary([]byte("node_modules/\n*.log\n")) {
		t.Error("text detected as binary")
	}
	if !looksBinary([]byte{0x7f, 'E', 'L', 'F', 0, 1}) {
		t.Error("binary detected as text")
	}
}

func newTestServer(t *testing.T, mut func(*Config)) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.Root, cfg.Data = filepath.Join(dir, "root"), filepath.Join(dir, "data")
	if mut != nil {
		mut(&cfg)
	}
	if err := cfg.finalize(); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(cfg.Root, 0o755)
	st, err := openState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := hashSecret("test1234")
	_ = st.Update(func(s *State) error { s.PasscodeHash = h; return nil })
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	_ = root.MkdirAll(uploadTmpDir, 0o700)
	sub, _ := fs.Sub(webFS, "web")
	ts := httptest.NewServer(NewServer(cfg, cfg.Root, root, st, sub))
	t.Cleanup(ts.Close)
	return ts, cfg.Root
}

func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func do(t *testing.T, c *http.Client, method, url string, body any, hdr ...string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if b, ok := body.([]byte); ok {
		r = bytes.NewReader(b)
	} else if body != nil {
		j, _ := json.Marshal(body)
		r = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, url, r)
	req.Header.Set("X-Filenav", "1")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestAuthUploadShare(t *testing.T) {
	ts, rootDir := newTestServer(t, nil)
	c := client(t)

	if code, _ := do(t, c, "GET", ts.URL+"/api/list", nil); code != 401 {
		t.Fatalf("unauthenticated list: %d", code)
	}
	if code, _ := do(t, c, "POST", ts.URL+"/api/login", map[string]string{"passcode": "nope"}); code != 401 {
		t.Fatalf("bad passcode: %d", code)
	}
	if code, _ := do(t, c, "POST", ts.URL+"/api/login", map[string]string{"passcode": "test1234"}); code != 200 {
		t.Fatalf("login: %d", code)
	}

	// chunked upload in two parts
	data := bytes.Repeat([]byte("filenav!"), 1000)
	_, init := do(t, c, "POST", ts.URL+"/api/upload/init", map[string]any{"dir": "", "name": "sub/a.txt", "size": len(data), "key": "k"})
	uid, _ := init["uid"].(string)
	if uid == "" {
		t.Fatalf("init: %v", init)
	}
	do(t, c, "PUT", ts.URL+"/api/upload/"+uid, data[:3000], "Upload-Offset", "0")
	if code, m := do(t, c, "PUT", ts.URL+"/api/upload/"+uid, data[:10], "Upload-Offset", "0"); code != 409 || m["offset"].(float64) != 3000 {
		t.Fatalf("offset mismatch not detected: %d %v", code, m)
	}
	_, m := do(t, c, "PUT", ts.URL+"/api/upload/"+uid, data[3000:], "Upload-Offset", "3000")
	if m["done"] != true {
		t.Fatalf("upload not done: %v", m)
	}
	got, _ := os.ReadFile(filepath.Join(rootDir, "sub", "a.txt"))
	if !bytes.Equal(got, data) {
		t.Fatal("uploaded content differs")
	}

	// share the folder with uploads and a password
	_, sh := do(t, c, "POST", ts.URL+"/api/shares", map[string]any{"path": "sub", "allow_upload": true, "password": "pw"})
	id, _ := sh["id"].(string)
	pub := client(t)
	if code, _ := do(t, pub, "GET", ts.URL+"/s/"+id+"/api/list", nil); code != 401 {
		t.Fatalf("locked share listed: %d", code)
	}
	do(t, pub, "POST", ts.URL+"/s/"+id+"/api/unlock", map[string]string{"password": "pw"})
	if code, m := do(t, pub, "GET", ts.URL+"/s/"+id+"/api/list?path=../", nil); code != 200 || len(m["entries"].([]any)) != 1 {
		t.Fatalf("share list escaped or failed: %d %v", code, m)
	}
	if code, _ := do(t, pub, "POST", ts.URL+"/s/"+id+"/api/upload/init", map[string]any{"name": "../../evil", "size": 1}); code != 400 {
		t.Fatalf("traversal upload allowed: %d", code)
	}
	if code, _ := do(t, pub, "GET", ts.URL+"/api/list", nil); code != 401 {
		t.Fatal("share visitor reached admin API")
	}
}

func TestReadOnlyAndNoAuth(t *testing.T) {
	ts, _ := newTestServer(t, func(c *Config) { c.Auth = false; c.ReadOnly = true })
	c := client(t)
	if code, _ := do(t, c, "GET", ts.URL+"/api/list", nil); code != 200 {
		t.Fatalf("auth off list: %d", code)
	}
	if code, m := do(t, c, "POST", ts.URL+"/api/mkdir", map[string]any{"name": "x"}); code != 403 || !strings.Contains(m["error"].(string), "read-only") {
		t.Fatalf("read-only mkdir: %d %v", code, m)
	}
}

func TestZipStream(t *testing.T) {
	var buf bytes.Buffer
	z := newZipStream(&buf)
	w, _ := z.Create("a.txt", fsTime(), false)
	_, _ = w.Write([]byte("hello"))
	_, _ = z.Create("d/", fsTime(), true)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("PK\x03\x04")) || !bytes.Contains(buf.Bytes(), []byte("PK\x05\x06")) {
		t.Fatal("not a zip")
	}
}

func fsTime() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }

func TestParseVersion(t *testing.T) {
	cases := map[string]bool{"v1.0.0": true, "1.2.3": true, "v1.0.0-rc1": true, "dev": false, "531aa8f": false, "v1.0.0-3-gabc": true, "v1.0": false}
	for v, ok := range cases {
		if _, got := parseVersion(v); got != ok {
			t.Errorf("parseVersion(%q) ok=%v want %v", v, got, ok)
		}
	}
	a, _ := parseVersion("v1.10.0")
	b, _ := parseVersion("v1.9.9")
	if !newer(a, b) || newer(b, a) || newer(a, a) {
		t.Error("version comparison wrong")
	}
}

func TestUpdateFlow(t *testing.T) {
	name, err := assetName()
	if err != nil {
		t.Skip(err)
	}
	newBin := []byte("#!/bin/sh\necho new\n")
	sum := sha256.Sum256(newBin)
	good := hex.EncodeToString(sum[:])
	sumsBody := good + "  " + name + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","html_url":"x","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"SHA256SUMS","browser_download_url":"%s/sums"}]}`, name, "http://"+r.Host, "http://"+r.Host)
		case "/bin":
			w.Write(newBin)
		case "/sums":
			io.WriteString(w, sumsBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	releaseAPI = srv.URL + "/latest"
	restartServiceF = func(string) {}
	oldVer := version
	defer func() { version = oldVer }()

	// up to date: nothing happens
	version = "v9.9.9"
	if err := runUpdate([]string{"--yes"}); err != nil {
		t.Fatal(err)
	}

	// can't touch the real test binary: point os.Executable at a copy via a fake dir is not possible,
	// so exercise the download+verify path through expectedSum and a bad checksum.
	if expectedSum(sumsBody, name) != good {
		t.Fatal("expectedSum failed")
	}
	if expectedSum("abc *"+name+"\n", name) != "abc" {
		t.Fatal("expectedSum binary-mode marker")
	}
	var buf bytes.Buffer
	if _, err := download(srv.URL+"/bin", &buf, 1<<20); err != nil || !bytes.Equal(buf.Bytes(), newBin) {
		t.Fatal("download failed", err)
	}
	if _, err := download(srv.URL+"/missing", io.Discard, 1<<20); err == nil {
		t.Fatal("404 download should fail")
	}
}

func TestPWAEndpoints(t *testing.T) {
	ts, _ := newTestServer(t, func(c *Config) { c.Title = "My Files" })
	c := client(t)
	resp, err := c.Get(ts.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if m["name"] != "My Files" || m["display"] != "standalone" || len(m["icons"].([]any)) != 3 {
		t.Fatalf("manifest: %v", m)
	}
	for _, ic := range m["icons"].([]any) {
		r, err := c.Get(ts.URL + ic.(map[string]any)["src"].(string))
		if err != nil || r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("icon %v: %v %v", ic, err, r.Status)
		}
		r.Body.Close()
	}
	r, _ := c.Get(ts.URL + "/sw.js")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || bytes.Contains(b, []byte("__VERSION__")) || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("sw.js: %d %q", r.StatusCode, r.Header.Get("Content-Type"))
	}
}
