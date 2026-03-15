#!/usr/bin/env bash
# One-command installer for Sistemo self-hosted.
# Usage: curl -sSL https://get.sistemo.io | sh
# Or:    curl -sSL https://raw.githubusercontent.com/davidestf/sistemo/main/scripts/install.sh | sh

set -e

GITHUB_REPO="${SISTEMO_REPO:-davidestf/sistemo}"
INSTALL_DIR="${SISTEMO_INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="sistemo"

# Detect OS and arch
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

if [ "$OS" != "linux" ]; then
  echo "Sistemo requires Linux with KVM support. Detected: $OS"
  exit 1
fi

if [ -n "$SISTEMO_INSTALL_URL" ]; then
  URL="$SISTEMO_INSTALL_URL"
  echo "Installing Sistemo from $URL"
else
  VERSION="${SISTEMO_VERSION:-latest}"
  if [ "$VERSION" = "latest" ]; then
    TAG=$(curl -sSL "https://api.github.com/repos/$GITHUB_REPO/releases/latest" 2>/dev/null \
      | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || true)
    [ -z "$TAG" ] && { echo "Could not detect latest version. Set SISTEMO_VERSION=vX.Y.Z"; exit 1; }
  else
    TAG="$VERSION"
  fi
  URL="https://github.com/$GITHUB_REPO/releases/download/${TAG}/sistemo-${OS}-${ARCH}"
  echo "Installing Sistemo $TAG ($OS/$ARCH)"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Downloading..."
if ! curl -sSLf -o "$TMP/sistemo" "$URL"; then
  echo "Download failed from $URL"
  echo "Build from source instead: go build -o sistemo ./cmd/sistemo"
  exit 1
fi
chmod +x "$TMP/sistemo"

if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP/sistemo" "$INSTALL_DIR/$BINARY_NAME"
else
  echo "Installing to $INSTALL_DIR (needs sudo)..."
  sudo mv "$TMP/sistemo" "$INSTALL_DIR/$BINARY_NAME"
fi

echo ""
echo "Installed: $(command -v $BINARY_NAME 2>/dev/null || echo "$INSTALL_DIR/$BINARY_NAME")"
echo ""

# Run setup (downloads Firecracker + kernel, generates SSH key, checks KVM)
echo "Running setup..."
"$INSTALL_DIR/$BINARY_NAME" install

echo ""
echo "Ready! Start with:"
echo "  sudo sistemo up"
