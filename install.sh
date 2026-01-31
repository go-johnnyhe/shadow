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

echo "➡️  Installing $BIN $LATEST for $OS/$ARCH..."
curl -sL "$URL" | tar xz

# Try installing to /usr/local/bin or prompt fallback
INSTALL_DIR=""
if [ -w /usr/local/bin ]; then
  mv $BIN /usr/local/bin/
  INSTALL_DIR="/usr/local/bin"
  echo "✅ Installed to /usr/local/bin/$BIN"
else
  echo "⚠️  Cannot write to /usr/local/bin, installing to ~/.local/bin (you may need to add it to PATH)"
  mkdir -p ~/.local/bin
  mv $BIN ~/.local/bin/
  INSTALL_DIR="$HOME/.local/bin"
  echo "✅ Installed to ~/.local/bin/$BIN"
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
    echo "➡️  Setting up editor integration..."
    
    if "$INSTALL_DIR/$BIN" vimSetup --auto 2>/dev/null; then
      if [ "$HAS_VIM" = true ] && [ "$HAS_NVIM" = true ]; then
        echo "✅ Vim and Neovim configured for live collaboration"
      elif [ "$HAS_NVIM" = true ]; then
        echo "✅ Neovim configured for live collaboration"
      else
        echo "✅ Vim configured for live collaboration"
      fi
    else
      echo "⚠️  Could not configure editor (you can run 'shadow vimSetup' manually later)"
    fi
  fi
fi

echo ""
echo "🎉 Shadow is ready! Try: shadow start ."
