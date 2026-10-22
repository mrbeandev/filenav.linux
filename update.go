package main

// `filenav update`: compare the running version with the latest GitHub
// release, download the matching binary, verify its SHA-256 against the
// release's SHA256SUMS, atomically replace the executable and restart the
// service if one is installed.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const repo = "mrbeandev/filenav.linux"

// overridable in tests
var (
	releaseAPI      = "https://api.github.com/repos/" + repo + "/releases/latest"
	releaseTagAPI   = "https://api.github.com/repos/" + repo + "/releases/tags/"
	updateClient    = &http.Client{Timeout: 10 * time.Minute}
	restartServiceF = restartIfInstalled
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// parseVersion turns "v1.2.3" (optionally with -suffix) into comparable parts.
// ok is false for non-release builds such as "dev" or git describe output.
func parseVersion(v string) (parts [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	f := strings.Split(v, ".")
	if len(f) != 3 {
		return parts, false
	}
	for i, s := range f {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// newer reports whether version a is newer than b.
func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func assetName() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("release binaries are Linux-only (this is %s)", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64", "386":
		return "filenav-linux-" + runtime.GOARCH, nil
	case "arm":
		return "filenav-linux-armv7", nil
	}
	return "", fmt.Errorf("no release binary for %s", runtime.GOARCH)
}

func fetchRelease(tag string) (*ghRelease, error) {
	url := releaseAPI
	if tag != "" {
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		url = releaseTagAPI + tag
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "filenav/"+version)
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	c := &http.Client{Timeout: 30 * time.Second, Transport: updateClient.Transport}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404:
		if tag != "" {
			return nil, fmt.Errorf("release %s not found", tag)
		}
		return nil, errors.New("no releases published yet")
	case resp.StatusCode == 403 || resp.StatusCode == 429:
		return nil, errors.New("GitHub API rate limit reached; try again later or set GITHUB_TOKEN")
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("GitHub API: %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("bad release data: %w", err)
	}
	return &rel, nil
}

func download(url string, w io.Writer, limit int64) (int64, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "filenav/"+version)
	resp, err := updateClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.Copy(w, io.LimitReader(resp.Body, limit))
}

// expectedSum finds the hash for name in a SHA256SUMS file.
func expectedSum(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func runUpdate(args []string) error {
	fset := flag.NewFlagSet("filenav update", flag.ExitOnError)
	check := fset.Bool("check", false, "only check, don't install")
	yes := fset.Bool("yes", false, "don't ask for confirmation")
	force := fset.Bool("force", false, "install even if not newer (e.g. reinstall, or replace a dev build)")
	want := fset.String("version", "", "install a specific version, e.g. v1.0.0")
	_ = fset.Parse(args)

	name, err := assetName()
	if err != nil {
		return err
	}
	fmt.Printf("Current version: %s\n", version)
	rel, err := fetchRelease(*want)
	if err != nil {
		return err
	}
	latest, okL := parseVersion(rel.TagName)
	if !okL {
		return fmt.Errorf("unexpected release tag %q", rel.TagName)
	}
	label := "Latest release:  "
	if *want != "" {
		label = "Requested:       "
	}
	fmt.Printf("%s%s\n", label, rel.TagName)

	cur, okC := parseVersion(version)
	switch {
	case *want != "":
		// explicit version: allow downgrade/reinstall
	case !okC && !*force:
		fmt.Println("\nThis is a development build, so it can't be compared with releases.")
		fmt.Println("Run `filenav update --force` to replace it with the latest release.")
		return nil
	case okC && !newer(latest, cur) && !*force:
		fmt.Println("\nYou're up to date.")
		return nil
	}
	if *check {
		fmt.Printf("\nUpdate available: %s -> %s\n%s\nRun `filenav update` to install it.\n", version, rel.TagName, rel.HTMLURL)
		return nil
	}

	var binURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			binURL = a.URL
		case "SHA256SUMS":
			sumsURL = a.URL
		}
	}
	if binURL == "" {
		return fmt.Errorf("release %s has no %s binary", rel.TagName, name)
	}
	if sumsURL == "" {
		return fmt.Errorf("release %s has no SHA256SUMS; refusing to install an unverified binary", rel.TagName)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	if err := checkWritable(dir); err != nil {
		return fmt.Errorf("cannot write to %s (%v). Try: sudo filenav update", dir, err)
	}

	if !*yes && isTerminal(os.Stdin) {
		fmt.Printf("\nInstall %s over %s? [Y/n] ", rel.TagName, exe)
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a == "n" || a == "no" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	var sums strings.Builder
	if _, err := download(sumsURL, &sums, 1<<20); err != nil {
		return err
	}
	want256 := expectedSum(sums.String(), name)
	if want256 == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", name)
	}

	fmt.Printf("Downloading %s...\n", name)
	tmp, err := os.CreateTemp(dir, ".filenav-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	h := sha256.New()
	n, err := download(binURL, io.MultiWriter(tmp, h), 200<<20)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want256 {
		return fmt.Errorf("checksum mismatch (got %s, want %s); not installing", got, want256)
	}
	fmt.Printf("Verified SHA-256 (%.1f MB)\n", float64(n)/(1<<20))

	mode := os.FileMode(0o755)
	if st, err := os.Stat(exe); err == nil {
		mode = st.Mode().Perm() | 0o111
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	// rename over the running binary is safe on Linux: the old inode stays
	// alive until the running process exits.
	if err := os.Rename(tmpName, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	fmt.Printf("Installed %s -> %s\n", rel.TagName, exe)
	restartServiceF(exe)
	return nil
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".filenav-wtest-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// restartIfInstalled restarts the systemd service when it runs this binary.
func restartIfInstalled(exe string) {
	m, err := detectMode()
	if err != nil {
		return
	}
	b, err := os.ReadFile(m.unitPath)
	if err != nil {
		// a system service may exist while we're updating as root via another path
		fmt.Println("No filenav service found; restart filenav yourself if it's running.")
		return
	}
	if !strings.Contains(string(b), "ExecStart="+exe+" ") {
		fmt.Printf("Note: the service runs a different binary (%s); not restarting it.\n", m.unitPath)
		return
	}
	if active, _ := m.systemctl("is-active", serviceName); active != "active" {
		fmt.Println("Service is not running; it will use the new version next time it starts.")
		return
	}
	if out, err := m.systemctl("restart", serviceName); err != nil {
		fmt.Printf("Could not restart the service: %v %s\nRun: %s restart %s\n", err, out, strings.Join(m.ctl, " "), serviceName)
		return
	}
	fmt.Println("Restarted the filenav service.")
}
