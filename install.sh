#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="$HOME/.local/bin"
ICON_BASE="$HOME/.local/share/icons/hicolor"
APP_DIR="$HOME/.local/share/applications"
SYSTEMD_DIR="$HOME/.config/systemd/user"

echo "==> Building VoxStilus (Talk-n' Type) static Go binary..."
(cd "$REPO_DIR/go" && CGO_ENABLED=0 go build -o voxstilus .)

mkdir -p "$BIN_DIR" "$APP_DIR" "$SYSTEMD_DIR"
install -m 0755 "$REPO_DIR/go/voxstilus" "$BIN_DIR/voxstilus"
cat > "$BIN_DIR/voxstilus-settings" <<'EOF'
#!/bin/sh
exec "$HOME/.local/bin/voxstilus" --settings "$@"
EOF
chmod +x "$BIN_DIR/voxstilus-settings"

echo "==> Installing VoxStilus icons..."
for sz in 128 256 512; do
  mkdir -p "$ICON_BASE/${sz}x${sz}/apps"
  install -m 0644 "$REPO_DIR/assets/icons/voxstilus-${sz}.png" "$ICON_BASE/${sz}x${sz}/apps/voxstilus.png"
done
if [ -f /usr/share/icons/hicolor/index.theme ] && [ ! -f "$ICON_BASE/index.theme" ]; then
  cp /usr/share/icons/hicolor/index.theme "$ICON_BASE/index.theme"
fi
gtk-update-icon-cache -f -t "$ICON_BASE" 2>/dev/null || true

# Match the browser --app window app_id so COSMIC / Niri / KDE docks keep the VoxStilus icon.
APP_ID="chrome-127.0.0.1__-Default"
if command -v brave-origin >/dev/null 2>&1 || command -v brave >/dev/null 2>&1; then
  APP_ID="brave-127.0.0.1__-Default"
fi

echo "==> Installing desktop launcher ($APP_ID.desktop)..."
cat > "$APP_DIR/${APP_ID}.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=VoxStilus
GenericName=Talk-n' Type Dictation Settings
Comment=Configure VoxStilus (Talk-n' Type) hold-to-talk key, models, and voice training
Exec=$BIN_DIR/voxstilus --settings
Icon=$ICON_BASE/256x256/apps/voxstilus.png
Terminal=false
Categories=Utility;
Keywords=voice;dictation;speech;keyboard;talk;
StartupNotify=true
StartupWMClass=$APP_ID
EOF
update-desktop-database "$APP_DIR" 2>/dev/null || true

echo "==> Installing systemd --user service..."
install -m 0644 "$REPO_DIR/systemd/talk-to-type.service" "$SYSTEMD_DIR/talk-to-type.service"
systemctl --user daemon-reload
systemctl --user enable --now talk-to-type.service

echo ""
echo "VoxStilus (Talk-n' Type) is installed and running!"
echo "  Settings UI:    voxstilus --settings"
echo "  Voice Trainer:  voxstilus --train"
echo ""
if ! id -nG "$USER" | tr ' ' '\n' | grep -qx "input"; then
  echo "IMPORTANT: Add your user to the 'input' group, then log out and back in:"
  echo "  sudo usermod -aG input \"\$USER\""
fi
