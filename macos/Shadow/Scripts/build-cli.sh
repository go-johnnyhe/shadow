#!/bin/bash
# Build the Shadow Go binary and copy it into the app bundle's Resources.
# This script is meant to be called as an Xcode "Run Script" build phase.

set -euo pipefail

# Determine architecture from Xcode build settings
if [ -n "${ARCHS:-}" ]; then
    case "$ARCHS" in
        arm64)  GOARCH="arm64" ;;
        x86_64) GOARCH="amd64" ;;
        *)      GOARCH="arm64" ;; # default
    esac
else
    GOARCH="arm64"
fi

# The Go project root is one level up from macos/ (SRCROOT = macos/)
GO_PROJECT_ROOT="${SRCROOT}/.."

# Output directory inside the built .app bundle
if [ -n "${BUILT_PRODUCTS_DIR:-}" ] && [ -n "${UNLOCALIZED_RESOURCES_FOLDER_PATH:-}" ]; then
    OUTPUT_DIR="${BUILT_PRODUCTS_DIR}/${UNLOCALIZED_RESOURCES_FOLDER_PATH}"
else
    OUTPUT_DIR="${SRCROOT}/build"
fi

mkdir -p "$OUTPUT_DIR"

echo "Building shadow binary (GOARCH=$GOARCH)..."

CGO_ENABLED=0 GOOS=darwin GOARCH="$GOARCH" \
    go build \
    -ldflags "-s -w" \
    -o "$OUTPUT_DIR/shadow" \
    "$GO_PROJECT_ROOT"

echo "Shadow binary built at: $OUTPUT_DIR/shadow"
