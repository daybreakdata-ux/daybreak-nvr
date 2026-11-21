DAYBREAK NVR for Linux — v1.0
================================
Linux v1.0 is feature-for-feature the Windows v1.7 app, ported
to Ubuntu desktop: same dark-slate SmartPSS-style GUI (Live
Preview wall 1/4/9/16, Playback, Devices & Settings with the
Tapo helper), same ffmpeg stream-copy recording (10-minute MP4
segments per camera), same motion recording, retention rules,
supervisor restarts and on-demand remote viewing. New in the
Linux build: a full web UI served by the NVR itself (see WEB
UI below), and a --headless mode so it can run as a service
with no window at all.

What's different from Windows:
- Settings live in ~/.config/daybreak-nvr/daybreak-nvr-config.json
  (not beside the program) and recordings default to
  ~/DaybreakNVR. The startup log is at
  ~/.config/DaybreakNVR/startup.log.
- The program uses your desktop's real OpenGL. If a machine
  has no working GL, run it with
  GALLIUM_DRIVER=softpipe LIBGL_ALWAYS_SOFTWARE=1 daybreak-nvr
  to fall back to software rendering.
- ffmpeg is bundled in the package (static build 7.0.2) — no
  separate install needed. If it is ever missing, the app falls
  back to the ffmpeg on your PATH (sudo apt install ffmpeg).
- Only one copy can run at a time (a lock file in the config
  folder); a second launch just exits.

A desktop NVR for TP-Link Tapo cameras (and any RTSP camera),
styled after SmartPSS Lite, with Blue-Iris-style always-on
recording underneath.

INSTALLING
----------
  sudo apt install ./daybreak-nvr_1.0-1_amd64.deb

That installs the program to /usr/lib/daybreak-nvr (binary +
bundled ffmpeg), a launcher in your applications menu
("Daybreak NVR"), and a systemd user service. Uninstall with:
  sudo apt remove daybreak-nvr
Your cameras, settings and recordings are kept when you
uninstall or upgrade (they live in your home folder, not in
the package).

RUNNING IT
----------
- Desktop: open "Daybreak NVR" from the applications menu.
- At login / no window: enable the service once:
    systemctl --user enable --now daybreak-nvr
  It runs `daybreak-nvr --headless` and starts at every login.
  Check it with `systemctl --user status daybreak-nvr`, and
  watch the log with `journalctl --user -u daybreak-nvr -f`.
- By hand, headless: `daybreak-nvr --headless`
  (Ctrl+C stops it cleanly).

GETTING STARTED
---------------
1. Install (above).
2. On each Tapo camera, in the Tapo phone app:
   open the camera → Settings (gear) → Advanced Settings →
   Camera Account → create a username and password.
   (This is separate from your TP-Link cloud login — it's what
   lets local software like this pull the camera's RTSP stream.)
3. Open Daybreak NVR.
4. Go to "Devices & Settings" → "Add camera":
   - Name it (Front Door, Driveway…)
   - Enter the camera's IP address (find it in the Tapo app under
     the camera's Device Info, or in your router's client list)
   - Enter the Camera Account username/password from step 2
   - Leave the RTSP URL overrides blank — the app builds Tapo's
     addresses automatically (stream1 = HD for recording,
     stream2 = SD for the live grid)
   - Press "Test connection" — you should see a green check —
     then Save.
5. The camera appears on Live Preview and starts recording
   immediately (if "Record this camera" was ticked).
   You can do the same from the web UI (below) instead.

DAILY USE
---------
Live Preview — device list on the left (red dot = recording),
video wall in the middle. Layout buttons switch 1 / 4 / 9 / 16
tiles (up to 12 cameras supported; 4 is the sweet spot).
Double-click any tile for a big single view; pick layout 4
(or your usual) to go back.

Playback — every recording, newest first, filter by camera.
Click a clip to watch it in the app (pause, jump ±30 seconds),
or "Open in default player" for full controls.

MOTION RECORDING
----------------
Per camera: Edit the camera → Record mode → "Motion only".
The camera is then watched continuously, but only the moments
where something actually moves are kept — with a few seconds
of padding before and after each movement. Everything else is
deleted automatically within about 15 minutes, so a quiet
camera uses almost no disk. The tile shows "Motion —
recording now" while something is moving, and the time of the
last motion otherwise. If a busy camera fills the disk anyway,
the normal retention rules (days / max size) still apply.
Sensitivity (Low / Medium / High) is per camera, in the same
Edit screen — start on Medium; pick High for far-away or
small movement, Low for trees and shadows that trigger too
often. A camera in motion mode shows short 10-second clips in
Playback — that's normal, they're the pieces around each
event.

WEB UI (NEW IN LINUX v1.0)
---------------------------
The NVR serves a complete web interface on your home network
— the same thing the old phone pages did, grown up:
  http://<this-PC's-IP>:8080/ui/
Sign in as "daybreak" with the password shown in
Devices & Settings → Phone viewing (on by default, generated
on first run; change it there). The web UI has:
  Live — 1/4/9/16 wall (MJPEG), click a tile for a big view
  Playback — clips filterable by camera and date, MP4 playback
  Devices — add/edit/delete cameras (Tapo helper + Test
    Connection included), storage and recording settings
  Motion events — the detector's event list per camera
  Settings — recording, retention, web viewing, remote mode
The old phone pages (live grid, per-camera page, clip
playback) still work at the same addresses. From a phone
browser you can also install it as an app: open the web UI
and use "Add to Home screen" (Android) or "Add to Home
Screen" (iOS Safari) — that's Phase 1 of the mobile plan
(see daybreak-nvr-mobile-app-plan.md).
If the phone can't connect, open the port in the firewall,
for example: sudo ufw allow 8080/tcp. Web viewing is home-
network only; nothing is opened to the internet.

REMOTE VIEWING (AWAY FROM HOME)
-------------------------------
Devices & Settings → Remote viewing. It's on by default in
On-demand mode. On your phone, from anywhere:

  1. Open the viewer address shown in the app (or scan the
     QR code there):
       https://daybreak-nvr-daybreakdata-3224s-projects.vercel.app
  2. Sign in with the viewer username and password shown in
     the Remote viewing card in the app.

In On-demand mode nothing streams out of your house until you
open the viewer. Expect the live picture to take about
3–8 seconds to appear after you open a camera — the app at
home has to notice the request and start sending. The stream
stops again shortly after you close the viewer, so remote
viewing uses no upload bandwidth while nobody is watching.
Recording and motion detection on this PC run as usual,
whether or not anyone is watching remotely.

How it works: this PC keeps a lightweight control connection
to a small relay server (Machine 1, on Oracle Cloud), which
has a public address. It works fine on CG-NAT and other
connections without a public IP or port forwarding, because
the PC always connects OUT to the relay. The relay has to be
running for remote viewing to work — if the app says the
control channel can't reach the relay, check that first.
"Always push" mode is also available in the card; it streams
every camera to the relay 24/7 and uses a lot of upload —
On-demand is the sensible default.

STORAGE
-------
Recordings are standard .mp4 files in 10-minute clips under
~/DaybreakNVR/<Camera>/<Date>/ — playable anywhere, no
lock-in. Defaults: keep 7 days of video. Change the folder,
clip length, retention days, or a total size cap in
Devices & Settings → Storage & recording. Old clips are
cleaned up automatically.

A rough size guide: one Tapo camera on its HD stream uses about
0.5–1.5 GB per hour depending on motion and quality settings,
so 4 cameras ≈ 50–150 GB per day at the top end. If your drive
is small, set a Max total size (GB) and the oldest clips roll
off first.

IF IT WON'T OPEN
----------------
The app writes a step-by-step startup log so a launch problem
is never a mystery again:
  ~/.config/DaybreakNVR/startup.log
Every line is one stage of starting up, so the last line shows
exactly where it stopped. If the app hits a problem while
starting, send the startup.log file (or a photo of its last
lines) and it can be diagnosed properly. The window is shown
before any recording/network services start, so a slow or
stuck network can no longer hold the window hostage.

Only one copy can run: a second launch (desktop or headless)
just exits quietly — check the tray, or `ps aux | grep
daybreak-nvr`, if you think it's already running.

If the window opens but stays black or crashes on launch, the
graphics driver is the usual suspect. Try the software
renderer once from a terminal:
  GALLIUM_DRIVER=softpipe LIBGL_ALWAYS_SOFTWARE=1 daybreak-nvr
If that opens fine, your GPU driver needs updating (or keep
using softpipe — recording is unaffected, only the interface
draws slower).

Headless problems: `systemctl --user status daybreak-nvr`
shows whether the service is running; `journalctl --user
-u daybreak-nvr -f` shows its live log. If the web UI
doesn't answer, check the port in Devices & Settings (or
~/.config/daybreak-nvr/daybreak-nvr-config.json) and that
the firewall allows it (sudo ufw allow 8080/tcp).

NOTES & LIMITS (v1.0 Linux)
----------------------------
- Recording is video + audio, copied straight from the camera
  (no re-encoding), so it uses very little CPU.
- If a camera drops off the network, the recorder retries on
  its own with increasing patience — no babysitting.
- Live tiles use the camera's lighter sub-stream, like real
  NVRs do, so 4–12 tiles stay smooth.
- PTZ controls (for cameras that pan/tilt) are not in yet —
  say the word if you want them next.
- Web viewing needs this PC and the app running, on the
  same network as the phone.
- The .deb is unsigned; apt will warn about that only if you
  install it with dpkg directly — use `sudo apt install
  ./daybreak-nvr_1.0-1_amd64.deb` and it handles it.

Daybreak Digital
