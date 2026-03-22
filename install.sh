#!/bin/sh

set -e

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
esac

# Get latest version from GitHub API
LATEST=$(curl -s https://api.github.com/repos/$REPO/releases/latest | grep '"tag_name":' | cut -d '"' -f 4)

# Compose download URL
TARBALL="${BIN}_${LATEST#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$LATEST/$TARBALL"

echo "installing $BIN $LATEST for $OS/$ARCH..."
TMPDIR=$(mktemp -d)
curl -sL "$URL" | tar xz -C "$TMPDIR"
# Find the binary (may be at top level or nested)
BIN_PATH=$(find "$TMPDIR" -name "$BIN" -type f | head -1)
if [ -z "$BIN_PATH" ]; then
  echo "error: failed to find $BIN in archive"
  rm -rf "$TMPDIR"
  exit 1
fi
mv "$BIN_PATH" ./$BIN
chmod +x ./$BIN
rm -rf "$TMPDIR"

# Try installing to /usr/local/bin or prompt fallback
INSTALL_DIR=""
if [ -w /usr/local/bin ]; then
  mv $BIN /usr/local/bin/
  INSTALL_DIR="/usr/local/bin"
  echo "installed to /usr/local/bin/$BIN"
else
  echo "note: cannot write to /usr/local/bin, installing to ~/.local/bin (you may need to add it to PATH)"
  mkdir -p ~/.local/bin
  mv $BIN ~/.local/bin/
  INSTALL_DIR="$HOME/.local/bin"
  echo "installed to ~/.local/bin/$BIN"
fi

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
