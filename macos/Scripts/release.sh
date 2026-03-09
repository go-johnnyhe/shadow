#!/bin/bash
# Build, sign, notarize, and package Shadow.app into a DMG.
#
# Usage:
#   ./Scripts/release.sh
#
# Required environment variables:
#   DEVELOPER_ID    - e.g. "Developer ID Application: Mingao He (W2HXB3MG88)"
#   TEAM_ID         - e.g. "W2HXB3MG88"
#   APPLE_ID        - Your Apple ID email
#   APP_PASSWORD    - App-specific password for notarization

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MACOS_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_ROOT="$(dirname "$MACOS_DIR")"
BUILD_DIR="$MACOS_DIR/build/release"
APP_NAME="Shadow"
APP_PATH="$BUILD_DIR/$APP_NAME.app"
DMG_PATH="$BUILD_DIR/$APP_NAME.dmg"

# Defaults from your local cert (override via env vars)
DEVELOPER_ID="${DEVELOPER_ID:-Developer ID Application: Mingao He (W2HXB3MG88)}"
TEAM_ID="${TEAM_ID:-W2HXB3MG88}"

echo "==> Cleaning build directory"
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

echo "==> Building Release configuration"
cd "$MACOS_DIR"
xcodebuild \
    -project Shadow.xcodeproj \
    -scheme Shadow \
    -configuration Release \
    -derivedDataPath "$BUILD_DIR/DerivedData" \
    CONFIGURATION_BUILD_DIR="$BUILD_DIR" \
    CODE_SIGN_IDENTITY="$DEVELOPER_ID" \
    DEVELOPMENT_TEAM="$TEAM_ID" \
    CODE_SIGN_STYLE=Manual \
    ENABLE_HARDENED_RUNTIME=YES \
    CODE_SIGN_INJECT_BASE_ENTITLEMENTS=NO \
    OTHER_CODE_SIGN_FLAGS="--timestamp" \
    build

echo "==> Signing embedded Go binary (with hardened runtime + timestamp)"
codesign --sign "$DEVELOPER_ID" --timestamp --options runtime --force \
    "$APP_PATH/Contents/Resources/shadow"

echo "==> Re-signing app bundle (to include newly signed Go binary)"
codesign --sign "$DEVELOPER_ID" --timestamp --options runtime --force \
    --entitlements "$MACOS_DIR/Shadow/Shadow.entitlements" \
    "$APP_PATH"

echo "==> Verifying code signature"
codesign -dv --verbose=2 "$APP_PATH" 2>&1 | grep -E "Authority|TeamIdentifier|Signature|Runtime"
codesign --verify --strict --deep "$APP_PATH"
echo "    Signature OK"

echo "==> Creating ZIP for notarization"
ZIP_PATH="$BUILD_DIR/$APP_NAME.zip"
ditto -c -k --keepParent "$APP_PATH" "$ZIP_PATH"

echo "==> Submitting for notarization"
if [ -z "${APPLE_ID:-}" ] || [ -z "${APP_PASSWORD:-}" ]; then
    echo "ERROR: Set APPLE_ID and APP_PASSWORD environment variables."
    echo "  export APPLE_ID=your@email.com"
    echo "  export APP_PASSWORD=xxxx-xxxx-xxxx-xxxx"
    exit 1
fi

xcrun notarytool submit "$ZIP_PATH" \
    --apple-id "$APPLE_ID" \
    --password "$APP_PASSWORD" \
    --team-id "$TEAM_ID" \
    --wait

echo "==> Stapling notarization ticket"
xcrun stapler staple "$APP_PATH"

echo "==> Verifying notarization"
spctl --assess --type execute -v "$APP_PATH" 2>&1
echo "    Notarization OK"

echo "==> Creating DMG"
rm -f "$DMG_PATH"

# Create a temporary DMG with a nice layout
TEMP_DMG="$BUILD_DIR/temp.dmg"
MOUNT_DIR="$BUILD_DIR/dmg_mount"
mkdir -p "$MOUNT_DIR"

# Simple DMG with app + Applications symlink
cp -R "$APP_PATH" "$MOUNT_DIR/"
ln -s /Applications "$MOUNT_DIR/Applications"

hdiutil create -volname "$APP_NAME" -srcfolder "$MOUNT_DIR" -ov -format UDRW "$TEMP_DMG"
hdiutil convert "$TEMP_DMG" -format UDZO -o "$DMG_PATH"
rm -f "$TEMP_DMG"
rm -rf "$MOUNT_DIR"

echo "==> Signing DMG"
codesign --sign "$DEVELOPER_ID" --timestamp "$DMG_PATH"

echo "==> Notarizing DMG"
xcrun notarytool submit "$DMG_PATH" \
    --apple-id "$APPLE_ID" \
    --password "$APP_PASSWORD" \
    --team-id "$TEAM_ID" \
    --wait

xcrun stapler staple "$DMG_PATH"

echo ""
echo "==> Done! Release artifact:"
echo "    $DMG_PATH"
ls -lh "$DMG_PATH"
