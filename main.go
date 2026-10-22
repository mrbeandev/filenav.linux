// filenav — a tiny, single-binary, self-hosted web file manager.
//
// Design goals: very low RAM/CPU (no DB, no runtime deps), streaming
// up/downloads of any size (resumable chunked uploads, HTTP Range downloads),
// public share links (optionally with upload), passkey + passcode auth.
package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

// set at build time: -ldflags "-X main.version=v1.0.0"
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `filenav %s: lightweight self-hosted web file manager

Usage:
  filenav [serve] [flags]          run the server (default command)
  filenav setup                    install as a systemd service (or show status if installed)
  filenav status                   show service status and health
  filenav start | stop | restart   control the service
  filenav uninstall                remove the service (keeps files and config)
  filenav update                   update to the latest release (--check to only check)
  filenav config                   print the effective configuration
  filenav passcode                 set/change the login passcode
  filenav reset-passkeys           remove all registered passkeys
  filenav logout-all               invalidate all sessions
  filenav version                  print the version
  filenav help                     show this help

Config file: ~/.filenav_linux/config.yml (created on first run; JSON also supported)
Docs: https://github.com/mrbeandev/filenav.linux
`, version)
}

func main() {
	log.SetFlags(log.LstdFlags)
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	// "filenav <cmd> -h" for commands without their own flags
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		switch cmd {
		case "status", "restart", "stop", "start", "uninstall", "version", "reset-passkeys", "logout-all":
			usage()
			return
		}
	}
	var err error
	switch cmd {
	case "serve", "run":
		err = runServe(args)
	case "setup", "install":
		err = runSetup(args)
	case "status":
		err = runStatus(args)
	case "restart", "stop", "start":
		err = runServiceCmd(cmd)
	case "uninstall":
		err = runUninstall()
	case "config":
		err = runPrintConfig(args)
	case "passcode", "reset-passkeys", "logout-all":
		err = runStateCmd(cmd, args)
	case "update", "upgrade":
		err = runUpdate(args)
	case "version", "--version", "-v":
		fmt.Println("filenav", version)
	case "help":
		err = runServe([]string{"-h"}) // prints commands + flags and exits
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func openState(cfg Config) (*Store, error) {
	if err := os.MkdirAll(cfg.Data, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	return OpenStore(filepath.Join(cfg.Data, "state.json"))
}

func runStateCmd(cmd string, args []string) error {
	cfg, _, err := loadConfig(cmd, args, false)
	if err != nil {
		return err
	}
	st, err := openState(cfg)
	if err != nil {
		return err
	}
	switch cmd {
	case "passcode":
		pc := readSecret("New passcode: ")
		if len(pc) < 4 {
			return errors.New("passcode must be at least 4 characters")
		}
		h, err := hashSecret(pc)
		if err != nil {
			return err
		}
		if err := st.Update(func(s *State) error { s.PasscodeHash = h; s.Sessions = map[string]*Session{}; return nil }); err != nil {
			return err
		}
		fmt.Println("Passcode updated; all sessions logged out.")
	case "reset-passkeys":
		_ = st.Update(func(s *State) error { s.Passkeys = nil; s.PasskeyOnly = false; return nil })
		fmt.Println("All passkeys removed; passcode login re-enabled.")
	case "logout-all":
		_ = st.Update(func(s *State) error { s.Sessions = map[string]*Session{}; return nil })
		fmt.Println("All sessions invalidated.")
	}
	fmt.Println("(state:", filepath.Join(cfg.Data, "state.json")+")")
	return nil
}

func runPrintConfig(args []string) error {
	cfg, _, err := loadConfig("config", args, false)
	if err != nil {
		return err
	}
	src := cfg.ConfigPath
	if src == "" {
		src = "(none, using defaults)"
	}
	fmt.Printf("# config file: %s\n", src)
	cfg.Passcode = strings.Repeat("*", min(len(cfg.Passcode), 8))
	os.Stdout.Write(renderConfig(cfg))
	return nil
}

func runServe(args []string) error {
	cfg, _, err := loadConfig("", args, true)
	if err != nil {
		return err
	}
	st, err := openState(cfg)
	if err != nil {
		return err
	}

	if cfg.Auth {
		var needPass bool
		st.View(func(s *State) { needPass = s.PasscodeHash == "" })
		if needPass {
			pc := cfg.Passcode
			generated := pc == ""
			if generated {
				pc = randomToken(9)
			}
			h, err := hashSecret(pc)
			if err != nil {
				return err
			}
			_ = st.Update(func(s *State) error { s.PasscodeHash = h; return nil })
			if generated {
				log.Printf("==================================================")
				log.Printf(" No passcode configured. Generated passcode: %s", pc)
				log.Printf(" Change it in Settings or with: filenav passcode")
				log.Printf("==================================================")
			} else {
				log.Printf("initial passcode set from config")
			}
		}
	} else {
		log.Printf("WARNING: authentication is DISABLED (auth: false). Anyone who can reach %s has full access.", cfg.Addr)
	}

	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return fmt.Errorf("root dir: %w", err)
	}
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	if err := root.MkdirAll(uploadTmpDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", uploadTmpDir, err)
	}

	sub, _ := fs.Sub(webFS, "web")
	srv := NewServer(cfg, cfg.Root, root, st, sub)

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

	errc := make(chan error, 1)
	go func() {
		if cfg.ConfigPath != "" {
			log.Printf("config: %s", cfg.ConfigPath)
		}
		scheme := "http"
		if cfg.TLSCert != "" {
			scheme = "https"
		}
		log.Printf("filenav %s serving %s at %s://%s", version, cfg.Root, scheme, cfg.Addr)
		var err error
		if cfg.TLSCert != "" {
			err = hs.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			err = hs.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			if errors.Is(err, syscall.EADDRINUSE) {
				err = fmt.Errorf("%w (change `port:` in %s or use -port)", err, cfg.ConfigPath)
			}
			errc <- err
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
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
