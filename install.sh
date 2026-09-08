#!/usr/bin/env bash
# Fresh Linux service installation only. No secret values in arguments.
# sudo ./install.sh --config /root/wmonitor.env --binary ./dist/wmonitor_linux --sha256 <independently-verified-digest>
set -euo pipefail
umask 077
CONFIG_SOURCE=""
BINARY=""
EXPECTED=""
while (($#)); do
  case "$1" in
    --config|--binary|--sha256)
      (($# >= 2)) || { echo "Missing option value" >&2; exit 1; }
      case "$1" in --config) CONFIG_SOURCE="$2";; --binary) BINARY="$2";; --sha256) EXPECTED="$2";; esac
      shift 2;;
    *) echo "Unsupported option. Use --config, --binary and --sha256; never pass secrets." >&2; exit 1;;
  esac
done
[[ $EUID -eq 0 ]] || { echo "Run from an authorized root shell." >&2; exit 1; }
[[ "$CONFIG_SOURCE" = /* && -f "$CONFIG_SOURCE" && ! -L "$CONFIG_SOURCE" ]] || { echo "An absolute regular config file is required." >&2; exit 1; }
[[ -f "$BINARY" && ! -L "$BINARY" && "$EXPECTED" =~ ^[[:xdigit:]]{64}$ ]] || { echo "Binary and trusted SHA-256 are required." >&2; exit 1; }
[[ $(stat -c %u "$CONFIG_SOURCE") = 0 ]] || { echo "Source config must be root-owned." >&2; exit 1; }
MODE=$(stat -c %a "$CONFIG_SOURCE")
(( (8#$MODE & 077) == 0 )) || { echo "Source config must not be readable by other users." >&2; exit 1; }
ACTUAL=$(sha256sum -- "$BINARY")
ACTUAL=${ACTUAL%% *}
[[ "${ACTUAL,,}" = "${EXPECTED,,}" ]] || { echo "Binary checksum mismatch; nothing installed." >&2; exit 1; }
if systemctl cat wmonitor.service >/dev/null 2>&1 || [[ -e /usr/local/bin/wmonitor || -e /etc/wmonitor/config.env ]]; then
  echo "Existing installation found. Use a reviewed maintenance-window upgrade; this installer never stops or replaces existing services." >&2
  exit 1
fi
for DIR in /etc/wmonitor /var/lib/wmonitor; do
  [[ ! -L "$DIR" ]] || { echo "Symlinked installation path refused." >&2; exit 1; }
  if [[ -e "$DIR" && $(stat -c %u "$DIR") != 0 ]]; then echo "Untrusted installation directory owner." >&2; exit 1; fi
  install -d -m 0700 -o root -g root "$DIR"
done
install -m 0600 -o root -g root "$CONFIG_SOURCE" /etc/wmonitor/config.env
install -m 0755 -o root -g root "$BINARY" /usr/local/bin/wmonitor
# Validation performs no enrollment, DB connection, service dispatch or secret output.
/usr/local/bin/wmonitor -config /etc/wmonitor/config.env -print-config
/usr/local/bin/wmonitor -config /etc/wmonitor/config.env -install
/usr/local/bin/wmonitor -start
systemctl is-active --quiet wmonitor
printf '%s\n' 'Service started. Complete native reboot/stop/uninstall checks before rollout.' 'A supplied checksum is not a signature: signed distribution remains a separate release gate.'
