#!/bin/bash
# Build Daybreak NVR for Linux (amd64) and package it as a .deb.
#
# Produces:
#   build/daybreak-nvr                  the Linux binary
#   build/daybreak-nvr_1.0-2_amd64.deb  the installable package
# and copies the .deb into the goal files folder
# (~/workspace/goals/daybreak-nvr-for-linux/files/).
#
# ffmpeg is the static build bundled in build/ffmpeg (johnvansickle
# 7.0.2-static) — the package does not depend on the distro ffmpeg.
# Build deps on the build machine (Ubuntu 24.04):
#   sudo apt install golang gcc libgl1-mesa-dev libxxf86vm-dev \
#     libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev \
#     libxkbcommon-dev dpkg-dev
set -e
cd "$(dirname "$0")"
export PATH="$HOME/tools/go/bin:$PATH"
export CGO_ENABLED=1
# /tmp is a small tmpfs here; keep Go's build temp on disk.
mkdir -p "$HOME/workspace/tmp"
export TMPDIR="$HOME/workspace/tmp"
export GOTMPDIR="$HOME/workspace/tmp"

VERSION="1.8"
PKGVER="1.8-1"
OUT="$HOME/workspace/goals/daybreak-nvr-for-linux/files"

echo "==> go vet / go test"
go vet ./...
go test ./...

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

echo "==> building Linux binary (v$VERSION)"
go build -trimpath -ldflags "-s -w" -o build/daybreak-nvr .
ls -la build/daybreak-nvr

echo "==> staging .deb"
ROOT="build/deb-src"
rm -rf "$ROOT"
mkdir -p "$ROOT/usr/lib/daybreak-nvr"
mkdir -p "$ROOT/usr/bin"
mkdir -p "$ROOT/usr/share/applications"
mkdir -p "$ROOT/usr/share/icons/hicolor/512x512/apps"
mkdir -p "$ROOT/usr/share/doc/daybreak-nvr"
mkdir -p "$ROOT/usr/lib/systemd/user"
mkdir -p "$ROOT/DEBIAN"

cp build/daybreak-nvr "$ROOT/usr/lib/daybreak-nvr/daybreak-nvr"
cp build/ffmpeg "$ROOT/usr/lib/daybreak-nvr/ffmpeg"
chmod 755 "$ROOT/usr/lib/daybreak-nvr/daybreak-nvr" "$ROOT/usr/lib/daybreak-nvr/ffmpeg"
# On-device AI person detection (v1.3): the YOLOv8n model and the
# ONNX Runtime shared library, loaded at runtime from beside the
# binary. No cloud, no account, no API key.
cp build/yolov8n.onnx "$ROOT/usr/lib/daybreak-nvr/yolov8n.onnx"
cp build/libonnxruntime.so "$ROOT/usr/lib/daybreak-nvr/libonnxruntime.so"
ln -sf /usr/lib/daybreak-nvr/daybreak-nvr "$ROOT/usr/bin/daybreak-nvr"
cp icon.png "$ROOT/usr/share/icons/hicolor/512x512/apps/daybreak-nvr.png"
cp README.txt "$ROOT/usr/share/doc/daybreak-nvr/README"
gzip -9 -n -f "$ROOT/usr/share/doc/daybreak-nvr/README"
cp build/daybreak-nvr.desktop "$ROOT/usr/share/applications/daybreak-nvr.desktop"
cp build/daybreak-nvr.service "$ROOT/usr/lib/systemd/user/daybreak-nvr.service"
cp build/copyright "$ROOT/usr/share/doc/daybreak-nvr/copyright"

cat > "$ROOT/DEBIAN/control" <<EOF
Package: daybreak-nvr
Version: $PKGVER
Section: video
Priority: optional
Architecture: amd64
Maintainer: Daybreak Digital
Depends: libgl1, libx11-6, libxcursor1, libxrandr2, libxinerama1, libxi6, libxxf86vm1, libxkbcommon0, libc6
Description: Daybreak NVR — desktop NVR for TP-Link Tapo cameras
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

cat > "$ROOT/DEBIAN/postinst" <<'EOF'
#!/bin/sh
# Daybreak NVR postinst. Must not fail on a system without a
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
#   systemctl --user enable --now daybreak-nvr
exit 0
EOF
chmod 755 "$ROOT/DEBIAN/postinst"

# Normalise permissions inside the staging tree. Files copied in
# inherit the workspace's restrictive modes (0664 data files,
# 2770 dirs), which made the installed .desktop file, icon and
# service unreadable by the user and /usr/lib/daybreak-nvr
# untraversable — GNOME could not see the app and the binary
# could not launch. Directories 0755, data files 0644, binaries
# and postinst 0755.
find "$ROOT" -type d -exec chmod 755 {} +
find "$ROOT" -type f -exec chmod 644 {} +
chmod 755 "$ROOT/usr/lib/daybreak-nvr/daybreak-nvr" "$ROOT/usr/lib/daybreak-nvr/ffmpeg" "$ROOT/DEBIAN/postinst"

echo "==> building .deb"
# deb-src inherits the workspace's setgid dirs (2770); dpkg-deb
# requires the control dir <=0775, so normalise permissions first.
# NOTE: numeric chmod 755 preserves the setgid bit on this
# filesystem (2770 -> 2755, still rejected), so strip setuid/setgid/
# sticky bits explicitly (symlinks excluded — the /usr/bin link is
# dangling inside the staging tree).
find "$ROOT" ! -type l -exec chmod a-s {} +
chmod 755 "$ROOT" "$ROOT/DEBIAN"
dpkg-deb --build --root-owner-group "$ROOT" "build/daybreak-nvr_${PKGVER}_amd64.deb"
mkdir -p "$OUT"
cp "build/daybreak-nvr_${PKGVER}_amd64.deb" "$OUT/"
ls -lh build/daybreak-nvr "build/daybreak-nvr_${PKGVER}_amd64.deb" "$OUT/daybreak-nvr_${PKGVER}_amd64.deb"
if command -v lintian >/dev/null 2>&1; then
    echo "==> lintian"
    lintian "build/daybreak-nvr_${PKGVER}_amd64.deb" || true
fi
echo "==> done"
