package main

// Configuration: built-in defaults < config file < FILENAV_* env vars < command-line flags.
//
// The config file lives at ~/.filenav_linux/config.yml (or config.yaml / config.json).
// The YAML reader understands the flat "key: value" format filenav writes itself
// (comments, quotes, booleans, numbers), so no third-party YAML library is needed.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Config struct {
	Root         string
	Data         string
	Address      string
	Port         int
	Origin       string
	Title        string
	TrustProxy   bool
	ChunkMB      int
	MaxUploadMB  int64
	TLSCert      string
	TLSKey       string
	Auth         bool
	Passkeys     bool
	Passcode     string // initial passcode, only used while no passcode is stored yet
	ReadOnly     bool
	ShowHidden   bool
	SessionDays  int
	Shares       bool
	ShareUploads bool
	Analyzer     bool

	// derived
	Addr       string
	ChunkSize  int64
	ConfigPath string // file actually loaded ("" if none)
}

func defaultConfig() Config {
	home := userHome()
	return Config{
		Root:         filepath.Join(home, "filenav"),
		Data:         filepath.Join(home, ".filenav_linux", "data"),
		Address:      "127.0.0.1",
		Port:         8080,
		Title:        "filenav",
		ChunkMB:      16,
		Auth:         true,
		Passkeys:     true,
		ShowHidden:   true,
		SessionDays:  30,
		Shares:       true,
		ShareUploads: true,
		Analyzer:     true,
	}
}

func userHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

func configDir() string { return filepath.Join(userHome(), ".filenav_linux") }

// findConfig returns the first existing default config file, or the default .yml path.
func findConfig(dir string) (string, bool) {
	for _, n := range []string{"config.yml", "config.yaml", "config.json"} {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return filepath.Join(dir, "config.yml"), false
}

func expandHome(p string) string {
	if p == "~" {
		return userHome()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(userHome(), p[2:])
	}
	return p
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y":
		return true, nil
	case "0", "false", "no", "off", "n", "":
		return false, nil
	}
	return false, fmt.Errorf("expected true/false, got %q", v)
}

// setters maps config keys to functions that parse a string value into the Config.
func (c *Config) setters() map[string]func(string) error {
	str := func(dst *string, path bool) func(string) error {
		return func(v string) error {
			if path && v != "" {
				v = expandHome(v)
			}
			*dst = v
			return nil
		}
	}
	boolean := func(dst *bool) func(string) error {
		return func(v string) error { b, err := parseBool(v); *dst = b; return err }
	}
	integer := func(dst *int) func(string) error {
		return func(v string) error { n, err := strconv.Atoi(strings.TrimSpace(v)); *dst = n; return err }
	}
	return map[string]func(string) error{
		"root":        str(&c.Root, true),
		"data_dir":    str(&c.Data, true),
		"address":     str(&c.Address, false),
		"port":        integer(&c.Port),
		"origin":      str(&c.Origin, false),
		"title":       str(&c.Title, false),
		"trust_proxy": boolean(&c.TrustProxy),
		"chunk_mb":    integer(&c.ChunkMB),
		"max_upload_mb": func(v string) error {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			c.MaxUploadMB = n
			return err
		},
		"tls_cert":      str(&c.TLSCert, true),
		"tls_key":       str(&c.TLSKey, true),
		"auth":          boolean(&c.Auth),
		"passkeys":      boolean(&c.Passkeys),
		"passcode":      str(&c.Passcode, false),
		"read_only":     boolean(&c.ReadOnly),
		"show_hidden":   boolean(&c.ShowHidden),
		"session_days":  integer(&c.SessionDays),
		"shares":        boolean(&c.Shares),
		"share_uploads": boolean(&c.ShareUploads),
		"analyzer":      boolean(&c.Analyzer),
	}
}

// loadFile applies a YAML or JSON config file.
func (c *Config) loadFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	kv := map[string]string{}
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for k, v := range m {
			if v == nil {
				kv[k] = ""
			} else {
				kv[k] = fmt.Sprint(v)
			}
		}
	} else if kv, err = parseFlatYAML(b); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	set := c.setters()
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fn, ok := set[strings.ToLower(k)]
		if !ok {
			fmt.Fprintf(os.Stderr, "config: ignoring unknown key %q in %s\n", k, path)
			continue
		}
		if err := fn(kv[k]); err != nil {
			return fmt.Errorf("%s: %s: %w", path, k, err)
		}
	}
	return nil
}

// parseFlatYAML reads "key: value" lines with # comments and optional quotes.
func parseFlatYAML(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || line == "---" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: expected \"key: value\"", ln)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch {
		case strings.HasPrefix(v, `"`):
			s, err := strconv.QuotedPrefix(v)
			if err != nil {
				return nil, fmt.Errorf("line %d: bad quoted string", ln)
			}
			v, _ = strconv.Unquote(s)
		case strings.HasPrefix(v, "'"):
			end := strings.Index(v[1:], "'")
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated string", ln)
			}
			v = v[1 : end+1]
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			if v == "~" || v == "null" {
				v = ""
			}
		}
		out[k] = v
	}
	return out, sc.Err()
}

var envKeys = map[string]string{
	"FILENAV_ROOT": "root", "FILENAV_DATA": "data_dir", "FILENAV_ADDRESS": "address", "FILENAV_PORT": "port",
	"FILENAV_ORIGIN": "origin", "FILENAV_TITLE": "title", "FILENAV_TRUST_PROXY": "trust_proxy",
	"FILENAV_CHUNK_MB": "chunk_mb", "FILENAV_MAX_UPLOAD_MB": "max_upload_mb", "FILENAV_TLS_CERT": "tls_cert",
	"FILENAV_TLS_KEY": "tls_key", "FILENAV_AUTH": "auth", "FILENAV_PASSKEYS": "passkeys",
	"FILENAV_PASSCODE": "passcode", "FILENAV_READ_ONLY": "read_only", "FILENAV_SHOW_HIDDEN": "show_hidden",
	"FILENAV_SESSION_DAYS": "session_days", "FILENAV_SHARES": "shares", "FILENAV_SHARE_UPLOADS": "share_uploads",
	"FILENAV_ANALYZER": "analyzer",
}

func (c *Config) applyEnv() error {
	set := c.setters()
	for env, key := range envKeys {
		if v, ok := os.LookupEnv(env); ok && v != "" {
			if err := set[key](v); err != nil {
				return fmt.Errorf("%s: %w", env, err)
			}
		}
	}
	// legacy FILENAV_ADDR=host:port
	if v := os.Getenv("FILENAV_ADDR"); v != "" {
		return c.setAddr(v)
	}
	return nil
}

func (c *Config) setAddr(hp string) error {
	h, p, err := net.SplitHostPort(hp)
	if err != nil {
		return fmt.Errorf("address %q: %w", hp, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return fmt.Errorf("port %q: %w", p, err)
	}
	c.Address, c.Port = h, n
	return nil
}

func (c *Config) finalize() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range", c.Port)
	}
	if c.ChunkMB < 1 {
		c.ChunkMB = 16
	}
	if c.SessionDays < 1 {
		c.SessionDays = 30
	}
	if c.Title == "" {
		c.Title = "filenav"
	}
	if c.MaxUploadMB < 0 {
		c.MaxUploadMB = 0
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls_cert and tls_key must be set together")
	}
	c.Origin = strings.TrimRight(c.Origin, "/")
	c.Addr = net.JoinHostPort(c.Address, strconv.Itoa(c.Port))
	c.ChunkSize = int64(c.ChunkMB) << 20
	var err error
	if c.Root, err = filepath.Abs(c.Root); err != nil {
		return err
	}
	if c.Data, err = filepath.Abs(c.Data); err != nil {
		return err
	}
	return nil
}

// loadConfig builds the effective configuration for a command.
// autoCreate writes a default config file on first run if none exists.
func loadConfig(name string, args []string, autoCreate bool) (Config, *flag.FlagSet, error) {
	c := defaultConfig()
	display := name
	if display == "" {
		display = "serve"
	}
	fset := flag.NewFlagSet("filenav "+display, flag.ExitOnError)
	fset.Usage = func() {
		out := fset.Output()
		if display == "serve" {
			usage()
			fmt.Fprintln(out, "\nFlags (override the config file):")
		} else {
			fmt.Fprintf(out, "Usage: filenav %s [flags]\n\nFlags:\n", display)
		}
		fset.PrintDefaults()
	}
	cfgPath := fset.String("config", os.Getenv("FILENAV_CONFIG"), "config file (default ~/.filenav_linux/config.yml)")
	fRoot := fset.String("root", "", "folder to serve")
	fData := fset.String("data", "", "state directory")
	fAddr := fset.String("addr", "", "listen host:port (overrides address+port)")
	fAddress := fset.String("address", "", "listen address, e.g. 127.0.0.1 or 0.0.0.0")
	fPort := fset.Int("port", 0, "listen port")
	fOrigin := fset.String("origin", "", "public URL, e.g. https://files.example.com")
	fTitle := fset.String("title", "", "UI title")
	fTrust := fset.Bool("trust-proxy", false, "trust X-Forwarded-* headers")
	fChunk := fset.Int("chunk-mb", 0, "upload chunk size in MiB")
	fCert := fset.String("tls-cert", "", "TLS certificate file")
	fKey := fset.String("tls-key", "", "TLS key file")
	fNoAuth := fset.Bool("no-auth", false, "disable authentication (DANGEROUS)")
	fRO := fset.Bool("read-only", false, "read-only mode")
	if name == "setup" {
		fset.Bool("yes", false, "accept defaults without prompting")
		fset.Bool("force", false, "re-install service and binary even if already set up")
	}
	_ = fset.Parse(args)

	path, explicit := *cfgPath, *cfgPath != ""
	exists := false
	if explicit {
		path = expandHome(path)
		_, err := os.Stat(path)
		exists = err == nil
		if !exists && name == "" {
			return c, fset, fmt.Errorf("config file %s not found", path)
		}
	} else {
		path, exists = findConfig(configDir())
	}
	if exists {
		if err := c.loadFile(path); err != nil {
			return c, fset, err
		}
		c.ConfigPath = path
	} else if autoCreate {
		if err := writeConfigFile(path, c); err != nil {
			return c, fset, fmt.Errorf("create %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "Created default config: %s\n", path)
		c.ConfigPath = path
	}
	if err := c.applyEnv(); err != nil {
		return c, fset, err
	}
	var ferr error
	fset.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "root":
			c.Root = expandHome(*fRoot)
		case "data":
			c.Data = expandHome(*fData)
		case "addr":
			ferr = c.setAddr(*fAddr)
		case "address":
			c.Address = *fAddress
		case "port":
			c.Port = *fPort
		case "origin":
			c.Origin = *fOrigin
		case "title":
			c.Title = *fTitle
		case "trust-proxy":
			c.TrustProxy = *fTrust
		case "chunk-mb":
			c.ChunkMB = *fChunk
		case "tls-cert":
			c.TLSCert = expandHome(*fCert)
		case "tls-key":
			c.TLSKey = expandHome(*fKey)
		case "no-auth":
			c.Auth = !*fNoAuth
		case "read-only":
			c.ReadOnly = *fRO
		}
	})
	if ferr != nil {
		return c, fset, ferr
	}
	return c, fset, c.finalize()
}

func flagBool(fset *flag.FlagSet, name string) bool {
	f := fset.Lookup(name)
	return f != nil && f.Value.String() == "true"
}

func yamlQuote(s string) string { return strconv.Quote(s) }

// renderConfig produces a commented YAML config file.
func renderConfig(c Config) []byte {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# filenav configuration\n")
	w("# Changes take effect after a restart:  filenav restart  (or systemctl restart filenav)\n\n")
	w("# Folder shown in the file manager\n")
	w("root: %s\n\n", yamlQuote(c.Root))
	w("# Where login sessions, passkeys and share links are stored\n")
	w("data_dir: %s\n\n", yamlQuote(c.Data))
	w("# Listen address: 127.0.0.1 = only this machine (use with a reverse proxy),\n")
	w("# 0.0.0.0 = reachable from other devices on the network\n")
	w("address: %s\n", yamlQuote(c.Address))
	w("port: %d\n\n", c.Port)
	w("# Public URL when behind a reverse proxy / domain, e.g. https://files.example.com\n")
	w("# Required for passkeys to work behind a proxy, and for correct share links.\n")
	w("origin: %s\n", yamlQuote(c.Origin))
	w("# Set to true when running behind Caddy/nginx/Cloudflare\n")
	w("trust_proxy: %t\n\n", c.TrustProxy)
	w("title: %s\n\n", yamlQuote(c.Title))
	w("# --- Authentication ---\n")
	w("# auth: false turns off login completely; anyone who can reach the port has full access!\n")
	w("auth: %t\n", c.Auth)
	w("# Allow passkey sign-in (Face ID / fingerprint / Windows Hello / security keys). Needs HTTPS or localhost.\n")
	w("passkeys: %t\n", c.Passkeys)
	w("# Initial passcode. Only used on first start, while no passcode has been saved yet.\n")
	w("# After you change the passcode in the UI (or with `filenav passcode`), this value is ignored.\n")
	w("passcode: %s\n", yamlQuote(c.Passcode))
	w("# How long a login lasts\n")
	w("session_days: %d\n\n", c.SessionDays)
	w("# --- Files ---\n")
	w("# Disallow uploads, edits, renames, moves and deletes\n")
	w("read_only: %t\n", c.ReadOnly)
	w("# Show dotfiles such as .gitignore and .env\n")
	w("show_hidden: %t\n", c.ShowHidden)
	w("# Maximum size of a single upload in MB (0 = unlimited)\n")
	w("max_upload_mb: %d\n", c.MaxUploadMB)
	w("# Upload chunk size in MB (keep below 100 behind Cloudflare)\n")
	w("chunk_mb: %d\n\n", c.ChunkMB)
	w("# --- Sharing ---\n")
	w("# Enable public share links\n")
	w("shares: %t\n", c.Shares)
	w("# Allow share links that let visitors upload\n")
	w("share_uploads: %t\n\n", c.ShareUploads)
	w("# --- Storage analyzer ---\n")
	w("# Keeps a cached index of folder sizes, updated live (inotify) and checked on startup\n")
	w("analyzer: %t\n\n", c.Analyzer)
	w("# --- HTTPS without a reverse proxy (optional) ---\n")
	w("tls_cert: %s\n", yamlQuote(c.TLSCert))
	w("tls_key: %s\n", yamlQuote(c.TLSKey))
	return []byte(b.String())
}

func writeConfigFile(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var data []byte
	if strings.HasSuffix(path, ".json") {
		m := map[string]any{
			"root": c.Root, "data_dir": c.Data, "address": c.Address, "port": c.Port, "origin": c.Origin,
			"trust_proxy": c.TrustProxy, "title": c.Title, "auth": c.Auth, "passkeys": c.Passkeys,
			"passcode": c.Passcode, "session_days": c.SessionDays, "read_only": c.ReadOnly,
			"show_hidden": c.ShowHidden, "max_upload_mb": c.MaxUploadMB, "chunk_mb": c.ChunkMB,
			"shares": c.Shares, "share_uploads": c.ShareUploads, "analyzer": c.Analyzer, "tls_cert": c.TLSCert, "tls_key": c.TLSKey,
		}
		data, _ = json.MarshalIndent(m, "", "  ")
		data = append(data, '\n')
	} else {
		data = renderConfig(c)
	}
	return os.WriteFile(path, data, 0o600) // may contain the initial passcode
}
