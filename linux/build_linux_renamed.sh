#!/bin/bash
# Build the RENAMED Daybreak NVR variant for Linux (amd64) and
# package it as a .deb.
#
# Produces:
#   build/daybreak-nvr-linux                  the Linux binary
#   build/daybreak-nvr-linux_1.3.1-1_amd64.deb  the installable package
# and copies the .deb into the goal files folder
# (~/workspace/goals/daybreak-nvr-for-linux/files/).
#
# The renamed variant is the same tree with the identity switched:
# package daybreak-nvr-linux, app "Daybreak NVR Linux", binary
# /usr/bin/daybreak-nvr-linux, config ~/.config/daybreak-nvr-linux,
# recordings ~/DaybreakNVR-Linux, Fyne ID com.daybreak.nvr.linux.
# It is built from a throwaway copy of the tree with those renames
# applied by sed (the original tree is never modified), so it
# co-installs with the original variant with zero overlapping files.
#
# ffmpeg is the static build bundled in build/ffmpeg (johnvansickle
# 7.0.2-static) — the package does not depend on the distro ffmpeg.
# Build deps on the build machine (Ubuntu 24.04):
#   sudo apt install golang gcc libgl1-mesa-dev libxxf86vm-dev \
#     libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev \
#     libxkbcommon-dev dpkg-dev
set -e
cd "$(dirname "$0")"
ROOT="$(pwd)"
export PATH="$HOME/tools/go/bin:$PATH"
export CGO_ENABLED=1
# /tmp is a small tmpfs here; keep Go's build temp on disk.
mkdir -p "$HOME/workspace/tmp"
export TMPDIR="$HOME/workspace/tmp"
export GOTMPDIR="$HOME/workspace/tmp"

VERSION="1.8"
PKGVER="1.8-1"
OUT="$HOME/workspace/goals/daybreak-nvr-for-linux/files"

# The on-device AI person detector (v1.3) needs the bundled
# YOLOv8n model and the ONNX Runtime shared library — fail here
# with a clear message instead of shipping a .deb with detection
# silently disabled.
for f in build/yolov8n.onnx build/libonnxruntime.so; do
    if [ ! -f "$f" ]; then
        echo "missing $f — the AI person detector cannot be bundled" >&2
        exit 1
    fi
done

echo "==> copying tree for the renamed variant"
SRC="build/renamed-src"
rm -rf "$SRC"
mkdir -p "$SRC"
# build/ holds previous outputs (including this copy's own target
# dir); it is not part of the source tree. --no-same-owner: the
# workspace files are root-owned and extraction as a normal user
# cannot chown them back.
tar --exclude=./build -cf - . | tar --no-same-owner -xf - -C "$SRC"

echo "==> applying renamed-variant identity (sed)"
# The person detector already lists both /usr/lib/daybreak-nvr/ and
# /usr/lib/daybreak-nvr-linux/ as fallbacks — protect the -linux
# occurrences first so the plain rename cannot turn them into
# "daybreak-nvr-linux-linux". The shared Vercel viewer URL is also
# protected: it is the same viewer for both variants, not variant
# identity (v1.3 kept it unrenamed in the README).
find "$SRC" -name '*.go' -exec sed -i \
    -e 's|daybreak-nvr-daybreakdata|__DBNVR_VIEWER__|g' \
    -e 's|daybreak-nvr-linux|__DBNVR_LINUX__|g' \
    -e 's|daybreak-nvr|daybreak-nvr-linux|g' \
    -e 's|__DBNVR_LINUX__|daybreak-nvr-linux|g' \
    -e 's|__DBNVR_VIEWER__|daybreak-nvr-daybreakdata|g' \
    -e 's|DaybreakNVR|DaybreakNVR-Linux|g' \
    -e 's|Daybreak NVR|Daybreak NVR Linux|g' \
    -e 's|com\.daybreak\.nvr|com.daybreak.nvr.linux|g' {} +

echo "==> go vet / go test (renamed tree)"
(cd "$SRC" && go vet ./...)
(cd "$SRC" && go test ./...)

echo "==> building Linux binary, renamed variant (v$VERSION)"
(cd "$SRC" && go build -trimpath -ldflags "-s -w" -o "$ROOT/build/daybreak-nvr-linux" .)
ls -la build/daybreak-nvr-linux

echo "==> staging .deb"
DEBROOT="build/deb-src-linux"
rm -rf "$DEBROOT"
mkdir -p "$DEBROOT/usr/lib/daybreak-nvr-linux"
mkdir -p "$DEBROOT/usr/bin"
mkdir -p "$DEBROOT/usr/share/applications"
mkdir -p "$DEBROOT/usr/share/icons/hicolor/512x512/apps"
mkdir -p "$DEBROOT/usr/share/doc/daybreak-nvr-linux"
mkdir -p "$DEBROOT/usr/lib/systemd/user"
mkdir -p "$DEBROOT/DEBIAN"

cp build/daybreak-nvr-linux "$DEBROOT/usr/lib/daybreak-nvr-linux/daybreak-nvr-linux"
cp build/ffmpeg "$DEBROOT/usr/lib/daybreak-nvr-linux/ffmpeg"
chmod 755 "$DEBROOT/usr/lib/daybreak-nvr-linux/daybreak-nvr-linux" "$DEBROOT/usr/lib/daybreak-nvr-linux/ffmpeg"
# On-device AI person detection (v1.3): the YOLOv8n model and the
# ONNX Runtime shared library, loaded at runtime from beside the
# binary. No cloud, no account, no API key.
cp build/yolov8n.onnx "$DEBROOT/usr/lib/daybreak-nvr-linux/yolov8n.onnx"
cp build/libonnxruntime.so "$DEBROOT/usr/lib/daybreak-nvr-linux/libonnxruntime.so"
ln -sf /usr/lib/daybreak-nvr-linux/daybreak-nvr-linux "$DEBROOT/usr/bin/daybreak-nvr-linux"
cp icon.png "$DEBROOT/usr/share/icons/hicolor/512x512/apps/daybreak-nvr-linux.png"
# README gets the same renames as the source tree (v1.3 did this:
# paths, commands and the app name all switch to the -linux
# identity; the stale "v1.0" version line is left as-is). The
# shared Vercel viewer URL stays unrenamed — it is the same
# viewer for both variants.
sed -e 's|daybreak-nvr-daybreakdata|__DBNVR_VIEWER__|g' \
    -e 's|daybreak-nvr|daybreak-nvr-linux|g' \
    -e 's|__DBNVR_VIEWER__|daybreak-nvr-daybreakdata|g' \
    -e 's|DaybreakNVR|DaybreakNVR-Linux|g' \
    -e 's|Daybreak NVR|Daybreak NVR Linux|g' \
    -e 's|DAYBREAK NVR for Linux|DAYBREAK NVR LINUX|' README.txt > "$DEBROOT/usr/share/doc/daybreak-nvr-linux/README"
gzip -9 -n -f "$DEBROOT/usr/share/doc/daybreak-nvr-linux/README"
sed 's/daybreak-nvr/daybreak-nvr-linux/g; s/Daybreak NVR/Daybreak NVR Linux/g' build/daybreak-nvr.desktop > "$DEBROOT/usr/share/applications/daybreak-nvr-linux.desktop"
sed 's/daybreak-nvr/daybreak-nvr-linux/g; s/Daybreak NVR/Daybreak NVR Linux/g' build/daybreak-nvr.service > "$DEBROOT/usr/lib/systemd/user/daybreak-nvr-linux.service"
sed 's/^Upstream-Name: Daybreak NVR$/Upstream-Name: Daybreak NVR Linux/' build/copyright > "$DEBROOT/usr/share/doc/daybreak-nvr-linux/copyright"

cat > "$DEBROOT/DEBIAN/control" <<EOF
Package: daybreak-nvr-linux
Version: $PKGVER
Section: video
Priority: optional
Architecture: amd64
Maintainer: Daybreak Digital
Depends: libgl1, libx11-6, libxcursor1, libxrandr2, libxinerama1, libxi6, libxxf86vm1, libxkbcommon0, libc6
Description: Daybreak NVR Linux — desktop NVR for TP-Link Tapo cameras
 v1.8: fixes the "not recording at all" failure — the event body
 and pre-roll segments now share one video timescale (15360), so
 the finalize concat no longer inflates clip durations ~5.86x and
 the duration check stops dropping every event. A watchdog
 restarts any ffmpeg worker (pre-buffer or detector) that is
 alive but has produced nothing for 30 s, so a stalled RTSP
 connection recovers instead of sitting dead for hours. The
 rolling pre-buffer pauses while an event records, so the camera
 has only two RTSP connections during an event (some cameras
 refuse a third).
 v1.7: no desktop sign-in gate — the desktop app runs as full
 local admin and services start immediately on launch (users,
 relay sync and the web UI login are untouched). Every launch
 kills all other NVR instances first (main binary + bundled
 ffmpeg workers, including orphans) before taking the
 single-instance lock, so a fresh open leaves exactly one NVR
 running — no more "already running" dead-end box.
 v1.6: direct event recording — Motion and Person-only mode record
 one clip per event straight from the camera (no 10 s chunks, no
 finalizer); a rolling pre-buffer keeps the configured keep-before
 seconds. New Person-only record mode (records only when a person
 is seen, and turns human detection on for that camera). The
 detection-zone editor is now the camera picture with the 16x9
 grid overlaid on it, in the desktop app and the web UI (fresh
 stills from a new /api/camera-snapshot endpoint).
 v1.5: event-length recordings — one clip per motion/person event
 (the finalizer merges 10 s chunks, using the live keep-before/
 keep-after event settings); merged clips are skipped by the
 motion reaper; clip kind now resolves the tracker events file
 by camera ID (with a slug fallback).
 A desktop NVR for TP-Link Tapo (and any RTSP) cameras: live
 preview wall, always-on MP4 segment recording, motion
 recording, retention rules, a full web UI on the LAN and
 on-demand remote viewing. Linux v1.2 replaces the single
 shared login with per-user accounts (admin / viewer roles),
 syncs users to the relay, and deletes clips on relay request.
 Linux v1.2.1 never lists or uploads MP4 segments ffmpeg has not
 finalized (no moov atom), so in-progress or orphaned segments can
 no longer appear as unplayable clips. Clip uploads are remuxed
 with +faststart (stream copy) so playback starts at once instead
 of waiting for the moov atom at the end of the file.
 Linux v1.3 rebuilds Devices & Settings as five pages (Cameras,
 Motion & Recording, General, Users, Storage); motion sensitivity
 becomes a 0..100 slider with per-camera detection zones and
 configurable pre/post-event buffers; and human detection runs
 fully on-device (bundled YOLOv8n + ONNX Runtime, no cloud) with
 per-camera tracking of people.
 Linux v1.3.1 is a UI-only fix: the Cameras and Users lists show
 all entries without scrolling until there are more than 10 (v1.3
 showed one row at a time), and the desktop footer status bar is
 removed (the top bar with title, disk usage and clock stays).
 Linux v1.4 adds one motion snapshot per motion/person event (the
 frame whose motion centroid is nearest the picture centre),
 Tapo-style playback thumbnails with duration and person/motion
 icons, multi-select clip delete plus Delete All, an Open
 recordings folder button, start at boot via an XDG autostart
 entry, and remember-me on the sign-in screen. Snapshots upload
 to the relay eagerly, and the relay clip index carries each clip's
 duration and kind.
EOF

cat > "$DEBROOT/DEBIAN/postinst" <<'EOF'
#!/bin/sh
# Daybreak NVR Linux postinst. Must not fail on a system without a
# desktop session (headless install, first boot, etc.), so every
# step is best-effort and the script always exits 0.
set -e
if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
    gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor >/dev/null 2>&1 || true
fi
# The systemd user service is deliberately NOT enabled here:
# enabling a user unit needs the installing user's session bus,
# which postinst does not have. The README tells the user to run:
#   systemctl --user enable --now daybreak-nvr-linux
exit 0
EOF
chmod 755 "$DEBROOT/DEBIAN/postinst"

# Normalise permissions inside the staging tree. Files copied in
# inherit the workspace's restrictive modes (0664 data files,
# 2770 dirs), which made the installed .desktop file, icon and
# service unreadable by the user and /usr/lib/daybreak-nvr-linux
# untraversable — GNOME could not see the app and the binary
# could not launch. Directories 0755, data files 0644, binaries
# and postinst 0755.
find "$DEBROOT" -type d -exec chmod 755 {} +
find "$DEBROOT" -type f -exec chmod 644 {} +
chmod 755 "$DEBROOT/usr/lib/daybreak-nvr-linux/daybreak-nvr-linux" "$DEBROOT/usr/lib/daybreak-nvr-linux/ffmpeg" "$DEBROOT/DEBIAN/postinst"

echo "==> building .deb"
# deb-src inherits the workspace's setgid dirs (2770); dpkg-deb
# requires the control dir <=0775, so normalise permissions first.
# NOTE: numeric chmod 755 preserves the setgid bit on this
# filesystem (2770 -> 2755, still rejected), so strip setuid/setgid/
# sticky bits explicitly (symlinks excluded — the /usr/bin link is
# dangling inside the staging tree).
find "$DEBROOT" ! -type l -exec chmod a-s {} +
chmod 755 "$DEBROOT" "$DEBROOT/DEBIAN"
dpkg-deb --build --root-owner-group "$DEBROOT" "build/daybreak-nvr-linux_${PKGVER}_amd64.deb"
mkdir -p "$OUT"
cp "build/daybreak-nvr-linux_${PKGVER}_amd64.deb" "$OUT/"
ls -lh build/daybreak-nvr-linux "build/daybreak-nvr-linux_${PKGVER}_amd64.deb" "$OUT/daybreak-nvr-linux_${PKGVER}_amd64.deb"
if command -v lintian >/dev/null 2>&1; then
    echo "==> lintian"
    lintian "build/daybreak-nvr-linux_${PKGVER}_amd64.deb" || true
fi
echo "==> done"
