"use strict";
// NVR remote viewer — Tapo-style redesign.
// Cameras live on a PC behind home internet with no public IP.
// Opening this page asks the relay to ask that PC to start
// streaming ("on-demand"); closing it lets the stream stop.
const RELAY = "https://163-192-218-43.sslip.io";
// Token session: {token, role, username}. Roles are exactly
// "admin" and "viewer". No password is ever stored here.
const LS_KEY = "nvr_session";
localStorage.removeItem("nvr_auth"); // drop any credential stored by the old login
let session = null;
try { session = JSON.parse(localStorage.getItem(LS_KEY) || "null"); } catch (e) { session = null; }
if (!session || typeof session.token !== "string" || !["admin", "viewer"].includes(session.role))
  session = null;

// Remember-me: opt-in saved sign-in (username + password) in
// localStorage, separate from the session token (LS_KEY).
const REMEMBER_KEY = "nvr_remember";
const isAdmin = () => !!(session && session.role === "admin");

const $ = (id) => document.getElementById(id);
let players = new Map(); // slug -> {hls, video, onstate, retryTimer, demandTimer, offlineTimer, live}
let cameras = [];
let appStarted = false;
let offlineSlugs = new Set(); // slugs that failed to wake after 45s

// Grid layout: "auto" | "1" | "4", persisted. "4" shows the first
// four cameras only, in a strict 2x2 grid.
const LAYOUT_KEY = "nvr_layout";
let layout = localStorage.getItem(LAYOUT_KEY) || "auto";
if (!["auto", "1", "4"].includes(layout)) layout = "auto";

// Views: "home" | "cameras" | "live" | "playback" | "me"
let currentView = "home";
let liveCam = null;      // camera open in Live view
let livePlayer = null;   // player object for the Live view video
let returnView = "home"; // where Back from Live view goes

// Home: favorites + grid/list, persisted.
const FAV_KEY = "nvr_favs";
const HOME_LAYOUT_KEY = "nvr_home_layout";
let favs = new Set(JSON.parse(localStorage.getItem(FAV_KEY) || "[]"));
let homeFilter = "all"; // "all" | "fav"
let homeLayout = localStorage.getItem(HOME_LAYOUT_KEY) || "grid";
if (!["grid", "list"].includes(homeLayout)) homeLayout = "grid";
let homePlayers = new Map(); // slug -> player object for Home cards
let homeSig = ""; // card-set signature; rebuild only when it changes

// ---- Theme: Light / Dark / Follow System (Me > Appearance) ----
const THEME_KEY = "nvr_theme";
let theme = localStorage.getItem(THEME_KEY) || "dark";
if (!["light", "dark", "system"].includes(theme)) theme = "dark";
const mqDark = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
function resolvedTheme() {
  if (theme === "dark") return "dark";
  if (theme === "light") return "light";
  return mqDark && mqDark.matches ? "dark" : "light";
}
function applyTheme() {
  document.documentElement.dataset.theme = resolvedTheme();
  document.querySelectorAll("#themeSeg button").forEach(b =>
    b.classList.toggle("active", b.dataset.theme === theme));
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = resolvedTheme() === "dark" ? "#001119" : "#edf1f3";
}
if (mqDark && mqDark.addEventListener)
  mqDark.addEventListener("change", () => { if (theme === "system") applyTheme(); });

function authHeader() { return session ? "Bearer " + session.token : ""; }

async function apiFetch(path, opts) {
  opts = opts || {};
  const headers = Object.assign({}, opts.headers || {});
  if (session && session.token) headers["Authorization"] = "Bearer " + session.token;
  const r = await fetch(RELAY + path, Object.assign({}, opts, { headers }));
  if (r.status === 401) {
    const e = new Error("unauthorized");
    e.status = 401;
    throw e;
  }
  if (!r.ok) {
    const e = new Error("HTTP " + r.status);
    e.status = r.status;
    throw e;
  }
  return r;
}

// Ask the relay for one camera's stream. Demand expires on the
// relay after ~45s, so a visible tile re-asks every 20s.
function requestCamera(slug) {
  apiFetch("/api/nvr/demand", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ slug }),
  }).catch(() => {});
}

function showLogin(msg) {
  $("app").classList.add("hidden");
  $("login").classList.remove("hidden");
  $("loginErr").textContent = msg || "";
  // Remember-me: autofill the saved sign-in, if the user opted in.
  // Otherwise the form starts blank (e.g. right after signing out,
  // which forgets the saved sign-in).
  try {
    const saved = JSON.parse(localStorage.getItem(REMEMBER_KEY) || "null");
    if (saved && saved.u) {
      $("user").value = saved.u;
      $("pass").value = saved.p || "";
      $("remember").checked = true;
    } else {
      $("user").value = "";
      $("pass").value = "";
      $("remember").checked = false;
    }
  } catch (e) {}
}

function showApp() {
  $("login").classList.add("hidden");
  $("app").classList.remove("hidden");
  // Always land on Home after signing in, even if the previous
  // session ended on another tab (e.g. Me > Sign out).
  showView("home");
  if (session) {
    const u = $("meUser");
    if (u) u.textContent = session.username || "Signed in";
    const r = $("meRole");
    if (r) r.textContent = session.role === "admin"
      ? "Admin — can delete clips and change settings in the NVR app"
      : "Viewer — live view, playback and downloads";
  }
}

$("login").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const u = $("user").value.trim();
  const p = $("pass").value;
  const btn = $("login").querySelector('button[type="submit"]');
  $("loginErr").textContent = "";
  btn.disabled = true;
  btn.textContent = "Signing in…";
  try {
    const r = await fetch(RELAY + "/api/nvr/auth", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: u, password: p }),
    });
    if (!r.ok) throw new Error("bad login");
    const data = await r.json();
    if (!data.token || !["admin", "viewer"].includes(data.role))
      throw new Error("bad login");
    session = { token: data.token, role: data.role, username: data.username || u };
    localStorage.setItem(LS_KEY, JSON.stringify(session));
    // Remember-me is opt-in: save the sign-in only when checked,
    // and clear any previously saved one when unchecked.
    if ($("remember").checked)
      localStorage.setItem(REMEMBER_KEY, JSON.stringify({ u, p }));
    else
      localStorage.removeItem(REMEMBER_KEY);
    showApp();
    startApp();
  } catch (e) {
    session = null;
    // Generic message on purpose: never reveal whether the
    // username or the password was wrong.
    $("loginErr").textContent = "Incorrect username or password.";
  } finally {
    btn.disabled = false;
    btn.textContent = "Sign in";
  }
});

// ---- Bottom sheets ----
function openSheet(title, bodyHtml) {
  $("sheetTitle").textContent = title;
  $("sheetBody").innerHTML = bodyHtml;
  $("sheetBack").classList.remove("hidden");
}
function closeSheet() { $("sheetBack").classList.add("hidden"); }
$("sheetClose").addEventListener("click", closeSheet);
$("sheetBack").addEventListener("click", (ev) => {
  if (ev.target === $("sheetBack")) closeSheet();
});

$("bellBtn").addEventListener("click", () => openSheet("Notifications",
  "<p>No notifications yet.</p>" +
  "<p>Motion alerts aren't connected here yet — recorded clips are under Playback.</p>"));
$("plusBtn").addEventListener("click", () => openSheet("Add a camera",
  "<p>Add cameras in the NVR app on your PC (Devices &amp; Settings), and make sure Remote viewing is on there — they'll show up here on their own.</p>"));
$("homeBtn").addEventListener("click", () => openSheet("My home",
  '<div class="row"><span>Home name</span><b>My home</b></div>' +
  '<div class="row"><span>Rooms</span><span>Managed in the NVR app on your PC</span></div>'));

// ---- Navigation (bottom nav) ----
const VIEWS = ["home", "cameras", "live", "playback", "me"];
function showView(view) {
  if (view === "live" && !liveCam) view = "home";
  // Leaving a video view stops its players and demand.
  if (currentView === "cameras" && view !== "cameras") stopAllPlayers();
  if (currentView === "home" && view !== "home") stopHomePlayers();
  if (currentView === "live" && view !== "live") stopLivePlayer();
  if (currentView === "playback" && view !== "playback") { stopClipPoll(); stopClipHls(); }
  if (view === "playback") stopAllPlayers(), stopLivePlayer(), stopClipPoll();
  currentView = view;
  for (const v of VIEWS) $("view-" + v).classList.toggle("hidden", v !== view);
  document.querySelectorAll("#bottomnav button").forEach(b =>
    b.classList.toggle("active", b.dataset.nav === (view === "live" ? returnView : view)));
  if (view === "home") renderHome();
  if (view === "cameras") buildGrid();
  if (view === "playback") { populatePlaybackCameras(); loadClips(); }
  if (view === "me") applyTheme();
  window.scrollTo(0, 0);
}
document.querySelectorAll("#bottomnav button").forEach(b =>
  b.addEventListener("click", () => showView(b.dataset.nav)));

// ---- Players ----
function stopPlayer(pl) {
  clearTimeout(pl.retryTimer);
  clearInterval(pl.demandTimer);
  clearTimeout(pl.offlineTimer);
  if (pl.hls) pl.hls.destroy();
}

function stopAllPlayers() {
  for (const pl of players.values()) stopPlayer(pl);
  players.clear();
  $("grid").innerHTML = "";
}

// Home cards have their own players (separate map so leaving Home
// doesn't disturb the grid and vice versa). Stopping them also
// invalidates the Home render signature so the cards rebuild —
// with fresh players — the next time Home is shown.
function stopHomePlayers() {
  for (const pl of homePlayers.values()) stopPlayer(pl);
  homePlayers.clear();
  homeSig = "";
}

function attachPlayer(cam, video, onstate, store) {
  store = store || players;
  const pl = { hls: null, video, onstate, retryTimer: null, demandTimer: null,
               offlineTimer: null, live: false, phase: "connecting" };
  store.set(cam.slug, pl);
  const url = RELAY + "/nvr-" + cam.slug + "/index.m3u8";

  // Phases: "connecting" (demand posted, stream not started yet) vs
  // "loading" (stream started, waiting for first frames) vs "live".
  // The tile/card overlay follows this exactly — it is hidden the
  // moment frames render.
  const setPhase = (phase) => {
    if (store.get(cam.slug) !== pl) return; // stale player
    pl.phase = phase;
    onstate(phase === "live",
      phase === "live" ? "live" : phase === "loading" ? "Loading…" : "Connecting…",
      phase);
  };

  // Demand first, then keep demanding while the page is open.
  requestCamera(cam.slug);
  pl.demandTimer = setInterval(() => requestCamera(cam.slug), 20000);
  setPhase("connecting");

  // No live picture after 45s of asking: treat the camera as
  // offline (shown in orange on Home). Cleared once it goes live.
  pl.offlineTimer = setTimeout(() => {
    if (!pl.live && store.get(cam.slug) === pl) {
      offlineSlugs.add(cam.slug);
      if (currentView === "home") renderHome();
    }
  }, 45000);

  const markLive = () => {
    if (store.get(cam.slug) !== pl) return; // stale player
    if (pl.live) return;
    pl.live = true;
    offlineSlugs.delete(cam.slug);
    setPhase("live");
    if (currentView === "home") renderHome();
  };

  if (window.Hls && Hls.isSupported()) {
    const start = () => {
      if (store.get(cam.slug) !== pl) return;
      if (!pl.live) setPhase("connecting");
      const hls = new Hls({
        liveSyncDurationCount: 2,
        maxLiveSyncPlaybackRate: 1.5,
        xhrSetup: (xhr) => xhr.setRequestHeader("Authorization", authHeader()),
        fetchSetup: (ctx, init) => {
          init.headers = Object.assign({}, init.headers,
            { "Authorization": authHeader() });
          return init;
        },
      });
      pl.hls = hls;
      hls.loadSource(url);
      hls.attachMedia(video);
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        // Stream is up; now waiting for the first frames.
        setPhase("loading");
        video.play().catch(() => {});
      });
      hls.on(Hls.Events.FRAG_BUFFERED, markLive);
      video.addEventListener("playing", markLive);
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (!data.fatal) return;
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          hls.recoverMediaError();
          return;
        }
        // Most likely the stream hasn't started yet (404 while
        // the home PC spins up its pusher): keep asking for it
        // and retry — that's the normal wake-up path.
        pl.live = false;
        requestCamera(cam.slug);
        setPhase("connecting");
        hls.destroy();
        pl.retryTimer = setTimeout(start, 4000);
      });
    };
    start();
  } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
    // Safari: native HLS. The relay's 404-until-awake behaviour
    // means an early load can fail; reload the source until it
    // plays.
    const tryLoad = () => {
      if (store.get(cam.slug) !== pl || pl.live) return;
      setPhase("connecting");
      video.src = url;
      video.play().catch(() => {});
    };
    video.addEventListener("loadedmetadata", () => {
      if (store.get(cam.slug) === pl && !pl.live) setPhase("loading");
    });
    video.addEventListener("playing", markLive);
    video.addEventListener("error", () => {
      if (!pl.live) {
        setPhase("connecting");
        pl.retryTimer = setTimeout(tryLoad, 4000);
      }
    });
    tryLoad();
  } else {
    onstate(false, "browser not supported", "connecting");
  }
  return pl;
}

// Show the right loading animation for a tile/card/live box, or
// hide it once frames render ("live") or the camera is marked
// offline ("off"). `el` is the .loadstate element.
function setLoadState(el, phase) {
  if (!el) return;
  el.classList.toggle("off", phase === "live" || phase === "off");
  el.classList.toggle("connecting", phase === "connecting");
  el.classList.toggle("loading", phase === "loading");
  const label = el.querySelector(".loadlabel");
  if (label) label.textContent = phase === "loading" ? "Loading…" : "Connecting…";
}

// ---- Home: live video tiles (same treatment as the grid) ----
const CAM_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M23 19a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4l2-3h6l2 3h4a2 2 0 0 1 2 2z"/><circle cx="12" cy="13" r="4"/></svg>';
const STAR_ICON = '<svg viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="1" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>';

function visibleHomeCameras() {
  return homeFilter === "fav" ? cameras.filter(c => favs.has(c.slug)) : cameras;
}

// Rebuild only when the card set changes; live/waking/offline state
// updates in place, so the 30s camera refresh never restarts a
// stream that is already playing.
function renderHome() {
  const grid = $("homeGrid");
  grid.classList.toggle("list", homeLayout === "list");
  const list = visibleHomeCameras();
  $("homeEmpty").classList.toggle("hidden", cameras.length > 0);
  const sig = homeFilter + "|" + [...favs].sort().join(",") + "|" +
    list.map(c => c.slug).join(",");
  if (sig !== homeSig) {
    stopHomePlayers();
    grid.innerHTML = "";
    for (const cam of list) {
      const card = document.createElement("div");
      card.className = "devcard";
      card.dataset.slug = cam.slug;
      card.innerHTML =
        '<div class="frame"><video muted playsinline autoplay></video>' +
        '<div class="loadstate connecting"><span class="loadanim"></span><span class="loadlabel">Connecting…</span></div></div>' +
        '<div class="tiletag"><span class="dot"></span>' +
        '<span class="name"></span><span class="state"></span></div>' +
        '<button class="fav" type="button" aria-label="Favorite">' + STAR_ICON + "</button>";
      card.querySelector(".name").textContent = cam.name;
      const favBtn = card.querySelector(".fav");
      favBtn.classList.toggle("on", favs.has(cam.slug));
      favBtn.addEventListener("click", (ev) => {
        ev.stopPropagation();
        if (favs.has(cam.slug)) favs.delete(cam.slug); else favs.add(cam.slug);
        localStorage.setItem(FAV_KEY, JSON.stringify([...favs]));
        favBtn.classList.toggle("on", favs.has(cam.slug));
        if (homeFilter === "fav") renderHome();
      });
      card.addEventListener("click", () => openLive(cam, "home"));
      grid.appendChild(card);
      attachPlayer(cam, card.querySelector("video"), () => updateHomeStates(), homePlayers);
    }
    homeSig = sig;
  }
  updateHomeStates();
  $("homeLayoutBtn").textContent = homeLayout === "grid" ? "☰ List view" : "▦ Grid view";
}

function updateHomeStates() {
  for (const card of document.querySelectorAll("#homeGrid .devcard")) {
    const slug = card.dataset.slug;
    const offline = offlineSlugs.has(slug);
    const pl = homePlayers.get(slug);
    const live = !!(pl && pl.live);
    const dot = card.querySelector(".dot");
    const state = card.querySelector(".state");
    if (dot) dot.classList.toggle("live", live);
    if (state) {
      state.textContent = offline ? "Offline" : live ? "live"
        : (pl && pl.phase === "loading" ? "Loading…" : "Connecting…");
      state.classList.toggle("off", offline);
    }
    setLoadState(card.querySelector(".loadstate"),
      offline ? "off" : live ? "live" : (pl ? pl.phase : "connecting"));
  }
}

document.querySelectorAll("#homeChips .chip[data-filter]").forEach(b =>
  b.addEventListener("click", () => {
    homeFilter = b.dataset.filter;
    document.querySelectorAll("#homeChips .chip[data-filter]").forEach(x =>
      x.classList.toggle("active", x === b));
    renderHome();
  }));
$("homeLayoutBtn").addEventListener("click", () => {
  homeLayout = homeLayout === "grid" ? "list" : "grid";
  localStorage.setItem(HOME_LAYOUT_KEY, homeLayout);
  renderHome();
});

// ---- Cameras: live grid / multi-view ----
function visibleCameras() {
  return layout === "4" ? cameras.slice(0, 4) : cameras;
}

function buildGrid() {
  const grid = $("grid");
  grid.classList.remove("layout-auto", "layout-1", "layout-4");
  grid.classList.add("layout-" + layout);
  document.body.dataset.layout = layout;
  const seen = new Set();
  for (const cam of visibleCameras()) {
    seen.add(cam.slug);
    if (players.has(cam.slug)) continue;
    const tile = document.createElement("div");
    tile.className = "tile";
    tile.id = "tile-" + cam.slug;
    tile.innerHTML =
      '<div class="frame"><video muted playsinline autoplay></video>' +
      '<div class="loadstate connecting"><span class="loadanim"></span><span class="loadlabel">Connecting…</span></div></div>' +
      '<div class="tiletag"><span class="dot"></span>' +
      '<span class="name"></span><span class="state"></span></div>';
    tile.querySelector(".name").textContent = cam.name;
    tile.addEventListener("click", () => openLive(cam, "cameras"));
    grid.appendChild(tile);
    const dot = tile.querySelector(".dot");
    const state = tile.querySelector(".state");
    attachPlayer(cam, tile.querySelector("video"), (live, text, phase) => {
      dot.classList.toggle("live", !!live);
      state.textContent = text;
      setLoadState(tile.querySelector(".loadstate"), phase || (live ? "live" : "connecting"));
    });
  }
  for (const [slug, pl] of players) {
    if (!seen.has(slug)) {
      stopPlayer(pl);
      const t = $("tile-" + slug);
      if (t) t.remove();
      players.delete(slug);
    }
  }
  $("empty").classList.toggle("hidden", cameras.length > 0);
}

function applyLayout() {
  localStorage.setItem(LAYOUT_KEY, layout);
  document.body.dataset.layout = layout;
  document.querySelectorAll(".seg button").forEach(b =>
    b.classList.toggle("active", b.dataset.layout === layout));
  if (currentView === "cameras" && appStarted) {
    stopAllPlayers();
    buildGrid();
  }
}

// ---- Fullscreen ----
function toggleFullscreen() {
  if (document.fullscreenElement) {
    document.exitFullscreen().catch(() => {});
    return;
  }
  let target = document.documentElement;
  if (currentView === "cameras") target = $("grid");
  else if (currentView === "live") target = $("live-video");
  else if (currentView === "playback") target = $("pb-video").src ? $("pb-video") : document.documentElement;
  let p = null;
  try { p = target.requestFullscreen ? target.requestFullscreen() : null; } catch (e) { p = null; }
  const fallback = () => {
    const v = document.querySelector("#grid video, #live-video, #pb-video");
    if (v && v.requestFullscreen) v.requestFullscreen().catch(() => {});
  };
  if (p && p.catch) p.catch(fallback);
  else if (!p) fallback();
}
document.addEventListener("fullscreenchange", () => {
  const fs = $("fs");
  if (fs) fs.textContent = document.fullscreenElement ? "⛶ Exit fullscreen" : "⛶ Fullscreen";
});
$("fs").addEventListener("click", toggleFullscreen);
document.querySelectorAll(".seg button").forEach(b =>
  b.addEventListener("click", () => { layout = b.dataset.layout; applyLayout(); }));

// ---- Live view: one camera ----
function stopLivePlayer() {
  if (livePlayer) {
    stopPlayer(livePlayer);
    for (const [slug, pl] of players) if (pl === livePlayer) players.delete(slug);
    livePlayer = null;
  }
  const v = $("live-video");
  v.pause();
  v.removeAttribute("src");
  v.load();
  $("liveStatus").textContent = "";
  $("liveToast").textContent = "";
  $("liveBox").classList.remove("show-ctl");
  setLoadState($("liveLoad"), "connecting");
  $("liveRecap").classList.add("hidden");
  $("liveRecap").innerHTML = "";
}

function openLive(cam, from) {
  returnView = from || "home";
  liveCam = cam;
  currentView = "live";
  for (const v of VIEWS) $("view-" + v).classList.toggle("hidden", v !== "live");
  document.querySelectorAll("#bottomnav button").forEach(b =>
    b.classList.toggle("active", b.dataset.nav === returnView));
  $("liveName").textContent = cam.name;
  $("liveStatus").textContent = "Connecting…";
  // Stop any grid and Home players so only this feed is demanded.
  stopAllPlayers();
  stopHomePlayers();
  // New stream starts muted; reset the mute button with it.
  const lv = $("live-video");
  lv.muted = true;
  delete $("ctlMute").dataset.noaudio;
  if (liveMuteUpdate) liveMuteUpdate();
  livePlayer = attachPlayer(cam, $("live-video"), (live, text, phase) => {
    $("liveStatus").textContent = live ? "Live" : text;
    $("liveBadge").style.display = live ? "" : "none";
    setLoadState($("liveLoad"), phase || (live ? "live" : "connecting"));
  });
  showOverlayTemporarily();
  renderLiveRecap();      // hides until clips arrive, if none yet
  refreshRecapClips();    // fetches the index; re-renders the card
  window.scrollTo(0, 0);
}

// ---- Single-camera recap: "Playback & Download" card ----
// Portrait only (CSS hides it in landscape); appears under the live
// video when this camera has clips. Built from allClips, which
// loadClips()/refreshRecapClips() keep fresh.
function fmtAgo(unix) {
  const mins = Math.max(0, Math.floor((Date.now() / 1000 - unix) / 60));
  if (mins < 1) return "Activity detected just now";
  if (mins < 60) return "Activity detected " + mins + " min ago";
  const h = Math.floor(mins / 60);
  if (h < 24) return "Activity detected " + h + "h " + (mins % 60) + "m ago";
  return "Activity detected " + Math.floor(h / 24) + "d ago";
}

function renderLiveRecap() {
  const box = $("liveRecap");
  if (!box) return;
  if (!liveCam) { box.classList.add("hidden"); box.innerHTML = ""; return; }
  const clips = allClips.filter(c => c.slug === liveCam.slug).slice(0, 5);
  if (!clips.length) { box.classList.add("hidden"); box.innerHTML = ""; return; }
  let html = '<div class="recap-head"><span>Playback &amp; Download</span>' +
    '<button type="button" id="recapAll">See all ›</button></div>' +
    '<div class="recap-strip">';
  for (const clip of clips) {
    html += '<button type="button" class="recap-item" data-token="' +
      escAttr(clip.slug + "/" + clip.start) + '">' +
      '<span class="recap-thumb">' + clipThumbHtml(clip) + "</span>" +
      '<span class="recap-time">' + escHtml(fmtClipTime(clip.start)) + "</span>" +
      (fmtDur(clip.duration)
        ? '<span class="recap-dur">' + fmtDur(clip.duration) + "</span>" : "") +
      "</button>";
  }
  html += '</div><div class="recap-activity">' + escHtml(fmtAgo(clips[0].start)) + "</div>";
  box.innerHTML = html;
  box.classList.remove("hidden");
  box.querySelectorAll(".recap-item").forEach(btn => {
    armThumbFallback(btn);
    btn.addEventListener("click", () => {
      const clip = allClips.find(c => (c.slug + "/" + c.start) === btn.dataset.token);
      if (!clip) return;
      const slug = liveCam ? liveCam.slug : "";
      goPlaybackFor(slug);
      // The list was just rendered from the clips we already have,
      // so the row is there now — play it straight away.
      const rows = [...document.querySelectorAll("#pb-list .clip")];
      const row = rows.find(r => r.dataset.token === clip.slug + "/" + clip.start) || null;
      playClip(clip, row, 0);
    });
  });
  const all = box.querySelector("#recapAll");
  if (all) all.addEventListener("click", () => goPlaybackFor(liveCam ? liveCam.slug : ""));
}

// Quiet clip-index refresh for the recap: unlike loadClips it never
// forces a sign-out on 401 — a stale session is the camera
// refresh's problem, not the recap's.
async function refreshRecapClips() {
  try {
    const r = await apiFetch("/api/nvr/clips");
    const data = await r.json();
    allClips = data.clips || [];
    renderClipList();
    renderTimeline();
    renderLiveRecap();
  } catch (e) { /* recap stays hidden; nothing else breaks */ }
}

function goPlaybackFor(slug) {
  liveCam = null;
  showView("playback");
  if (slug) {
    const sel = $("pb-camera");
    if ([...sel.options].some(o => o.value === slug)) {
      sel.value = slug;
      renderClipList();
      renderTimeline();
    }
  }
}

$("liveBack").addEventListener("click", () => {
  const back = returnView === "cameras" ? "cameras" : "home";
  liveCam = null;
  showView(back);
});

// Tap the video once to show/hide the overlay controls.
let overlayTimer = null;
function showOverlayTemporarily() {
  $("liveBox").classList.add("show-ctl");
  clearTimeout(overlayTimer);
  overlayTimer = setTimeout(() => $("liveBox").classList.remove("show-ctl"), 4000);
}
$("live-video").addEventListener("click", () => {
  if ($("liveBox").classList.contains("show-ctl")) $("liveBox").classList.remove("show-ctl");
  else showOverlayTemporarily();
});
$("liveBox").addEventListener("click", (ev) => {
  if (ev.target.closest(".overlay button")) return;
});

function goMultiScreen() {
  layout = "4";
  applyLayout();
  liveCam = null;
  showView("cameras");
}
$("ctlMulti").addEventListener("click", goMultiScreen);
$("btnMulti").addEventListener("click", goMultiScreen);
$("ctlFs").addEventListener("click", () => {
  const v = $("live-video");
  if (v.requestFullscreen) v.requestFullscreen().catch(() => {});
});
$("liveGear").addEventListener("click", () => {
  if (!liveCam) return;
  openSheet(liveCam.name,
    "<p>Detection, recording and other settings live in the NVR app on your PC (Devices &amp; Settings).</p>" +
    "<p>Remote viewing for this camera is " + (livePlayer && livePlayer.live ? "live" : "waking up") + ".</p>");
});

// ---- Mute / unmute ----
// Videos start muted (autoplay policy). The button toggles muted
// and shows the state. If the stream turns out to carry no audio
// track, the button says so instead of faking it: a muted video
// still decodes audio, so webkitAudioDecodedByteCount staying 0 a
// few seconds into playback means there is no audio to unmute.
function wireMuteButton(video, btn, labelEl, emoji) {
  if (!video || !btn) return null;
  labelEl = labelEl || btn;
  const update = () => {
    const state = btn.dataset.noaudio === "1" ? "noaudio"
      : video.muted ? "muted" : "on";
    labelEl.textContent = state === "noaudio" ? (emoji ? "🔇 No audio" : "No audio")
      : state === "muted" ? (emoji ? "🔇 Muted" : "Muted")
      : (emoji ? "🔊 Sound on" : "Sound on");
  };
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    if (btn.dataset.noaudio === "1") return;
    video.muted = !video.muted;
    update();
  });
  video.addEventListener("volumechange", update);
  video.addEventListener("playing", () => {
    setTimeout(() => {
      if (video.paused || video.ended) return;
      const noTrack = !(video.audioTracks && video.audioTracks.length);
      if (noTrack && typeof video.webkitAudioDecodedByteCount === "number" &&
          video.webkitAudioDecodedByteCount === 0) {
        btn.dataset.noaudio = "1";
        update();
      }
    }, 6000);
  });
  update();
  return update;
}
const liveMuteUpdate = wireMuteButton($("live-video"), $("ctlMute"), $("ctlMuteLabel"), false);
const pbMuteUpdate = wireMuteButton($("pb-video"), $("pb-mute"), null, true);

async function screenshotLive() {
  const v = $("live-video");
  $("liveToast").textContent = "";
  try {
    const c = document.createElement("canvas");
    c.width = v.videoWidth || 1280;
    c.height = v.videoHeight || 720;
    const ctx = c.getContext("2d");
    ctx.drawImage(v, 0, 0, c.width, c.height);
    const url = c.toDataURL("image/png");
    const a = document.createElement("a");
    a.href = url;
    a.download = (liveCam ? liveCam.slug : "camera") + "-screenshot.png";
    a.click();
  } catch (e) {
    $("liveToast").textContent = "Couldn't capture this stream on this browser.";
  }
}
$("ctlShot").addEventListener("click", screenshotLive);
$("btnShot").addEventListener("click", screenshotLive);

$("btnPlayback").addEventListener("click", () => goPlaybackFor(liveCam ? liveCam.slug : ""));

// ---- Camera list refresh ----
async function refreshCameras() {
  try {
    const r = await apiFetch("/api/nvr/cameras");
    const data = await r.json();
    cameras = data.cameras || [];
    populatePlaybackCameras();
    if (currentView === "home") renderHome();
    else if (currentView === "cameras") buildGrid();
  } catch (e) {
    if (String(e.message).includes("unauthorized")) {
      stopAllPlayers();
      stopHomePlayers();
      stopLivePlayer();
      stopClipPoll();
      showLogin("Signed out — please sign in again.");
    }
  }
}

// Coming back to the tab: re-assert demand for every visible
// camera at once instead of waiting for each 20s timer.
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible") return;
  if (currentView === "cameras") for (const cam of visibleCameras()) requestCamera(cam.slug);
  else if (currentView === "live" && liveCam) requestCamera(liveCam.slug);
});

// ---- Playback ----
// Clips come from the relay's clip index; a tap asks the home PC
// to upload the file, then the viewer streams it from the relay.
let allClips = [];       // playback clips, newest first
let clipPollTimer = null;
let clipToken = null;    // token of the clip currently being prepared/played
let playingToken = null; // token of the clip playing in the Play tab (row highlight survives list re-renders)
let pbTab = "play";      // "play" | "download"
const SPEEDS = [0.25, 0.5, 1, 2];
let speedIdx = 2;

// "YYYY-MM-DD" in the viewer's local timezone, for date filtering.
function localDateStr(d) {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return y + "-" + m + "-" + day;
}

function populatePlaybackCameras() {
  const sel = $("pb-camera");
  const prev = sel.value;
  sel.innerHTML = '<option value="">All cameras</option>';
  for (const cam of cameras) {
    const o = document.createElement("option");
    o.value = cam.slug;
    o.textContent = cam.name;
    sel.appendChild(o);
  }
  if ([...sel.options].some(o => o.value === prev)) sel.value = prev;
}

function fmtSize(bytes) {
  if (bytes == null) return "";
  if (bytes >= 1048576) return (bytes / 1048576).toFixed(1) + " MB";
  if (bytes >= 1024) return Math.round(bytes / 1024) + " KB";
  return bytes + " B";
}

function fmtClipTime(unix) {
  const d = new Date(unix * 1000);
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric" }) +
    " · " + d.toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" });
}

function fmtClock(seconds) {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  const p = (n) => String(n).padStart(2, "0");
  return p(h) + ":" + p(m) + ":" + p(s);
}

// Escape text for an HTML attribute (snapshot URLs) or text node.
function escAttr(s) {
  return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}
function escHtml(s) {
  return escAttr(s).replace(/>/g, "&gt;");
}

// Snapshot thumbnail for a clip: the relay's play-token URL when
// the clip has one, else the camera icon. Older relays send no
// snapshot field at all — treated as null.
// The relay serves snapshot URLs as relative paths, so prefix them
// with the relay base (same as play URLs) — a bare relative src
// would resolve against the viewer's own origin and 404.
function clipThumbHtml(clip) {
  if (clip && clip.snapshot) {
    const src = clip.snapshot.startsWith("http") ? clip.snapshot : RELAY + clip.snapshot;
    return '<img class="clip-thumb-img" src="' + escAttr(src) + '" alt="">';
  }
  return CAM_ICON;
}

// Duration in m:ss (h:mm:ss past an hour); "" when unknown (0).
function fmtDur(seconds) {
  seconds = Math.floor(seconds || 0);
  if (seconds <= 0) return "";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  const p = (n) => String(n).padStart(2, "0");
  return h > 0 ? h + ":" + p(m) + ":" + p(s) : m + ":" + p(s);
}

// Person vs motion icon for a clip; "" when the relay didn't say.
function kindIcon(kind) {
  if (kind === "person") return '<span class="clip-kind" title="Person detected">👤</span>';
  if (kind === "motion") return '<span class="clip-kind" title="Motion detected">🏃</span>';
  return "";
}

// If a snapshot image fails (stale play token, missing file), swap
// back to the camera icon once — never retry, never spin.
function armThumbFallback(scopeEl) {
  const img = scopeEl.querySelector(".clip-thumb img, .recap-thumb img");
  if (!img) return;
  img.addEventListener("error", () => {
    const t = img.closest(".clip-thumb, .recap-thumb");
    if (t) t.innerHTML = CAM_ICON;
  }, { once: true });
}

function filteredClips() {
  const slug = $("pb-camera").value;
  const date = $("pb-date").value;
  return allClips.filter(c =>
    (!slug || c.slug === slug) &&
    (!date || localDateStr(new Date(c.start * 1000)) === date));
}

function clipEnd(clip, sorted) {
  // End of this segment: the next segment's start, else 10 minutes
  // (the NVR's default segment length). `sorted` is ascending.
  const i = sorted.indexOf(clip);
  if (i >= 0 && i < sorted.length - 1) return sorted[i + 1].start;
  return clip.start + 600;
}

function renderClipList() {
  const list = filteredClips();
  const el = $("pb-list");
  el.innerHTML = "";
  $("pb-empty").classList.toggle("hidden", list.length > 0);
  for (const clip of list) {
    const row = document.createElement("div");
    row.className = "clip";
    row.dataset.token = clip.slug + "/" + clip.start;
    row.innerHTML =
      '<div class="clip-main"><span class="clip-thumb">' + clipThumbHtml(clip) + "</span>" +
      '<span class="clip-name"></span>' + kindIcon(clip.kind) +
      '<span class="clip-meta"></span></div>' +
      '<div class="clip-status"><span class="stext">Tap to play</span>' +
      (isAdmin() ? '<button class="del" type="button">Delete</button>' : "") + "</div>";
    row.querySelector(".clip-name").textContent = clip.name;
    row.querySelector(".clip-meta").textContent =
      fmtClipTime(clip.start) + " · " + fmtSize(clip.size) +
      (fmtDur(clip.duration) ? " · " + fmtDur(clip.duration) : "");
    armThumbFallback(row);
    // Keep the playing row highlighted (and its status) when the
    // list re-renders under it — e.g. after tapping a recap item,
    // whose fresh clip index arrives mid-playback.
    if (playingToken && row.dataset.token === playingToken) {
      row.classList.add("active");
      const st = row.querySelector(".clip-status .stext");
      if (st && $("pb-status").textContent) st.textContent = $("pb-status").textContent;
    }
    row.addEventListener("click", () => playClip(clip, row, 0));
    const delBtn = row.querySelector(".del");
    if (delBtn) delBtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      deleteClip(clip, delBtn, row.querySelector(".clip-status .stext"));
    });
    el.appendChild(row);
  }
  renderDownloadList();
}

function renderDownloadList() {
  const list = filteredClips();
  const el = $("pb-dl-list");
  el.innerHTML = "";
  $("pb-dl-empty").classList.toggle("hidden", list.length > 0);
  for (const clip of list) {
    const row = document.createElement("div");
    row.className = "clip";
    row.innerHTML =
      '<div class="clip-main"><input type="checkbox" aria-label="Select clip">' +
      '<span class="clip-thumb">' + clipThumbHtml(clip) + "</span>" +
      '<span class="clip-name"></span>' + kindIcon(clip.kind) +
      '<span class="clip-meta"></span>' +
      '<button class="dl" type="button" aria-label="Download clip">' +
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>' +
      "</button></div>" +
      '<div class="clip-status"><span class="stext"></span>' +
      (isAdmin() ? '<button class="del" type="button">Delete</button>' : "") + "</div>";
    row.querySelector(".clip-name").textContent = clip.name;
    row.querySelector(".clip-meta").textContent =
      fmtClipTime(clip.start) + " · " + fmtSize(clip.size) +
      (fmtDur(clip.duration) ? " · " + fmtDur(clip.duration) : "");
    armThumbFallback(row);
    const token = clip.slug + "/" + clip.start;
    row.querySelector(".dl").addEventListener("click", (ev) => {
      ev.stopPropagation();
      downloadClip(clip, row.querySelector(".clip-status .stext"));
    });
    const delBtn = row.querySelector(".del");
    if (delBtn) delBtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      deleteClip(clip, delBtn, row.querySelector(".clip-status .stext"));
    });
    row.dataset.token = token;
    el.appendChild(row);
  }
}

async function loadClips() {
  try {
    const r = await apiFetch("/api/nvr/clips");
    const data = await r.json();
    allClips = data.clips || [];
    renderClipList();
    renderTimeline();
    renderLiveRecap(); // refresh the single-camera card if it's showing
  } catch (e) {
    if (String(e.message).includes("unauthorized")) unauthDuringPlayback();
  }
}

function stopClipPoll() {
  if (clipPollTimer) { clearInterval(clipPollTimer); clipPollTimer = null; }
  clipToken = null;
  playingToken = null;
}

// The hls.js instance for the clip currently playing in Playback.
// Destroyed whenever a different clip is tapped, playback is left,
// or HLS fails and the MP4 fallback takes over.
let clipHls = null;
function stopClipHls() {
  if (clipHls) { try { clipHls.destroy(); } catch (e) {} clipHls = null; }
}

function unauthDuringPlayback() {
  stopClipPoll();
  stopClipHls();
  stopAllPlayers();
  stopHomePlayers();
  stopLivePlayer();
  showLogin("Signed out — please sign in again.");
}

// One POST per request; then poll clip-status every 3s (up to
// ~3 min) while the home PC uploads the file. Resolves with
// {play, hls} — the signed MP4 URL and the HLS playlist URL
// (null when the relay has none) — or rejects with
// "missing" / "timeout".
function ensureClipReady(token, onStatus) {
  return new Promise(async (resolve, reject) => {
    let resp;
    try {
      resp = await apiFetch("/api/nvr/clip-request", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
    } catch (e) {
      if (String(e.message).includes("unauthorized")) return reject(new Error("unauthorized"));
      return reject(new Error("unreachable"));
    }
    const data = await resp.json().catch(() => ({}));
    if (data.status === "ready" && data.play)
      return resolve({ play: data.play, hls: data.hls || null });
    if (data.status === "missing") return reject(new Error("missing"));
    onStatus && onStatus("Preparing clip on your home PC…");
    const started = Date.now();
    const timer = setInterval(async () => {
      if (clipToken !== token) { clearInterval(timer); return reject(new Error("replaced")); }
      if (Date.now() - started > 180000) {
        clearInterval(timer);
        return reject(new Error("timeout"));
      }
      let r;
      try {
        r = await apiFetch("/api/nvr/clip-status?token=" + encodeURIComponent(token));
      } catch (e) {
        clearInterval(timer);
        if (String(e.message).includes("unauthorized")) return reject(new Error("unauthorized"));
        return reject(new Error("unreachable"));
      }
      const st = await r.json().catch(() => ({}));
      if (st.status === "ready" && st.play) {
        clearInterval(timer);
        resolve({ play: st.play, hls: st.hls || null });
      } else if (st.status === "missing") {
        clearInterval(timer);
        reject(new Error("missing"));
      }
    }, 3000);
    clipPollTimer = timer;
  });
}

function playUrlOf(play) {
  return play.startsWith("http") ? play : RELAY + play;
}

async function playClip(clip, row, offsetSec) {
  stopClipPoll();
  stopClipHls();
  document.querySelectorAll("#pb-list .clip").forEach(c => {
    c.classList.remove("active");
    const s = c.querySelector(".clip-status .stext");
    if (s) s.textContent = "Tap to play";
  });
  if (row) row.classList.add("active");
  const token = clip.slug + "/" + clip.start;
  clipToken = token;
  playingToken = token;
  const statusEl = row ? row.querySelector(".clip-status .stext") : $("pb-status");
  const setStatus = (t) => {
    if (statusEl) statusEl.textContent = t;
    $("pb-status").textContent = t;
  };
  setStatus("Preparing clip on your home PC…");
  $("pb-player").classList.remove("hidden");
  const v = $("pb-video");
  v.pause();
  v.removeAttribute("src");
  v.load();
  // Clips start muted; reset the mute button with each new clip.
  v.muted = true;
  delete $("pb-mute").dataset.noaudio;
  if (pbMuteUpdate) pbMuteUpdate();
  try {
    const ready = await ensureClipReady(token, setStatus);
    if (clipToken !== token) return; // another clip was tapped meanwhile
    const play = ready.play, hlsUrl = ready.hls;
    const seek = () => {
      if (offsetSec > 0 && v.duration && offsetSec < v.duration)
        v.currentTime = offsetSec;
      v.removeEventListener("loadedmetadata", seek);
    };
    v.addEventListener("loadedmetadata", seek);
    // Stream the clip as HLS when the relay offers a playlist:
    // the first segment is only a few seconds of video, so playback
    // starts while the rest streams in. The play token is already in
    // the URL, so hls.js must NOT add an Authorization header.
    const playMp4 = () => {
      stopClipHls();
      if (clipToken !== token) return;
      v.addEventListener("loadedmetadata", seek);
      v.src = playUrlOf(play);
      v.play().catch(() => {});
    };
    if (hlsUrl && window.Hls && Hls.isSupported()) {
      const hls = new Hls({});
      clipHls = hls;
      hls.loadSource(playUrlOf(hlsUrl));
      hls.attachMedia(v);
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        if (clipToken !== token) return;
        v.play().catch(() => {});
      });
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (!data.fatal) return;
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          hls.recoverMediaError();
          return;
        }
        // HLS failed (e.g. segmentation broke): fall back to the
        // progressive MP4 rather than a dead player.
        playMp4();
      });
    } else if (hlsUrl && v.canPlayType("application/vnd.apple.mpegurl")) {
      // Safari: native HLS.
      v.src = playUrlOf(hlsUrl);
      v.play().catch(() => {});
    } else {
      playMp4();
    }
    setStatus("Ready — playing");
  } catch (e) {
    if (clipToken !== token) return;
    if (e.message === "unauthorized") { unauthDuringPlayback(); return; }
    const msg = e.message === "missing" ? "Clip no longer available on the PC"
      : e.message === "timeout" ? "Timed out — the home PC may be offline. Tap to try again."
      : "Couldn't reach the home PC — try again.";
    setStatus(msg);
  }
}

async function downloadClip(clip, statusEl) {
  const token = clip.slug + "/" + clip.start;
  clipToken = token;
  const setStatus = (t) => { if (statusEl) statusEl.textContent = t; };
  setStatus("Preparing download…");
  try {
    const ready = await ensureClipReady(token, setStatus);
    if (clipToken !== token) return;
    const play = ready.play;
    const a = document.createElement("a");
    a.href = playUrlOf(play);
    a.download = clip.slug + "-" + clip.start + ".mp4";
    document.body.appendChild(a);
    a.click();
    a.remove();
    setStatus("Download started");
  } catch (e) {
    if (e.message === "unauthorized") { unauthDuringPlayback(); return; }
    setStatus(e.message === "missing" ? "Clip no longer available on the PC"
      : e.message === "timeout" ? "Timed out — the home PC may be offline."
      : "Couldn't reach the home PC — try again.");
  }
}

// ---- Clip delete (admin only) ----
// Two-tap confirm on the button itself: first tap arms it
// ("Sure?"), second tap sends the delete. Viewers never get a
// Delete button at all, so there is no delete path for them.
async function deleteClip(clip, btn, statusEl) {
  if (btn.dataset.armed !== "1") {
    btn.dataset.armed = "1";
    btn.textContent = "Sure?";
    btn.classList.add("armed");
    setTimeout(() => {
      if (!btn.isConnected) return;
      delete btn.dataset.armed;
      btn.classList.remove("armed");
      btn.textContent = "Delete";
    }, 5000);
    return;
  }
  delete btn.dataset.armed;
  btn.classList.remove("armed");
  btn.textContent = "Deleting…";
  btn.disabled = true;
  const setStatus = (t) => { if (statusEl) statusEl.textContent = t; };
  try {
    await apiFetch("/api/nvr/clip-delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // token (slug/start) is what the relay + NVR delete by; file
      // is the relay's copy name. The old camera-name fallback
      // never matched a real file.
      body: JSON.stringify({ camera: clip.slug,
                             file: clip.slug + "-" + clip.start + ".mp4",
                             token: clip.slug + "/" + clip.start }),
    });
    allClips = allClips.filter(c => c !== clip);
    renderClipList();
    renderTimeline();
  } catch (e) {
    btn.disabled = false;
    btn.textContent = "Delete";
    if (e.status === 403) {
      setStatus("You don't have permission to delete clips.");
    } else if (e.message === "unauthorized" || e.status === 401) {
      unauthDuringPlayback();
    } else {
      setStatus("Couldn't delete the clip — try again.");
    }
  }
}

// ---- 24-hour timeline (blue = recorded segments) ----
function renderTimeline() {
  const tl = $("pb-timeline");
  tl.querySelectorAll(".seg-block").forEach(b => b.remove());
  const sorted = filteredClips().slice().sort((a, b) => a.start - b.start);
  const date = $("pb-date").value;
  if (!date) return;
  const dayStart = new Date(date + "T00:00:00").getTime() / 1000;
  for (const clip of sorted) {
    const end = clipEnd(clip, sorted);
    const left = Math.max(0, (clip.start - dayStart) / 86400 * 100);
    const right = Math.min(100, (end - dayStart) / 86400 * 100);
    if (right <= 0 || left >= 100 || right <= left) continue;
    const block = document.createElement("div");
    block.className = "seg-block";
    block.style.left = left + "%";
    block.style.width = (right - left) + "%";
    tl.appendChild(block);
  }
}

function timelineSeconds(ev) {
  const rect = $("pb-timeline").getBoundingClientRect();
  const frac = Math.min(1, Math.max(0, (ev.clientX - rect.left) / rect.width));
  return frac * 86400;
}

function jumpToTimeOfDay(seconds) {
  const date = $("pb-date").value;
  if (!date) return;
  const dayStart = new Date(date + "T00:00:00").getTime() / 1000;
  const t = dayStart + seconds;
  const sorted = filteredClips().slice().sort((a, b) => a.start - b.start);
  let hit = null;
  for (const clip of sorted) {
    if (t >= clip.start && t < clipEnd(clip, sorted)) { hit = clip; break; }
  }
  if (!hit) {
    // No recording covers that time: play the latest clip before it.
    for (const clip of sorted) if (clip.start <= t) hit = clip;
  }
  if (!hit) {
    $("pb-status").textContent = "No recording at " + fmtClock(seconds) + " on this date.";
    return;
  }
  const rows = [...document.querySelectorAll("#pb-list .clip")];
  const row = rows.find(r => r.dataset.token === hit.slug + "/" + hit.start) || null;
  playClip(hit, row, Math.max(0, t - hit.start));
}

let scrubbing = false;
$("pb-timeline").addEventListener("pointerdown", (ev) => {
  scrubbing = true;
  $("pb-timeline").setPointerCapture(ev.pointerId);
  const s = timelineSeconds(ev);
  const tip = $("pb-tip");
  tip.style.display = "block";
  tip.style.left = (s / 86400 * 100) + "%";
  tip.textContent = fmtClock(s);
  $("pb-now").style.display = "block";
  $("pb-now").style.left = (s / 86400 * 100) + "%";
});
$("pb-timeline").addEventListener("pointermove", (ev) => {
  if (!scrubbing) return;
  const s = timelineSeconds(ev);
  const tip = $("pb-tip");
  tip.style.left = (s / 86400 * 100) + "%";
  tip.textContent = fmtClock(s);
  $("pb-now").style.left = (s / 86400 * 100) + "%";
});
$("pb-timeline").addEventListener("pointerup", (ev) => {
  if (!scrubbing) return;
  scrubbing = false;
  $("pb-tip").style.display = "none";
  $("pb-now").style.display = "none";
  jumpToTimeOfDay(timelineSeconds(ev));
});

// ---- Playback transport (fixes Tapo's imprecise scrubbing) ----
$("pb-back10").addEventListener("click", () => {
  const v = $("pb-video");
  v.currentTime = Math.max(0, v.currentTime - 10);
});
$("pb-fwd10").addEventListener("click", () => {
  const v = $("pb-video");
  v.currentTime = Math.min(v.duration || v.currentTime + 10, v.currentTime + 10);
});
$("pb-speed").addEventListener("click", () => {
  speedIdx = (speedIdx + 1) % SPEEDS.length;
  $("pb-video").playbackRate = SPEEDS[speedIdx];
  $("pb-speed").textContent = "Speed " + SPEEDS[speedIdx] + "×";
});
$("pb-jump-go").addEventListener("click", () => {
  const val = $("pb-jump-time").value;
  if (!val) return;
  const parts = val.split(":").map(Number);
  const seconds = (parts[0] || 0) * 3600 + (parts[1] || 0) * 60 + (parts[2] || 0);
  jumpToTimeOfDay(seconds);
});

// ---- Playback tabs + filters ----
document.querySelectorAll(".pbtabs button").forEach(b =>
  b.addEventListener("click", () => {
    pbTab = b.dataset.pbtab;
    document.querySelectorAll(".pbtabs button").forEach(x =>
      x.classList.toggle("active", x === b));
    $("pb-play-tab").classList.toggle("hidden", pbTab !== "play");
    $("pb-download-tab").classList.toggle("hidden", pbTab !== "download");
    if (pbTab === "download") renderDownloadList();
  }));
$("pb-camera").addEventListener("change", () => { renderClipList(); renderTimeline(); });
$("pb-date").addEventListener("change", () => { renderClipList(); renderTimeline(); });
$("pb-date").value = localDateStr(new Date());
$("pb-dl-selected").addEventListener("click", async () => {
  const rows = [...document.querySelectorAll("#pb-dl-list .clip")];
  const picked = rows.filter(r => r.querySelector('input[type="checkbox"]').checked);
  if (picked.length === 0) return;
  const clips = filteredClips();
  for (const row of picked) {
    const clip = clips.find(c => (c.slug + "/" + c.start) === row.dataset.token);
    if (!clip) continue;
    await downloadClip(clip, row.querySelector(".clip-status .stext"));
  }
});

// ---- Me ----
document.querySelectorAll("#themeSeg button").forEach(b =>
  b.addEventListener("click", () => {
    theme = b.dataset.theme;
    localStorage.setItem(THEME_KEY, theme);
    applyTheme();
  }));
$("signout").addEventListener("click", () => {
  localStorage.removeItem(LS_KEY);
  localStorage.removeItem(REMEMBER_KEY); // signing out forgets the saved sign-in too
  session = null;
  stopAllPlayers();
  stopHomePlayers();
  stopLivePlayer();
  stopClipPoll();
  stopClipHls();
  currentView = "home";
  liveCam = null;
  showLogin();
});

// ---- Boot ----
function startApp() {
  if (!appStarted) {
    appStarted = true;
    setInterval(refreshCameras, 30000);
  }
  // Always re-fetch: a re-login after sign-out / session expiry
  // must repopulate the camera list, not just the first boot.
  refreshCameras();
}

applyTheme();
applyLayout();

if ("serviceWorker" in navigator)
  navigator.serviceWorker.register("sw.js").catch(() => {});

if (session) {
  showApp();
  startApp();
} else {
  showLogin();
}
