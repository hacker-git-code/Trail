#!/bin/sh
# Install or update trail on macOS or Linux:
#
#   curl -fsSL https://raw.githubusercontent.com/hacker-git-code/trail/main/install.sh | sh
#
# It downloads the latest release, checks that it runs, and puts it on your PATH.
# If trail is already installed, it updates that copy in place. Your history in
# ~/.trail is never touched.
set -eu

case "$(uname -s)" in
  Darwin) os=macos ;;
  Linux) os=linux ;;
  *) echo "trail: this installer is for macOS and Linux. On Windows, use install.ps1." >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "trail: no build for $(uname -m) yet." >&2; exit 1 ;;
esac
url="https://github.com/hacker-git-code/trail/releases/latest/download/trail-$os-$arch"

existing=$(command -v trail 2>/dev/null || true)
if [ -n "$existing" ]; then
  dest="$existing"
elif [ -w /usr/local/bin ]; then
  dest=/usr/local/bin/trail
else
  mkdir -p "$HOME/.local/bin"
  dest="$HOME/.local/bin/trail"
fi

echo "Downloading trail..."
tmp=$(mktemp "${TMPDIR:-/tmp}/trail.XXXXXX")
trap 'rm -f "$tmp"' EXIT
curl -fsSL "$url" -o "$tmp"
chmod +x "$tmp"
if ! ver=$("$tmp" version 2>/dev/null); then
  echo "trail: the downloaded file didn't run, so nothing was changed." >&2
  exit 1
fi

if [ -w "$(dirname "$dest")" ] && { [ ! -e "$dest" ] || [ -w "$dest" ]; }; then
  mv -f "$tmp" "$dest"
else
  echo "Installing to $(dirname "$dest") needs your password:"
  sudo mv -f "$tmp" "$dest"
fi

echo
echo "Installed $ver at $dest"
case ":$PATH:" in
  *":$(dirname "$dest"):"*) ;;
  *) echo
     echo "Add it to your PATH (put this in ~/.zshrc or ~/.bashrc):"
     echo "  export PATH=\"$(dirname "$dest"):\$PATH\"" ;;
esac
echo
if [ -n "$existing" ]; then
  echo "Updated in place. Your history is untouched."
else
  echo "Get started:"
  echo "  trail watch add ~/Documents ~/Desktop   # bring back anything deleted, from any app"
  echo "  trail watch autostart on                # keep watching in the background"
  echo "  trail install claude                    # Claude Code: who and why for every change"
fi
