#!/bin/bash
set -e

# Visual Header
echo "╔═════════════════════════════════════════════════╗"
echo "║   GoFTPd Build - Master/Slave Architecture      ║"
echo "╚═════════════════════════════════════════════════╝"
echo ""

# Step 0: Check if Go is installed
if ! command -v go &> /dev/null; then
    echo "❌ Error: Go is not installed."
    echo "Please install Go first (https://go.dev/doc/install) or check your PATH."
    exit 1
fi

echo "Step 1: Download dependencies..."
# Note: 'go get' inside a module-enabled project is often replaced by 'go mod download'
go mod download

echo ""
echo "Step 2: Tidy modules..."
go mod tidy

echo ""
echo "Step 3: Build..."
# Ensure the directory exists before building
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
    echo "Edit: edit etc/config.yml" 
    echo ""
    echo "Run: ./goftpd"
else
    echo "❌ Build failed"
    exit 1
fi