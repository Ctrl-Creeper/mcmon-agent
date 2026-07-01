#!/bin/sh
set -eu

SERVICE_NAME="mcmon-agent"
INSTALL_DIR=""
CONFIG_PATH=""
REPO="Ctrl-Creeper/mcmon-agent"
VERSION="latest"
HOST_URL=""
AGENT_ID=""
TOKEN=""
CONFIG_BASE64=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --service-name) SERVICE_NAME="$2"; shift 2 ;;
    --install-dir) INSTALL_DIR="$2"; shift 2 ;;
    --config) CONFIG_PATH="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --host-url) HOST_URL="$2"; shift 2 ;;
    --agent-id) AGENT_ID="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --config-base64) CONFIG_BASE64="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "Please run as root, for example: curl ... | sudo sh" >&2
  exit 1
fi
if [ -z "$HOST_URL" ] || [ -z "$TOKEN" ] || [ -z "$CONFIG_BASE64" ]; then
  echo "Missing required --host-url, --token, or --config-base64" >&2
  exit 1
fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  armv7l) ARCH="armv7" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
if [ "$OS" = "darwin" ] && [ "$ARCH" = "armv7" ]; then
  echo "Unsupported macOS architecture: $(uname -m)" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1 || true)"
fi
if [ -z "$VERSION" ]; then
  echo "Unable to resolve latest version from https://api.github.com/repos/${REPO}/releases/latest" >&2
  echo "Check that the repository exists and has at least one GitHub Release." >&2
  exit 1
fi

BINARY_NAME="mcmon-agent-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY_NAME}"

echo "Installing mcmon-agent ${VERSION}"
echo "Host: ${HOST_URL}"
echo "Service: ${SERVICE_NAME}"

if [ "$OS" = "linux" ]; then
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemd is required on Linux" >&2
    exit 1
  fi
  INSTALL_DIR="${INSTALL_DIR:-/opt/mcmon-agent}"
  CONFIG_PATH="${CONFIG_PATH:-/etc/mcmon-agent/config.json}"
  BIN_PATH="${INSTALL_DIR}/mcmon-agent"

  echo "Install dir: ${INSTALL_DIR}"
  systemctl stop "${SERVICE_NAME}.service" >/dev/null 2>&1 || true
  systemctl disable "${SERVICE_NAME}.service" >/dev/null 2>&1 || true

  mkdir -p "$INSTALL_DIR" "$(dirname "$CONFIG_PATH")"
  curl -fL "$URL" -o "$BIN_PATH"
  chmod 0755 "$BIN_PATH"

  printf '%s' "$CONFIG_BASE64" | base64 -d > "$CONFIG_PATH"
  chmod 0600 "$CONFIG_PATH"

  cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=MCMon Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_PATH} --config ${CONFIG_PATH} --host-url ${HOST_URL} --agent-id ${AGENT_ID} --token ${TOKEN}
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable --now "${SERVICE_NAME}.service"
  echo "mcmon-agent installed and started."
elif [ "$OS" = "darwin" ]; then
  INSTALL_DIR="${INSTALL_DIR:-/usr/local/mcmon-agent}"
  CONFIG_PATH="${CONFIG_PATH:-/usr/local/etc/mcmon-agent/config.json}"
  BIN_PATH="${INSTALL_DIR}/mcmon-agent"
  PLIST="/Library/LaunchDaemons/com.mcmon.${SERVICE_NAME}.plist"

  echo "Install dir: ${INSTALL_DIR}"
  launchctl bootout system "$PLIST" >/dev/null 2>&1 || true

  mkdir -p "$INSTALL_DIR" "$(dirname "$CONFIG_PATH")"
  curl -fL "$URL" -o "$BIN_PATH"
  chmod 0755 "$BIN_PATH"

  printf '%s' "$CONFIG_BASE64" | base64 -d > "$CONFIG_PATH"
  chmod 0600 "$CONFIG_PATH"

  cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.mcmon.${SERVICE_NAME}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${BIN_PATH}</string>
    <string>--config</string>
    <string>${CONFIG_PATH}</string>
    <string>--host-url</string>
    <string>${HOST_URL}</string>
    <string>--agent-id</string>
    <string>${AGENT_ID}</string>
    <string>--token</string>
    <string>${TOKEN}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/var/log/${SERVICE_NAME}.log</string>
  <key>StandardErrorPath</key>
  <string>/var/log/${SERVICE_NAME}.err.log</string>
</dict>
</plist>
EOF
  chmod 0644 "$PLIST"
  launchctl bootstrap system "$PLIST"
  echo "mcmon-agent installed and started."
else
  echo "Unsupported OS: ${OS}" >&2
  exit 1
fi
