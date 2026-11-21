#!/bin/bash
# Fallback installer build (only needed if NSIS/makensis is unavailable).
# Regenerates payload.zip from the staged portable folder and builds the
# console setup exe into /tmp. The primary installer is installer/DaybreakNVR.nsi.
set -e
cd "$(dirname "$0")/../.."
export PATH="$HOME/tools/go/bin:$PATH"
export TMPDIR="$HOME/workspace/tmp" GOTMPDIR="$HOME/workspace/tmp"
(cd "$HOME/workspace/daybreak-nvr" && zip -q -j "$HOME/workspace/daybreak-nvr-win/cmd/setup/payload.zip" DaybreakNVR.exe ffmpeg.exe README.txt icon.png)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOPROXY=off GOFLAGS=-mod=mod go build -o /tmp/DaybreakNVR-Setup-v1.3-fallback.exe ./cmd/setup
ls -lh /tmp/DaybreakNVR-Setup-v1.3-fallback.exe
