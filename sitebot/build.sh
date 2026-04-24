#!/bin/bash
set -e

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FIFO_PATH="${ROOT_DIR}/etc/goftpd.sitebot.fifo"

echo "╔══════════════════════════════════════════════╗"
echo "║   GoFTPd Sitebot Build                       ║"
echo "╚══════════════════════════════════════════════╝"
echo ""

# Step 0: Check if Go is installed
if command -v go >/dev/null 2>&1; then
    echo "✅ Go already installed: $(go version)"
elif [ -x /usr/local/go/bin/go ]; then
    export PATH=$PATH:/usr/local/go/bin
    echo "✅ Go already installed at /usr/local/go: $(go version)"
else
    echo "⚠️  Go not found. Installing via official tarball..."

    GO_VERSION="1.26.2"

    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) echo "❌ Unsupported architecture: $ARCH"; exit 1 ;;
    esac

    GO_TARBALL="go${GO_VERSION}.${OS}-${ARCH}.tar.gz"
    GO_URL="https://go.dev/dl/${GO_TARBALL}"

    echo "📦 Downloading: $GO_URL"
    curl -fL -o "$GO_TARBALL" "$GO_URL" || { echo "❌ Download failed"; exit 1; }

    echo "📂 Extracting Go to /usr/local/go"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "$GO_TARBALL" || { echo "❌ Extract failed"; exit 1; }

    PROFILE="$HOME/.bashrc"
    [ "$OS" = "darwin" ] && PROFILE="$HOME/.zshrc"

    if ! grep -q "/usr/local/go/bin" "$PROFILE" 2>/dev/null; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> "$PROFILE"
    fi

    export PATH=$PATH:/usr/local/go/bin
    rm -f "$GO_TARBALL"

    if ! command -v go >/dev/null 2>&1; then
        echo "❌ Go installation failed — add /usr/local/go/bin to your PATH"
        exit 1
    fi

    echo "✅ Go successfully installed: $(go version)"
fi

echo ""

echo ""
echo "Step 1: Ensure Go module exists and is correct..."
EXPECTED_MODULE="goftpd/sitebot"

if [ ! -f "go.mod" ]; then
    echo "ℹ️  No go.mod found, creating one..."
    go mod init "$EXPECTED_MODULE"
else
    CURRENT_MODULE=$(awk '/^module /{print $2; exit}' go.mod)
    if [ "$CURRENT_MODULE" != "$EXPECTED_MODULE" ]; then
        echo "ℹ️  Updating module path from '$CURRENT_MODULE' to '$EXPECTED_MODULE'"
        rm -f go.mod go.sum
        go mod init "$EXPECTED_MODULE"
    else
        echo "✅ go.mod already correct: $CURRENT_MODULE"
    fi
fi

echo ""
echo "Step 2: Download dependencies..."
go mod download || true

echo ""
echo "Step 3: Tidy modules..."
go mod tidy

echo ""
echo "Step 4: Detect build target..."
MAIN_FILE=$(grep -Rsl --include="*.go" '^package main$' . | head -n 1 || true)

if [ -z "$MAIN_FILE" ]; then
    echo "❌ Error: Could not find any Go file with 'package main'"
    echo "Run: find . -name '*.go'"
    exit 1
fi

MAIN_DIR=$(dirname "$MAIN_FILE")
echo "✅ Found main package in: $MAIN_DIR"

echo ""
echo "Step 5: Build..."
go build -o sitebot "$MAIN_DIR"

if [ -f sitebot ]; then
    echo ""
    echo "╔════════════════════════════════════════════╗"
    echo "║   ✅ SITEBOT BUILD SUCCESS                 ║"
    echo "╚════════════════════════════════════════════╝"
    echo ""
    ls -lh sitebot
    echo ""
    echo "Make sure the FIFO exists:"
    echo "  mkdir -p $(dirname "${FIFO_PATH}")"
    echo "  [ -p ${FIFO_PATH} ] || mkfifo ${FIFO_PATH}"
    echo "  chmod 666 ${FIFO_PATH}"
    echo ""
    echo "Use this in BOTH daemon and sitebot config:"
    echo "  event_fifo: ${FIFO_PATH}"
    echo ""
    echo "Run:"
    echo "  ./sitebot -config ./etc/config.yml"
else
    echo "❌ Build failed"
    exit 1
fi
