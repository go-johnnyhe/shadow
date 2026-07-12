#!/bin/sh

set -eu

REPO="go-johnnyhe/shadow"
BIN="shadow"
AUTO_SETUP=true

# Parse arguments
while [ $# -gt 0 ]; do
  case "$1" in
    --no-vim-setup)
      AUTO_SETUP=false
      shift
      ;;
    *)
      shift
      ;;
  esac
done

# Detect platform
OS=$(uname | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

# Normalize arch names
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  *)
    echo "error: unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

case "$OS" in
  darwin | linux) ;;
  *)
    echo "error: unsupported operating system: $OS" >&2
    exit 1
    ;;
esac

for command_name in curl tar; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "error: $command_name is required to install $BIN" >&2
    exit 1
  fi
done

# Get latest version from GitHub API
LATEST=$(curl -fsSL --retry 3 "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$LATEST" ]; then
  echo "error: could not determine the latest Shadow release" >&2
  exit 1
fi

# Compose download URL
TARBALL="${BIN}_${LATEST#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$LATEST/$TARBALL"

echo "installing $BIN $LATEST for $OS/$ARCH..."
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT HUP INT TERM
ARCHIVE="$TMPDIR/$TARBALL"
if ! curl -fL --retry 3 "$URL" -o "$ARCHIVE"; then
  echo "error: failed to download $URL" >&2
  exit 1
fi
if ! tar xzf "$ARCHIVE" -C "$TMPDIR"; then
  echo "error: downloaded release archive is invalid" >&2
  exit 1
fi
# Find the binary (may be at top level or nested)
BIN_PATH=$(find "$TMPDIR" -name "$BIN" -type f | head -1)
if [ -z "$BIN_PATH" ]; then
  echo "error: failed to find $BIN in archive" >&2
  exit 1
fi
chmod +x "$BIN_PATH"

# Try installing to /usr/local/bin or prompt fallback
INSTALL_DIR=""
if [ -w /usr/local/bin ]; then
  mv "$BIN_PATH" "/usr/local/bin/$BIN"
  INSTALL_DIR="/usr/local/bin"
  echo "installed to /usr/local/bin/$BIN"
else
  echo "note: cannot write to /usr/local/bin, installing to ~/.local/bin (you may need to add it to PATH)"
  mkdir -p ~/.local/bin
  mv "$BIN_PATH" "$HOME/.local/bin/$BIN"
  INSTALL_DIR="$HOME/.local/bin"
  echo "installed to ~/.local/bin/$BIN"
fi

trap - EXIT HUP INT TERM
rm -rf "$TMPDIR"

# Auto-setup vim/nvim if requested and editors are detected
if [ "$AUTO_SETUP" = true ]; then
  HAS_VIM=false
  HAS_NVIM=false

  if command -v vim >/dev/null 2>&1; then
    HAS_VIM=true
  fi

  if command -v nvim >/dev/null 2>&1; then
    HAS_NVIM=true
  fi

  if [ "$HAS_VIM" = true ] || [ "$HAS_NVIM" = true ]; then
    echo ""
    echo "setting up editor integration..."

    if "$INSTALL_DIR/$BIN" vimSetup --auto 2>/dev/null; then
      if [ "$HAS_VIM" = true ] && [ "$HAS_NVIM" = true ]; then
        echo "done: vim and neovim configured for live collaboration"
      elif [ "$HAS_NVIM" = true ]; then
        echo "done: neovim configured for live collaboration"
      else
        echo "done: vim configured for live collaboration"
      fi
    else
      echo "note: could not configure editor (you can run 'shadow vimSetup' manually later)"
    fi
  fi
fi

# Auto-setup MCP for AI agents (Claude Code, Cursor)
if "$INSTALL_DIR/$BIN" mcp install 2>/dev/null; then
  echo "done: MCP configured for AI agents"
else
  echo "note: could not configure MCP (you can run 'shadow mcp install' manually later)"
fi

echo ""
echo "shadow is ready! run: shadow"
