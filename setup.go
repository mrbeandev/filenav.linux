package main

// `filenav setup` installs filenav as a systemd service (system-wide when run
// as root, otherwise a per-user service). If it is already installed it just
// verifies everything and prints the status.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const serviceName = "filenav"

type svcMode struct {
	system   bool
	unitPath string
	binPath  string
	ctl      []string // systemctl prefix
	journal  []string
	user     string // account the service runs as (system mode)
	home     string
}

func detectMode() (svcMode, error) {
	if os.Geteuid() == 0 {
		// run as the invoking user when started via sudo, so ~/.filenav_linux is theirs
		uname := os.Getenv("SUDO_USER")
		if uname == "" || uname == "root" {
			uname = "root"
		}
		u, err := user.Lookup(uname)
		if err != nil {
			return svcMode{}, err
		}
		return svcMode{
			system: true, unitPath: "/etc/systemd/system/" + serviceName + ".service",
			binPath: "/usr/local/bin/filenav", ctl: []string{"systemctl"},
			journal: []string{"journalctl", "-u", serviceName}, user: u.Username, home: u.HomeDir,
		}, nil
	}
	home := userHome()
	return svcMode{
		unitPath: filepath.Join(home, ".config/systemd/user", serviceName+".service"),
		binPath:  filepath.Join(home, ".local/bin/filenav"),
		ctl:      []string{"systemctl", "--user"},
		journal:  []string{"journalctl", "--user", "-u", serviceName},
		home:     home,
	}, nil
}

func (m svcMode) systemctl(args ...string) (string, error) {
	cmd := exec.Command(m.ctl[0], append(m.ctl[1:], args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (m svcMode) cfgPath() string {
	p, _ := findConfig(filepath.Join(m.home, ".filenav_linux"))
	return p
}

func (m svcMode) unit(cfgPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `[Unit]
Description=filenav - lightweight web file manager
Documentation=https://github.com/mrbeandev/filenav.linux
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s serve --config %s
Restart=on-failure
RestartSec=3
# filenav uses ~10 MB; cap it so it can never hurt a small server
MemoryMax=256M
NoNewPrivileges=true
PrivateTmp=true
`, m.binPath, cfgPath)
	if m.system {
		fmt.Fprintf(&b, "User=%s\nEnvironment=HOME=%s\n", m.user, m.home)
		b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	} else {
		b.WriteString("\n[Install]\nWantedBy=default.target\n")
	}
	return b.String()
}

func ask(r *bufio.Reader, q, def string, yes bool) string {
	if yes {
		return def
	}
	if def != "" {
		fmt.Printf("%s [%s]: ", q, def)
	} else {
		fmt.Printf("%s: ", q)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func askYN(r *bufio.Reader, q string, def, yes bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	ans := ask(r, q+" ("+d+")", "", yes)
	if ans == "" {
		return def
	}
	b, err := parseBool(ans)
	if err != nil {
		return def
	}
	return b
}

func runSetup(args []string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemd (systemctl) not found; run filenav directly with `filenav serve` instead")
	}
	m, err := detectMode()
	if err != nil {
		return err
	}
	cfg, fset, err := loadConfig("setup", args, false)
	if err != nil {
		return err
	}
	yes, force := flagBool(fset, "yes"), flagBool(fset, "force")

	// When run via sudo, the config belongs to the invoking user.
	cfgPath := cfg.ConfigPath
	if cfgPath == "" {
		cfgPath = m.cfgPath()
	}

	if _, err := os.Stat(m.unitPath); err == nil && !force {
		return showStatus(m, cfgPath, true)
	}

	fmt.Println("filenav setup")
	fmt.Println("=============")
	if m.system {
		fmt.Printf("Installing a system service (runs as user %q, starts at boot).\n\n", m.user)
	} else {
		fmt.Println("Installing a user service (no root needed). Tip: run with sudo for a system-wide service.")
		fmt.Println()
	}

	r := bufio.NewReader(os.Stdin)
	if cfg.ConfigPath == "" {
		if m.system {
			cfg.Root = filepath.Join(m.home, "filenav")
			cfg.Data = filepath.Join(m.home, ".filenav_linux", "data")
		}
		cfg.Root = expandHome(ask(r, "Folder to serve", cfg.Root, yes))
		cfg.Address = ask(r, "Listen address (127.0.0.1 = local/reverse proxy, 0.0.0.0 = whole network)", cfg.Address, yes)
		for {
			p, err := strconv.Atoi(ask(r, "Port", strconv.Itoa(cfg.Port), yes))
			if err == nil && p > 0 && p < 65536 {
				cfg.Port = p
				break
			}
			fmt.Println("  please enter a number between 1 and 65535")
		}
		if !portFree(cfg.Address, cfg.Port) {
			fmt.Printf("  ! port %d looks busy. You can change it later in %s\n", cfg.Port, cfgPath)
		}
		cfg.Origin = ask(r, "Public URL if you use a domain, e.g. https://files.example.com (empty = none)", cfg.Origin, yes)
		if cfg.Origin != "" {
			cfg.TrustProxy = askYN(r, "Is filenav behind a reverse proxy (Caddy/nginx/Cloudflare)?", true, yes)
		}
		cfg.Auth = askYN(r, "Require login?", true, yes)
		if cfg.Auth {
			cfg.Passcode = ask(r, "Initial passcode (empty = generate one)", "", yes)
			if cfg.Passcode == "" {
				cfg.Passcode = randomToken(9)
				fmt.Printf("  generated passcode: %s\n", cfg.Passcode)
			}
		}
		if err := writeConfigFile(cfgPath, cfg); err != nil {
			return err
		}
		if m.system {
			chownToUser(m.user, filepath.Dir(cfgPath), cfgPath)
		}
		fmt.Printf("\n✓ wrote config %s\n", cfgPath)
	} else {
		fmt.Printf("✓ using existing config %s\n", cfgPath)
	}

	if err := os.MkdirAll(cfg.Root, 0o755); err == nil && m.system {
		chownToUser(m.user, cfg.Root)
	}

	// install the binary
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)
	if self != m.binPath {
		if err := copyBinary(self, m.binPath); err != nil {
			return fmt.Errorf("install binary to %s: %w", m.binPath, err)
		}
		fmt.Printf("✓ installed binary %s\n", m.binPath)
	}

	if err := os.MkdirAll(filepath.Dir(m.unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(m.unitPath, []byte(m.unit(cfgPath)), 0o644); err != nil {
		return err
	}
	fmt.Printf("✓ wrote service %s\n", m.unitPath)

	if out, err := m.systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v %s", err, out)
	}
	if out, err := m.systemctl("enable", "--now", serviceName); err != nil {
		return fmt.Errorf("systemctl enable: %v %s", err, out)
	}
	if force {
		_, _ = m.systemctl("restart", serviceName)
	}
	fmt.Println("✓ service enabled and started")
	if !m.system {
		fmt.Println("\nNote: user services stop when you log out unless lingering is enabled:")
		fmt.Printf("  sudo loginctl enable-linger %s\n", os.Getenv("USER"))
	}
	time.Sleep(1200 * time.Millisecond)
	fmt.Println()
	return showStatus(m, cfgPath, false)
}

func portFree(addr string, port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func chownToUser(uname string, paths ...string) {
	u, err := user.Lookup(uname)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	for _, p := range paths {
		_ = os.Chown(p, uid, gid)
	}
}

func copyBinary(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst) // atomic; works even while the old binary is running
}

// showStatus prints a health report of an installed service.
func showStatus(m svcMode, cfgPath string, already bool) error {
	active, _ := m.systemctl("is-active", serviceName)
	enabled, _ := m.systemctl("is-enabled", serviceName)
	cfg := defaultConfig()
	cfgOK := cfg.loadFile(cfgPath) == nil
	_ = cfg.finalize()

	ok := func(b bool) string {
		if b {
			return "✓"
		}
		return "✗"
	}
	if already {
		fmt.Println("filenav is already set up.")
		fmt.Println()
	}
	scope := "user"
	if m.system {
		scope = "system"
	}
	fmt.Printf("  %s service    %s (%s)\n", ok(active == "active"), active, scope)
	fmt.Printf("  %s autostart  %s\n", ok(enabled == "enabled"), enabled)
	fmt.Printf("  %s config     %s\n", ok(cfgOK), cfgPath)
	fmt.Printf("    unit       %s\n", m.unitPath)
	if b, err := os.ReadFile(m.unitPath); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "ExecStart=") {
				bin := strings.Fields(strings.TrimPrefix(l, "ExecStart="))[0]
				_, err := os.Stat(bin)
				fmt.Printf("  %s binary     %s\n", ok(err == nil), bin)
			}
		}
	}
	if cfgOK {
		fmt.Printf("    serving    %s\n", cfg.Root)
		url := "http://" + net.JoinHostPort(displayHost(cfg.Address), strconv.Itoa(cfg.Port))
		if cfg.TLSCert != "" {
			url = strings.Replace(url, "http://", "https://", 1)
		}
		healthy := probe(url + "/api/me")
		fmt.Printf("  %s reachable  %s\n", ok(healthy), url)
		if cfg.Origin != "" {
			fmt.Printf("    public URL %s\n", cfg.Origin)
		}
		if !cfg.Auth {
			fmt.Println("  ! auth is DISABLED in the config: anyone who can reach the port has full access")
		}
		if active == "active" && healthy && enabled == "enabled" && already {
			fmt.Println("\nEverything is fully set up and running.")
		}
	}
	fmt.Println()
	fmt.Println("Manage it with:")
	ctl := strings.Join(m.ctl, " ")
	if m.system {
		ctl = "sudo " + ctl
	}
	fmt.Printf("  %s status|restart|stop %s\n", ctl, serviceName)
	fmt.Printf("  %s -f        # logs\n", strings.Join(m.journal, " "))
	fmt.Printf("  filenav setup --force      # reinstall/update the service and binary\n")
	fmt.Printf("  filenav uninstall          # remove the service (keeps your files and config)\n")
	if active != "active" {
		if out, _ := exec.Command(m.journal[0], append(m.journal[1:], "-n", "15", "--no-pager")...).CombinedOutput(); len(out) > 0 {
			fmt.Printf("\nRecent logs:\n%s\n", bytes.TrimSpace(out))
		}
	}
	return nil
}

func displayHost(a string) string {
	if a == "" || a == "0.0.0.0" || a == "::" {
		return "127.0.0.1"
	}
	return a
}

func probe(url string) bool {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func runStatus(args []string) error {
	m, err := detectMode()
	if err != nil {
		return err
	}
	if _, err := os.Stat(m.unitPath); err != nil {
		// maybe installed in the other scope
		if !m.system {
			if _, err := os.Stat("/etc/systemd/system/" + serviceName + ".service"); err == nil {
				fmt.Println("filenav is installed as a system service. Run: sudo filenav status")
				return nil
			}
		}
		fmt.Println("filenav is not installed as a service. Run `filenav setup` (or `sudo filenav setup`).")
		return nil
	}
	cfg, _, err := loadConfig("status", args, false)
	path := cfg.ConfigPath
	if err != nil || path == "" {
		path = m.cfgPath()
	}
	return showStatus(m, path, false)
}

func runServiceCmd(action string) error {
	m, err := detectMode()
	if err != nil {
		return err
	}
	if _, err := os.Stat(m.unitPath); err != nil {
		return fmt.Errorf("service not installed; run `filenav setup` first")
	}
	out, err := m.systemctl(action, serviceName)
	if err != nil {
		return fmt.Errorf("%v %s", err, out)
	}
	fmt.Printf("filenav %sed\n", strings.TrimSuffix(action, "e"))
	return nil
}

func runUninstall() error {
	m, err := detectMode()
	if err != nil {
		return err
	}
	if _, err := os.Stat(m.unitPath); err != nil {
		fmt.Println("No filenav service found.")
		return nil
	}
	_, _ = m.systemctl("disable", "--now", serviceName)
	if err := os.Remove(m.unitPath); err != nil {
		return err
	}
	_, _ = m.systemctl("daemon-reload")
	fmt.Println("✓ service stopped and removed")
	fmt.Printf("  Kept: your files, the config (%s) and the binary (%s).\n", m.cfgPath(), m.binPath)
	return nil
}
