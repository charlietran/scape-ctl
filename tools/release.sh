#!/usr/bin/env bash
set -euo pipefail

# Release script for scapectl.
# Usage: ./tools/release.sh [version]
#
# If version is omitted, auto-increments the patch number from the latest tag.
# When run locally, creates and pushes a git tag, then builds and releases.
# When run in CI ($GITHUB_ACTIONS set), skips tag creation (already triggered by push).

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

MODULE="./cmd/scapectl"
LDFLAGS_BASE="-s -w"

# ── Preflight ──

command -v go >/dev/null 2>&1 || { echo "Error: go is not installed." >&2; exit 1; }
command -v gh >/dev/null 2>&1 || { echo "Error: gh is not installed. Install with: brew install gh" >&2; exit 1; }

# ── Resolve version ──

if [[ -n "${1:-}" ]]; then
    VERSION="${1#v}"
else
    LATEST=$(git tag --list 'v*' --sort=-version:refname | head -1)
    if [[ -z "$LATEST" ]]; then
        VERSION="0.0.1"
    else
        VER="${LATEST#v}"
        MAJOR="${VER%%.*}"
        REST="${VER#*.}"
        MINOR="${REST%%.*}"
        PATCH="${REST#*.}"
        PATCH=$((PATCH + 1))
        VERSION="${MAJOR}.${MINOR}.${PATCH}"
    fi
fi

TAG="v${VERSION}"
echo "==> Version: ${VERSION} (tag: ${TAG})"

# ── Tag (local only) ──

if [[ -z "${GITHUB_ACTIONS:-}" ]]; then
    if [[ -n "$(git status --porcelain)" ]]; then
        echo "Error: working tree is not clean. Commit or stash changes first." >&2
        exit 1
    fi
    echo "==> Creating tag ${TAG}..."
    git tag "${TAG}"
    git push origin "${TAG}"
fi

# ── Build setup ──

BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT

echo "==> Building macOS (arm64)..."
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
    go build -ldflags "${LDFLAGS_BASE} -X main.version=${VERSION}" \
    -o "${BUILD_DIR}/scapectl" ${MODULE}

# Bundle as .app
APP_DIR="${BUILD_DIR}/ScapeCtl.app/Contents"
mkdir -p "${APP_DIR}/MacOS" "${APP_DIR}/Resources"
mv "${BUILD_DIR}/scapectl" "${APP_DIR}/MacOS/scapectl"
cp config.example.toml "${APP_DIR}/Resources/"

cat > "${APP_DIR}/Info.plist" << PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>Scape Control</string>
    <key>CFBundleDisplayName</key>
    <string>Scape Control</string>
    <key>CFBundleIdentifier</key>
    <string>com.charlietran.scapectl</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleExecutable</key>
    <string>scapectl</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>NSHighResolutionCapable</key>
    <string>True</string>
    <key>LSUIElement</key>
    <string>1</string>
</dict>
</plist>
PLIST

(cd "${BUILD_DIR}" && zip -qr Mac_ScapeCtl.zip ScapeCtl.app)

echo "==> Building Linux (amd64)..."
LINUX_DIR="${BUILD_DIR}/ScapeCtl"
mkdir -p "${LINUX_DIR}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags "${LDFLAGS_BASE} -X main.version=${VERSION}" \
    -o "${LINUX_DIR}/scapectl" ${MODULE}
cp config.example.toml 50-fractal.rules "${LINUX_DIR}/"
tar -czf "${BUILD_DIR}/Linux_ScapeCtl.tar.gz" -C "${BUILD_DIR}" ScapeCtl

echo "==> Building Windows (amd64)..."
WIN_DIR="${BUILD_DIR}/win"
mkdir -p "${WIN_DIR}"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -ldflags "${LDFLAGS_BASE} -H windowsgui -X main.version=${VERSION}" \
    -o "${WIN_DIR}/scapectl.exe" ${MODULE}
cp config.example.toml "${WIN_DIR}/"
(cd "${WIN_DIR}" && zip -qr "${BUILD_DIR}/Win_ScapeCtl.zip" .)

# ── Create GitHub Release ──

echo "==> Creating GitHub release ${TAG}..."
gh release create "${TAG}" \
    --title "${TAG}" \
    --generate-notes \
    "${BUILD_DIR}/Mac_ScapeCtl.zip" \
    "${BUILD_DIR}/Linux_ScapeCtl.tar.gz" \
    "${BUILD_DIR}/Win_ScapeCtl.zip"

echo "==> Done! Release ${TAG} created."
