#!/bin/sh

set -e

REPO="go-johnnyhe/shadow"
BIN="shadow"

# Detect platform
OS=$(uname | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

# Normalize arch names
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
esac

# Get latest version from GitHub API
LATEST=$(curl -s https://api.github.com/repos/$REPO/releases/latest | grep '"tag_name":' | cut -d '"' -f 4)

# Compose download URL
TARBALL="${BIN}_${LATEST#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$LATEST/$TARBALL"

echo "➡️  Installing $BIN $LATEST for $OS/$ARCH..."
curl -sL "$URL" | tar xz

# Try installing to /usr/local/bin or prompt fallback
if [ -w /usr/local/bin ]; then
  mv $BIN /usr/local/bin/
  echo "✅ Installed to /usr/local/bin/$BIN"
else
  echo "⚠️ Cannot write to /usr/local/bin, installing to ~/.local/bin (you may need to add it to PATH)"
  mkdir -p ~/.local/bin
  mv $BIN ~/.local/bin/
  echo "✅ Installed to ~/.local/bin/$BIN"
fi
