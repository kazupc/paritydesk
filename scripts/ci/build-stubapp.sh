#!/bin/sh
# Builds the e2e stub app (testdata/stubapp/StubApp.swift) into a bundle.
#
# usage: build-stubapp.sh OUT.app [BUNDLE_ID] [VERSION]
#
# BUNDLE_ID defaults to dev.paritydesk.stubapp and VERSION to the version
# the 6.x compatibility manifest lists as tested. The real app's bundle id
# is refused, and so is any output path under /Applications. The bundle is
# not code signed beyond the linker's ad-hoc signature; it is meant to run
# only on CI, from a temporary HOME.
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 3 ]; then
	echo "usage: $0 OUT.app [BUNDLE_ID] [VERSION]" >&2
	exit 2
fi
out=$1
bundle_id=${2:-dev.paritydesk.stubapp}
version=${3:-6.28899.2}

case "$bundle_id" in
com.anthropic.* | COM.ANTHROPIC.* | "")
	echo "build-stubapp: refusing bundle id '$bundle_id'" >&2
	exit 2
	;;
esac
case "$bundle_id" in
*[!A-Za-z0-9.-]*)
	echo "build-stubapp: bundle id may hold only letters, digits, dots and hyphens" >&2
	exit 2
	;;
esac
case "$version" in
*[!0-9.]* | "")
	echo "build-stubapp: version must be digits and dots" >&2
	exit 2
	;;
esac
case "$out" in
*.app) ;;
*)
	echo "build-stubapp: OUT must end in .app" >&2
	exit 2
	;;
esac
case "$out" in
/Applications | /Applications/*)
	echo "build-stubapp: refusing to write under /Applications" >&2
	exit 2
	;;
esac

if ! command -v swiftc >/dev/null 2>&1; then
	echo "build-stubapp: swiftc not found" >&2
	exit 1
fi

root=$(cd "$(dirname "$0")/../.." && pwd -P)
src="$root/testdata/stubapp/StubApp.swift"

rm -rf "$out"
mkdir -p "$out/Contents/MacOS" "$out/Contents/Resources"
swiftc -O -swift-version 5 -o "$out/Contents/MacOS/stubapp" "$src"

cat >"$out/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleExecutable</key>
	<string>stubapp</string>
	<key>CFBundleIdentifier</key>
	<string>$bundle_id</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>ParitydeskStub</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$version</string>
	<key>CFBundleVersion</key>
	<string>$version</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSPrincipalClass</key>
	<string>NSApplication</string>
</dict>
</plist>
PLIST

/usr/bin/plutil -lint "$out/Contents/Info.plist" >/dev/null
echo "built $out ($bundle_id $version)"
