#!/bin/sh
set -e

REPO="pylonto/pylon"
INSTALL_DIR="/usr/local/bin"

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
    linux|darwin) ;;
    *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

BINARY="pylon-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/latest/download/${BINARY}"

echo "Downloading pylon for ${OS}/${ARCH}..."

# Fall back to ~/.local/bin if no write access to /usr/local/bin
if [ ! -w "$INSTALL_DIR" ] 2>/dev/null; then
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
fi

DEST="${INSTALL_DIR}/pylon"
if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$URL" -o "$DEST"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$DEST" "$URL"
else
    echo "Error: curl or wget required"; exit 1
fi

chmod +x "$DEST"

# Verify installation
if "$DEST" version >/dev/null 2>&1; then
    echo ""
    echo "Pylon installed successfully."
    echo ""
    echo "Get started:"
    echo "  pylon setup"
    echo ""
    echo "Documentation: https://pylon.to/docs"
else
    echo "Error: installation verification failed"
    exit 1
fi

# Check for Docker
echo ""
if ! command -v docker >/dev/null 2>&1; then
    echo "Warning: Docker not found. Pylon needs Docker to run agents."
    echo "Install: https://docs.docker.com/engine/install/"
fi

# Check if INSTALL_DIR is in PATH
case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *) echo "Note: Add ${INSTALL_DIR} to your PATH:"; echo "  export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac
