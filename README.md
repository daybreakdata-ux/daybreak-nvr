# Daybreak NVR — v1.8 Source Archive

This repository preserves the Daybreak NVR source code as it stood on
2026-11-21, before it was replaced by the kCam remake. It is an archive,
not an actively developed project.

## Contents

- `linux/` — Daybreak NVR for Linux (Go/Fyne desktop app, v1.8). Live wall,
  motion/person recording, zone picker, web UI, user management (Admin /
  Viewer), and relay sync for remote viewing. Built for the Ubuntu VM.
- `android/` — Daybreak NVR Android app (WebView shell). Remote-only viewer
  that opens the Vercel viewer; sign-in is against the desktop user data.
- `viewer/` — Vercel viewer / PWA (plain HTML + JS). Tapo-style camera
  grid, live HLS playback, clip playback and download.

## Notes

- Build artifacts are intentionally NOT included: no `.deb` packages,
  no compiled binaries, no bundled ffmpeg, no YOLO/ONNX model files, no
  launcher icon PNGs, and no test-image fixtures. The code here is source
  only; rebuild from source to reproduce a binary.
- The relay publish/viewer passwords baked into
  `linux/internal/engine/relay.go` were replaced with `REPLACE-ME` before
  archiving. Set real values in your own deployment config.
- The TLS `auto.crt` / `auto.key` pair and the Vercel `.env.local` token
  were not archived.

## Original project

Built for a home setup in Kokomo, Indiana: 4 TP-Link Tapo cameras
(driveway, back-yard, alley, gate) on an Ubuntu VM, with a relay on an
Oracle Cloud VM for remote viewing.
