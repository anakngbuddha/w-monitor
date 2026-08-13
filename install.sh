#!/usr/bin/env bash
set -e

# Sysmon Linux Installer Script

INSTALL_DIR="/usr/local/bin"
EXE_NAME="sysmon"
SOURCE_EXE="./wmonitor_linux"
TARGET_EXE="$INSTALL_DIR/$EXE_NAME"

# 1. Ensure root privileges
if [ "$EUID" -ne 0 ]; then
  echo "Please run as root (e.g. sudo ./install.sh)"
  exit 1
fi

if [ ! -f "$SOURCE_EXE" ]; then
    echo "Error: Cannot find $SOURCE_EXE."
    echo "Please run ./build_release.ps1 first to build the executable."
    exit 1
fi

echo "Installing Sysmon..."

# 2. Stop existing service
if systemctl is-active --quiet sysmon; then
    echo "Stopping existing sysmon service..."
    systemctl stop sysmon
fi

if systemctl is-enabled --quiet sysmon 2>/dev/null; then
    echo "Uninstalling old service registration..."
    if [ -f "$TARGET_EXE" ]; then
        "$TARGET_EXE" -uninstall || true
    fi
fi

# 3. Copy files
echo "Copying binary to $INSTALL_DIR..."
cp "$SOURCE_EXE" "$TARGET_EXE"
chmod +x "$TARGET_EXE"

# 4. Install and Start Service
echo "Registering systemd service..."
"$TARGET_EXE" -install

echo "Starting service..."
"$TARGET_EXE" -start

echo ""
echo "Sysmon has been successfully installed and started!"
echo "Dashboard is available at http://localhost:8080"
echo "Check status with: systemctl status sysmon"
echo "View logs with: journalctl -u sysmon -f"
