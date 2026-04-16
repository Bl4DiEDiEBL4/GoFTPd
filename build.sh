#!/bin/bash
set -e

# Visual Header
echo "╔═════════════════════════════════════════════════╗"
echo "║   GoFTPd Build - Master/Slave Architecture      ║"
echo "╚═════════════════════════════════════════════════╝"
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
echo "Step 1: Download dependencies..."
go mod download

echo ""
echo "Step 2: Tidy modules..."
go mod tidy

echo ""
echo "Step 3: Build..."
if [ ! -d "./cmd/goftpd" ]; then
    echo "❌ Error: Directory ./cmd/goftpd not found."
    exit 1
fi

go build -o goftpd ./cmd/goftpd

if [ -f goftpd ]; then
    echo ""
    echo "╔════════════════════════════════════════════╗"
    echo "║   ✅ BUILD SUCCESS                          ║"
    echo "╚════════════════════════════════════════════╝"
    echo ""
    ls -lh goftpd
    echo ""
    echo "Edit: etc/config.yml"
    echo "Run:  ./goftpd"
else
    echo "❌ Build failed"
    exit 1
fi