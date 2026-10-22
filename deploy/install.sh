#!/usr/bin/env bash
# Installs filenav as a systemd service. Run as root from the project dir
# after building (`make`), or with a prebuilt ./filenav binary.
set -euo pipefail

BIN=${1:-./filenav}
[ -x "$BIN" ] || { echo "binary $BIN not found — run 'make' first"; exit 1; }

id filenav >/dev/null 2>&1 || useradd --system --home /var/lib/filenav --shell /usr/sbin/nologin filenav
install -m 0755 "$BIN" /usr/local/bin/filenav
install -d -o filenav -g filenav -m 0750 /srv/files
install -d -o filenav -g filenav -m 0700 /var/lib/filenav
[ -f /etc/filenav.env ] || install -m 0640 -g filenav deploy/filenav.env /etc/filenav.env
install -m 0644 deploy/filenav.service /etc/systemd/system/filenav.service
systemctl daemon-reload
systemctl enable --now filenav
sleep 1
echo
echo "filenav installed. Edit /etc/filenav.env (set FILENAV_ORIGIN!) then: systemctl restart filenav"
echo "Generated passcode (first start only):"
journalctl -u filenav --no-pager | grep -o 'Generated passcode: .*' | tail -1 || true
echo "Set your own:  sudo -u filenav FILENAV_DATA=/var/lib/filenav filenav passcode"
