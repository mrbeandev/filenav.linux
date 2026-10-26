package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRcloneObscure(t *testing.T) {
	for _, pw := range []string{"s3cret-pass", "", "ünïcødé пароль 🔑"} {
		o := rcloneObscure(pw)
		b, err := base64.RawURLEncoding.DecodeString(o)
		if err != nil || len(b) != aes.BlockSize+len(pw) {
			t.Fatalf("bad encoding %q", o)
		}
		key := []byte{0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d, 0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b, 0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb, 0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38}
		blk, _ := aes.NewCipher(key)
		out := make([]byte, len(pw))
		cipher.NewCTR(blk, b[:aes.BlockSize]).XORKeyStream(out, b[aes.BlockSize:])
		if string(out) != pw {
			t.Fatalf("round trip: %q != %q", out, pw)
		}
	}
}

func TestShareWebDAV(t *testing.T) {
	ts, root := newTestServer(t, nil)
	os.MkdirAll(filepath.Join(root, "pub", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "pub", "a.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(root, "secret.txt"), []byte("no"), 0o644)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/login", map[string]string{"passcode": "test1234"})
	_, rw := do(t, c, "POST", ts.URL+"/api/shares", map[string]any{"path": "pub", "allow_upload": true, "password": "pw123456"})
	_, ro := do(t, c, "POST", ts.URL+"/api/shares", map[string]any{"path": "pub"})
	base := func(sh map[string]any) string { return ts.URL + "/s/" + sh["id"].(string) + "/dav/" }
	req := func(method, url, user, pass string, body string, hdr map[string]string) (int, string, http.Header) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r, _ := http.NewRequest(method, url, rd)
		if user != "" {
			r.SetBasicAuth(user, pass)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b), resp.Header
	}
	rwF, rwU, roF := base(rw)+"files/share/", base(rw)+"uploads/share/", base(ro)+"files/share/"
	d1 := map[string]string{"Depth": "1"}

	if code, _, h := req("PROPFIND", rwF, "", "", "", d1); code != 401 || h.Get("WWW-Authenticate") == "" {
		t.Fatalf("no password: %d", code)
	}
	if code, _, _ := req("PROPFIND", rwF, "share", "wrong", "", d1); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	code, body, _ := req("PROPFIND", rwF, "share", "pw123456", "", d1)
	if code != 207 || !strings.Contains(body, "a.txt") || !strings.Contains(body, "sub/") || strings.Contains(body, "secret") {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body, _ := req("GET", rwF+"a.txt", "share", "pw123456", "", nil); code != 200 || body != "hello" {
		t.Fatalf("get: %d %q", code, body)
	}
	for _, p := range []string{"../secret.txt", "..%2fsecret.txt", "sub/../../secret.txt"} {
		if code, body, _ := req("GET", rwF+p, "share", "pw123456", "", nil); code == 200 || body == "no" {
			t.Fatalf("escape %s: %d %q", p, code, body)
		}
	}
	// simple upload, no overwrite, no delete/move
	if code, _, _ := req("PUT", rwF+"new.txt", "share", "pw123456", "data", map[string]string{"X-OC-Mtime": "1700000000"}); code != 201 {
		t.Fatalf("put: %d", code)
	}
	if st, _ := os.Stat(filepath.Join(root, "pub", "new.txt")); st == nil || st.ModTime().Unix() != 1700000000 {
		t.Fatalf("mtime not applied: %v", st)
	}
	if code, _, _ := req("PUT", rwF+"a.txt", "share", "pw123456", "evil", nil); code != 412 {
		t.Fatalf("overwrite: %d", code)
	}
	if code, _, _ := req("DELETE", rwF+"a.txt", "share", "pw123456", "", nil); code != 403 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := req("MOVE", rwF+"a.txt", "share", "pw123456", "", map[string]string{"Destination": rwF + "b.txt"}); code != 403 {
		t.Fatalf("move: %d", code)
	}
	if code, _, _ := req("MKCOL", rwF+"newdir/", "share", "pw123456", "", nil); code != 201 {
		t.Fatalf("mkcol: %d", code)
	}
	// pieced upload, pieces sent out of order
	if code, _, _ := req("MKCOL", rwU+"up1/", "share", "pw123456", "", nil); code != 201 {
		t.Fatalf("chunk mkcol: %d", code)
	}
	req("PUT", rwU+"up1/000000000000005-000000000000009", "share", "pw123456", "56789", nil)
	req("PUT", rwU+"up1/000000000000000-000000000000004", "share", "pw123456", "01234", nil)
	code, body, _ = req("MOVE", rwU+"up1/.file", "share", "pw123456", "", map[string]string{"Destination": rwF + "newdir/big.bin", "OC-Total-Length": "10"})
	if code != 201 {
		t.Fatalf("assemble: %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "pub", "newdir", "big.bin")); string(b) != "0123456789" {
		t.Fatalf("assembled %q", b)
	}
	// missing piece is rejected; destination outside the share is rejected
	req("MKCOL", rwU+"up2/", "share", "pw123456", "", nil)
	req("PUT", rwU+"up2/000000000000005-000000000000009", "share", "pw123456", "56789", nil)
	if code, _, _ := req("MOVE", rwU+"up2/.file", "share", "pw123456", "", map[string]string{"Destination": rwF + "gap.bin"}); code != 400 {
		t.Fatalf("gap: %d", code)
	}
	req("PUT", rwU+"up2/000000000000000-000000000000004", "share", "pw123456", "01234", nil)
	if code, _, _ := req("MOVE", rwU+"up2/.file", "share", "pw123456", "", map[string]string{"Destination": ts.URL + "/s/" + ro["id"].(string) + "/dav/files/share/x.bin"}); code == 201 {
		t.Fatal("cross-share destination accepted")
	}
	// checksum PATCH
	_, _, h := req("PATCH", rwF+"a.txt", "share", "pw123456", "", map[string]string{"X-Recalculate-Hash": "sha1"})
	if h.Get("OC-Checksum") != "SHA1:aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d" {
		t.Fatalf("sha1: %q", h.Get("OC-Checksum"))
	}
	// read-only share: list ok, uploads refused
	if code, _, _ := req("PROPFIND", roF, "", "", "", d1); code != 207 {
		t.Fatalf("ro list: %d", code)
	}
	if code, _, _ := req("PUT", roF+"x.txt", "", "", "x", nil); code != 403 {
		t.Fatalf("ro put: %d", code)
	}
	if code, _, _ := req("MKCOL", base(ro)+"uploads/share/u/", "", "", "", nil); code != 403 {
		t.Fatalf("ro chunk: %d", code)
	}
}
