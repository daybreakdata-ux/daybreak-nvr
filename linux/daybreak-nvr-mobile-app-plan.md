# Daybreak NVR — Mobile App Plan

Linux v1.0 (Ubuntu desktop) already ships the foundation for mobile:
the NVR serves a full web UI at `http://<PC-IP>:8080/ui/` (Basic auth,
user `daybreak`, password in Devices & Settings → Phone viewing), with
a PWA manifest + service worker, and JSON APIs under `/api/`:
`/api/status`, `/api/cameras`, `/api/test`, `/api/clips`, `/api/events`,
`/api/settings`, plus MJPEG live streams and clip playback.
Remote (away-from-home) viewing already works through the Vercel
viewer at https://daybreak-nvr-daybreakdata-3224s-projects.vercel.app
via the on-demand relay on Machine 1 — no port forwarding needed.

## Phase 1 — PWA (done in Linux v1.0, no install needed)

- Open `http://<PC-IP>:8080/ui/` in the phone browser, sign in once,
  then "Add to Home screen" (Android) / "Add to Home Screen" (iOS).
- Gives: live wall (1/4/9/16), playback, add/edit cameras with the
  Tapo helper + Test Connection, motion events, settings.
- Limits: home network only (unless the phone is on the LAN or VPN);
  no push notifications; MJPEG live view, not HLS; Basic-auth login
  is stored by the browser, not a proper session.
- Nothing to build — just use it and report what feels missing.

## Phase 2 — Android wrapper app (next, small build)

A WebView-shell app, same pattern as StreamHub (`~/workspace/streamhub/`):
a thin native shell around the existing web UI, so every web-UI
improvement reaches the phone for free.

- Package: `com.daybreak.nvr`, branded splash + icon (reuse `icon.png`).
- Two modes in one app: on the home LAN it opens the local web UI
  (`http://<PC-IP>:8080/ui/`); away from home it opens the Vercel
  remote viewer instead, with a manual switch in settings.
- Saved server address + credentials (encrypted on-device, like
  Shield+'s vault approach — never hard-coded), biometric unlock
  optional.
- Wake-lock / keep-screen-on while a live view is open; background
  audio off (video only, as today).
- Deliverable: a single APK, installable over the PWA — no server
  changes needed. Estimated: one focused build session.

## Phase 3 — Proper native Android app (only if Phase 2 feels limited)

- Native Kotlin app talking to the same `/api/*` endpoints (they
  already exist and are tested in `internal/webview/webview_api_test.go`).
- Native live view (ExoPlayer/HLS or MJPEG), clip download/offline
  cache, camera add/edit forms, motion-event list with thumbnails.
- Push notifications for motion events: needs a small push path —
  the NVR would have to reach the phone (FCM) or the phone polls
  `/api/events`. This is the one piece with a real design decision:
  FCM means a Google project + a sender key on the NVR; polling
  means delayed alerts and battery use. Decide after Phase 2.
- PTZ controls, if wanted, belong here too (not in any NVR version yet).

## Decisions for Justin (no work starts on Phase 2/3 until he picks)

1. Phase 2 wrapper now, or stay on the PWA for a while and collect
   what annoys him first? (Recommendation: use the PWA first — it is
   already built and free.)
2. For remote viewing, is the Vercel viewer good enough, or should
   the app talk to the relay directly?
3. Motion push alerts: wanted at all? If yes, FCM or polling?

## Notes

- The old Vercel PWA in `~/workspace/daybreak-nvr-pwa/` (live HLS
  grid, remote-viewer login) stays as the remote viewer; Phase 2
  reuses it rather than replacing it.
- Web viewing is LAN-only by design; nothing in this plan opens a
  port to the internet. Remote viewing stays on-demand through the
  existing relay.
