// filenav — a tiny, single-binary, self-hosted web file manager.
//
// Design goals: very low RAM/CPU (no DB, no runtime deps), streaming
// up/downloads of any size (resumable chunked uploads, HTTP Range downloads),
// public share links (optionally with upload), passcode + passkey auth.
package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

var version = "dev"

type Config struct {
	Addr       string
	Root       string
	Data       string
	Origin     string // optional fixed public origin, e.g. https://files.example.com
	Title      string
	TrustProxy bool
	ChunkSize  int64
	TLSCert    string
	TLSKey     string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func loadConfig(args []string) Config {
	var c Config
	fset := flag.NewFlagSet("filenav", flag.ExitOnError)
	fset.StringVar(&c.Addr, "addr", env("FILENAV_ADDR", "127.0.0.1:8080"), "listen address (env FILENAV_ADDR)")
	fset.StringVar(&c.Root, "root", env("FILENAV_ROOT", "./files"), "directory to manage (env FILENAV_ROOT)")
	fset.StringVar(&c.Data, "data", env("FILENAV_DATA", "./filenav-data"), "state directory (env FILENAV_DATA)")
	fset.StringVar(&c.Origin, "origin", env("FILENAV_ORIGIN", ""), "public origin, e.g. https://files.example.com (env FILENAV_ORIGIN); auto-detected if empty")
	fset.StringVar(&c.Title, "title", env("FILENAV_TITLE", "filenav"), "UI title (env FILENAV_TITLE)")
	fset.BoolVar(&c.TrustProxy, "trust-proxy", envBool("FILENAV_TRUST_PROXY", false), "trust X-Forwarded-* headers (env FILENAV_TRUST_PROXY)")
	chunkMB := fset.Int("chunk-mb", func() int { n, _ := strconv.Atoi(env("FILENAV_CHUNK_MB", "16")); return n }(), "upload chunk size in MiB (env FILENAV_CHUNK_MB); keep < 100 behind Cloudflare")
	fset.StringVar(&c.TLSCert, "tls-cert", env("FILENAV_TLS_CERT", ""), "TLS certificate file (optional)")
	fset.StringVar(&c.TLSKey, "tls-key", env("FILENAV_TLS_KEY", ""), "TLS key file (optional)")
	showVer := fset.Bool("version", false, "print version")
	_ = fset.Parse(args)
	if *showVer {
		fmt.Println("filenav", version)
		os.Exit(0)
	}
	if *chunkMB < 1 {
		*chunkMB = 16
	}
	c.ChunkSize = int64(*chunkMB) << 20
	c.Origin = strings.TrimRight(c.Origin, "/")
	return c
}

func usage() {
	fmt.Fprintf(os.Stderr, `filenav %s — lightweight web file manager

Usage:
  filenav [flags]              run the server
  filenav passcode [flags]     set/change the login passcode (reads from stdin)
  filenav reset-passkeys       remove all registered passkeys
  filenav logout-all           invalidate all sessions

Run "filenav -h" for flags.
`, version)
}

func main() {
	log.SetFlags(log.LstdFlags)
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	cfg := loadConfig(args)

	if err := os.MkdirAll(cfg.Data, 0o700); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	st, err := OpenStore(filepath.Join(cfg.Data, "state.json"))
	if err != nil {
		log.Fatalf("state: %v", err)
	}

	switch cmd {
	case "":
	case "passcode":
		pc := readSecret("New passcode: ")
		if len(pc) < 4 {
			log.Fatal("passcode must be at least 4 characters")
		}
		h, err := hashSecret(pc)
		if err != nil {
			log.Fatal(err)
		}
		if err := st.Update(func(s *State) error { s.PasscodeHash = h; s.Sessions = map[string]*Session{}; return nil }); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Passcode updated; all sessions logged out.")
		return
	case "reset-passkeys":
		_ = st.Update(func(s *State) error { s.Passkeys = nil; return nil })
		fmt.Println("All passkeys removed.")
		return
	case "logout-all":
		_ = st.Update(func(s *State) error { s.Sessions = map[string]*Session{}; return nil })
		fmt.Println("All sessions invalidated.")
		return
	case "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}

	// First run: make sure there is a passcode.
	var needPass bool
	st.View(func(s *State) { needPass = s.PasscodeHash == "" })
	if needPass {
		pc := env("FILENAV_PASSCODE", "")
		generated := pc == ""
		if generated {
			pc = randomToken(9)
		}
		h, err := hashSecret(pc)
		if err != nil {
			log.Fatal(err)
		}
		_ = st.Update(func(s *State) error { s.PasscodeHash = h; return nil })
		if generated {
			log.Printf("==================================================")
			log.Printf(" No passcode was set. Generated passcode: %s", pc)
			log.Printf(" Change it with: filenav passcode")
			log.Printf("==================================================")
		}
	}

	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		log.Fatalf("root dir: %v", err)
	}
	absRoot, _ := filepath.Abs(cfg.Root)
	root, err := os.OpenRoot(absRoot)
	if err != nil {
		log.Fatalf("open root: %v", err)
	}
	if err := root.MkdirAll(uploadTmpDir, 0o700); err != nil {
		log.Fatalf("create %s: %v", uploadTmpDir, err)
	}

	sub, _ := fs.Sub(webFS, "web")
	srv := NewServer(cfg, absRoot, root, st, sub)

	hs := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No Read/WriteTimeout on purpose: large transfers can take hours.
		MaxHeaderBytes: 64 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go srv.janitor(ctx)

	go func() {
		log.Printf("filenav %s serving %s on %s", version, absRoot, cfg.Addr)
		var err error
		if cfg.TLSCert != "" {
			err = hs.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			err = hs.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Print("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
}

func readSecret(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	isTTY := false
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		isTTY = true
		c := exec.Command("stty", "-echo")
		c.Stdin = os.Stdin
		_ = c.Run()
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if isTTY {
		c := exec.Command("stty", "echo")
		c.Stdin = os.Stdin
		_ = c.Run()
		fmt.Fprintln(os.Stderr)
	}
	return strings.TrimRight(line, "\r\n")
}
