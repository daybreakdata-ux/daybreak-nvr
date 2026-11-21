// Full web UI for Daybreak NVR: one self-contained page (HTML +
// CSS + JS, no external assets) served at /ui/. Same dark-slate
// SmartPSS-style look as the desktop app, responsive so it works
// as a phone PWA (Phase 1 of the mobile plan) as well as on a
// desktop browser.
package webview

import (
	"net/http"
)

const uiHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#0b1118">
<link rel="manifest" href="/manifest.webmanifest">
<link rel="icon" href="/ui/icon.png">
<title>Daybreak NVR</title>
<style>
:root{--bg:#0b1118;--panel:#18202a;--panel2:#202a37;--line:#2a3644;--txt:#d8e0e9;--dim:#8b98a8;--teal:#168573;--lime:#b2c13a;--blue:#64b0f0;--red:#e06c5a}
*{box-sizing:border-box}
body{background:var(--bg);color:var(--txt);font-family:system-ui,-apple-system,sans-serif;margin:0;font-size:15px}
header{display:flex;align-items:center;gap:14px;padding:10px 16px;background:#101823;border-bottom:1px solid var(--line);position:sticky;top:0;z-index:5;flex-wrap:wrap}
header h1{font-size:18px;margin:0;letter-spacing:.5px}
header h1 b{color:var(--lime)} header h1 span{color:var(--teal)}
#status{color:var(--dim);font-size:13px}
nav{display:flex;gap:6px;margin-left:auto;flex-wrap:wrap}
nav button{background:var(--panel2);color:var(--txt);border:1px solid var(--line);border-radius:8px;padding:8px 14px;font-size:14px;cursor:pointer}
nav button.on{background:var(--teal);border-color:var(--teal);color:#fff;font-weight:600}
main{padding:14px 16px 40px;max-width:1200px;margin:0 auto}
section{display:none} section.on{display:block}
h2{font-size:16px;margin:6px 0 12px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px;margin-bottom:14px}
.card h3{margin:0 0 10px;font-size:15px}
label{display:block;font-size:13px;color:var(--dim);margin:10px 0 4px}
input,select,textarea{width:100%;background:#0e141c;border:1px solid var(--line);color:var(--txt);border-radius:7px;padding:9px 10px;font-size:14px}
input[type=checkbox]{width:auto}
button{background:var(--teal);color:#fff;border:0;border-radius:8px;padding:9px 16px;font-size:14px;cursor:pointer}
button.ghost{background:var(--panel2);color:var(--txt);border:1px solid var(--line)}
button.danger{background:#5a2b24}
button:disabled{opacity:.5;cursor:default}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.grid{display:grid;gap:10px}
.grid.g1{grid-template-columns:1fr}
.grid.g4{grid-template-columns:repeat(2,1fr)}
.grid.g9{grid-template-columns:repeat(3,1fr)}
.grid.g16{grid-template-columns:repeat(4,1fr)}
@media(max-width:700px){.grid.g9,.grid.g16{grid-template-columns:repeat(2,1fr)}}
.tile{background:var(--panel2);border-radius:10px;overflow:hidden;cursor:pointer;border:1px solid var(--line)}
.tile img{width:100%;display:block;aspect-ratio:16/9;object-fit:cover;background:#05070a}
.tile .nm{padding:8px 10px;font-weight:600;font-size:14px;display:flex;justify-content:space-between}
.tile .nm small{color:var(--dim);font-weight:400}
video{width:100%;border-radius:10px;background:#000}
table{width:100%;border-collapse:collapse;font-size:14px}
th,td{text-align:left;padding:8px 6px;border-bottom:1px solid var(--line)}
th{color:var(--dim);font-weight:600}
.clip{display:block;background:var(--panel2);border-radius:8px;padding:10px 12px;margin:6px 0;text-decoration:none;color:var(--txt);cursor:pointer}
.clip small{color:var(--dim)}
.ev{padding:9px 10px;border-bottom:1px solid var(--line);font-size:14px}
.ev small{color:var(--dim)}
.note{color:var(--dim);font-size:13px;line-height:1.5}
.err{color:var(--red)} .ok{color:var(--lime)}
.layoutbtns button{padding:6px 12px}
</style></head><body>
<header>
  <h1><b>DAYBREAK</b> <span>NVR</span> <small id="ver" style="color:var(--dim);font-weight:400"></small></h1>
  <span id="status">Connecting…</span>
  <nav>
    <button data-tab="live" class="on">Live</button>
    <button data-tab="play">Playback</button>
    <button data-tab="dev" id="navDev">Devices</button>
    <button data-tab="events">Motion events</button>
    <button data-tab="settings" id="navSettings">Settings</button>
    <button data-tab="users" id="navUsers" style="display:none">Users</button>
    <button id="logoutBtn" class="ghost" title="Sign out">Sign out</button>
  </nav>
</header>
<main>
  <section id="tab-live" class="on">
    <div class="row" style="margin-bottom:12px">
      <span class="layoutbtns">
        <button data-layout="1">1</button>
        <button data-layout="4" class="ghost on">4</button>
        <button data-layout="9" class="ghost">9</button>
        <button data-layout="16" class="ghost">16</button>
      </span>
      <span id="liveCount" class="note"></span>
      <button id="liveBack" class="ghost" style="display:none">← All cameras</button>
    </div>
    <div id="wall" class="grid g4"></div>
    <p id="liveEmpty" class="note" style="display:none">No cameras yet — add one in the Devices tab.</p>
  </section>

  <section id="tab-play">
    <div class="card">
      <div class="row">
        <select id="playCam" style="max-width:260px"><option value="">All cameras</option></select>
        <input id="playDate" type="date" style="max-width:190px">
        <button id="playGo">Show clips</button>
      </div>
      <div id="player" style="margin-top:12px;display:none">
        <video id="vid" controls autoplay playsinline></video>
        <p id="vidName" class="note"></p>
      </div>
      <div id="clipList" style="margin-top:10px"></div>
    </div>
  </section>

  <section id="tab-dev">
    <div class="card">
      <h3>Cameras</h3>
      <table><thead><tr><th>Name</th><th>IP / RTSP</th><th>Mode</th><th>Recording</th><th></th></tr></thead>
      <tbody id="camRows"></tbody></table>
      <p class="note">Up to 12 cameras. Deleting a camera keeps its recordings on disk.</p>
    </div>
    <div class="card">
      <h3 id="formTitle">Add camera</h3>
      <p class="note">Tapo cameras: in the Tapo app open the camera → Settings → Advanced Settings → Camera Account and create a username and password. Enter them here with the camera's IP — Daybreak NVR builds the Tapo RTSP addresses for you (stream1 records, stream2 previews). For other brands, paste full RTSP URLs in the override boxes instead.</p>
      <input type="hidden" id="fId">
      <label>Name</label><input id="fName" placeholder="Front Door">
      <label>Camera IP</label><input id="fIP" placeholder="192.168.1.50">
      <label>Camera Account user</label><input id="fUser">
      <label>Camera Account password</label><input id="fPass" type="password">
      <label>Main RTSP URL override (optional)</label><input id="fMain" placeholder="optional — overrides the Tapo address">
      <label>Sub RTSP URL override (optional)</label><input id="fSub" placeholder="optional — overrides the Tapo address">
      <div class="row" style="margin-top:10px">
        <label style="margin:0"><input type="checkbox" id="fEnabled" checked> Enabled</label>
        <label style="margin:0"><input type="checkbox" id="fRecord" checked> Record this camera</label>
      </div>
      <p class="note">Record mode, motion sensitivity, detection zones and human detection are set on the Settings → Motion &amp; Recording page.</p>
      <div class="row" style="margin-top:14px">
        <button id="fTest" class="ghost">Test connection</button>
        <button id="fSave">Save camera</button>
        <button id="fCancel" class="ghost">Clear form</button>
        <span id="fMsg"></span>
      </div>
    </div>
  </section>

  <section id="tab-events">
    <div class="card">
      <div class="row">
        <select id="evCam" style="max-width:260px"><option value="">All cameras</option></select>
        <button id="evGo">Refresh</button>
      </div>
      <div id="evList" style="margin-top:8px"></div>
      <p class="note">Motion events come from the frame-difference detector on each camera's sub-stream. Person events come from the on-device AI detector. Recording in motion mode keeps the chunks around each event (the pre/post-event buffer, set on the Settings → Motion &amp; Recording page) and deletes the rest.</p>
    </div>
  </section>

  <section id="tab-settings">
    <div class="row" style="margin-bottom:12px">
      <button id="subGeneral" class="ghost">General</button>
      <button id="subMotion" class="ghost">Motion &amp; Recording</button>
      <button id="subStorage" class="ghost">Storage</button>
    </div>
    <div id="subpage-general">
      <div class="card"><h3>General</h3>
        <label>Live wall layout</label>
        <select id="sLayout" style="max-width:260px">
          <option value="1">1 camera</option>
          <option value="4">4 cameras (2×2)</option>
          <option value="9">9 cameras (3×3)</option>
          <option value="16">16 cameras (4×4)</option>
        </select>
        <label>Live preview frame rate</label><input id="sFPS" type="number" min="1" max="30">
        <p class="note">These apply to the whole app. Phone and remote viewing keep running with their current settings — they have no toggles in settings any more.</p>
      </div>
    </div>
    <div id="subpage-motion" style="display:none">
      <div class="card"><h3>Motion &amp; Recording</h3>
        <label>Camera</label><select id="mCam" style="max-width:260px"></select>
        <label>Record mode</label>
        <select id="mMode" style="max-width:260px"><option value="continuous">Continuous</option><option value="motion">Motion only</option><option value="person">Person only</option><option value="off">Do not record</option></select>
        <label>Motion sensitivity <span id="mSensVal"></span></label>
        <input id="mSens" type="range" min="0" max="100" value="50" style="width:100%">
        <p class="note">Higher means a smaller change triggers recording. 25 ≈ the old Low, 50 ≈ Medium, 75 ≈ High.</p>
        <label>Detection zone</label>
        <div id="mZoneWrap" style="position:relative;max-width:480px;aspect-ratio:16/9;display:none">
          <img id="mZoneImg" alt="Camera picture" style="position:absolute;inset:0;width:100%;height:100%;object-fit:cover;display:block">
          <div id="mZone" style="position:absolute;inset:0;display:grid;grid-template-columns:repeat(16,1fr);grid-template-rows:repeat(9,1fr);gap:1px"></div>
        </div>
        <div id="mZonePlain" style="display:grid;grid-template-columns:repeat(16,1fr);gap:2px;max-width:480px"></div>
        <div class="row" style="margin-top:8px">
          <button id="mZoneAll" class="ghost">Enable whole frame</button>
          <button id="mZoneClear" class="ghost">Clear all</button>
          <button id="mZoneRefresh" class="ghost">Refresh image</button>
          <span id="mZoneCount" class="note"></span>
        </div>
        <p class="note" id="mZoneNote">Tap a cell on the camera picture to turn it on or off — for example, turn off the cells covering a road or a tree that sways. With the whole frame on, everything counts.</p>
        <label>Keep before each event (seconds)</label><input id="mPre" type="number" min="0" max="120">
        <label>Keep after each event (seconds)</label><input id="mPost" type="number" min="0" max="120">
        <p class="note">When motion (or a person) is detected, the recording keeps this many seconds before the event starts and after it ends.</p>
        <label><input type="checkbox" id="mAI"> Human detection (on-device AI)</label>
        <label>Confidence threshold <span id="mAIVal"></span></label>
        <input id="mAIConf" type="range" min="0" max="100" value="50" style="width:100%">
        <p class="note">Human detection runs entirely on this PC (YOLOv8n, bundled with the app). No cloud, no account, no API key. It samples the camera's preview frames about once a second while something is moving, and about every 10 seconds otherwise. A person seen inside the detection zone starts/keeps recording in Motion mode and appears in the events list with a tracking ID. In Person-only mode it is the recording trigger: only a person starts a recording, so human detection is on for that camera.</p>
      </div>
    </div>
    <div id="subpage-storage" style="display:none">
      <div class="card"><h3>Storage &amp; recording</h3>
        <label>Recordings folder</label><input id="sDir">
        <label>Clip length — continuous mode (minutes)</label><input id="sSeg" type="number" min="1" max="120">
        <label>Keep recordings (days)</label><input id="sDays" type="number" min="1" max="365">
        <label>Max total size (GB, 0 = no cap)</label><input id="sGB" type="number" min="0">
        <p class="note">Motion and Person-only mode record one clip per event, directly — the recording starts when motion (or a person) is detected and stops after the keep-after buffer ends, so each event is a single clip in the playback list. Continuous mode records in segments of the length above. Retention (days / size cap) deletes the oldest clips first.</p>
      </div>
    </div>
    <button id="sSave">Save settings</button>
    <span id="sMsg" style="margin-left:10px"></span>
  </section>

  <section id="tab-users">
    <div class="card">
      <h3>Users</h3>
      <p class="note">Admins can do everything, including changing settings and deleting clips. Viewers can watch live, play back and download clips — nothing else. The built-in <b>admin</b> user can never be deleted; changing its password here replaces the built-in password.</p>
      <table><thead><tr><th>Username</th><th>Role</th><th></th></tr></thead>
      <tbody id="userRows"></tbody></table>
    </div>
    <div class="card">
      <h3 id="userFormTitle">Add user</h3>
      <label>Username</label><input id="uName" placeholder="viewer1">
      <label>Password (at least 4 characters; leave blank to keep the current password when changing a role)</label><input id="uPass" type="password">
      <label>Role</label>
      <select id="uRole" style="max-width:170px"><option value="viewer">Viewer</option><option value="admin">Admin</option></select>
      <div class="row" style="margin-top:14px">
        <button id="uSave">Save user</button>
        <button id="uCancel" class="ghost">Clear form</button>
        <span id="uMsg"></span>
      </div>
    </div>
  </section>
</main>
<script>
'use strict';
var $ = function(id){ return document.getElementById(id); };
function api(path, method, body){
  var o = {method: method || 'GET', headers: {'Content-Type': 'application/json'}};
  if (body) o.body = JSON.stringify(body);
  return fetch(path, o).then(function(r){
    if (r.status === 401) { window.location.href = '/ui/login'; throw new Error('Signed out — please sign in again.'); }
    return r.json().then(function(j){
      if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
      return j;
    });
  });
}
function esc(s){ return String(s == null ? '' : s).replace(/[&<>"']/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
function fmtBytes(b){ if (b < 1024) return b + ' B'; var u = ['KB','MB','GB','TB'], i = -1; do { b /= 1024; i++; } while (b >= 1024 && i < u.length - 1); return b.toFixed(1) + ' ' + u[i]; }
function fmtTime(t){ var d = new Date(t); return d.toLocaleString(undefined, {month:'short', day:'numeric', hour:'numeric', minute:'2-digit'}); }

var cameras = [];
var layout = 4, singleCam = null;
var myRole = '';

function applyRole(){
  var isAdmin = myRole === 'admin';
  $('navDev').style.display = isAdmin ? '' : 'none';
  $('navSettings').style.display = isAdmin ? '' : 'none';
  $('navUsers').style.display = isAdmin ? '' : 'none';
  if (!isAdmin && (document.querySelector('nav button.on') || {}).dataset) {
    var cur = document.querySelector('nav button.on');
    if (cur && (cur.dataset.tab === 'dev' || cur.dataset.tab === 'settings' || cur.dataset.tab === 'users')) showTab('live');
  }
}

function showTab(name){
  document.querySelectorAll('nav button').forEach(function(b){ b.classList.toggle('on', b.dataset.tab === name); });
  document.querySelectorAll('main section').forEach(function(s){ s.classList.toggle('on', s.id === 'tab-' + name); });
  if (name === 'play') loadClips();
  if (name === 'dev') loadCameras();
  if (name === 'events') loadEvents();
  if (name === 'settings') loadSettings();
  if (name === 'users') loadUsers();
}
document.querySelectorAll('nav button').forEach(function(b){
  b.addEventListener('click', function(){ showTab(b.dataset.tab); });
});

function refreshStatus(){
  api('/api/status').then(function(st){
    $('ver').textContent = 'v' + st.version;
    $('status').textContent = 'Recording: ' + st.recording + ' of ' + st.cameras + ' cameras · Stored: ' + fmtBytes(st.stored_bytes) + ' in ' + st.stored_files + ' clips' + (st.remote ? ' · Remote: ' + st.remote : '');
  }).catch(function(e){ $('status').textContent = 'Status unavailable: ' + e.message; });
}

function renderWall(){
  var wall = $('wall');
  wall.className = 'grid g' + (singleCam ? 1 : layout);
  wall.innerHTML = '';
  var list = singleCam ? cameras.filter(function(c){ return c.id === singleCam; }) : cameras.filter(function(c){ return c.enabled; });
  $('liveEmpty').style.display = list.length ? 'none' : 'block';
  $('liveCount').textContent = list.length ? list.length + ' camera' + (list.length > 1 ? 's' : '') : '';
  $('liveBack').style.display = singleCam ? '' : 'none';
  list.forEach(function(c){
    var d = document.createElement('div');
    d.className = 'tile';
    d.innerHTML = '<img src="/mjpeg?id=' + encodeURIComponent(c.id) + '" alt=""><div class="nm"><span>' + esc(c.name) + '</span><small>' + (c.record ? (c.record_mode === 'motion' ? 'motion' : (c.record_mode === 'person' ? 'person' : (c.record_mode === 'off' ? 'live' : 'rec'))) : 'preview') + '</small></div>';
    d.addEventListener('click', function(){
      if (singleCam) { singleCam = null; }
      else { singleCam = c.id; }
      renderWall();
    });
    wall.appendChild(d);
  });
}
$('liveBack').addEventListener('click', function(){ singleCam = null; renderWall(); });
document.querySelectorAll('.layoutbtns button').forEach(function(b){
  b.addEventListener('click', function(){
    layout = parseInt(b.dataset.layout, 10);
    document.querySelectorAll('.layoutbtns button').forEach(function(x){ x.classList.toggle('on', x === b); x.classList.toggle('ghost', x !== b); });
    singleCam = null; renderWall();
  });
});

function loadCameras(){
  return api('/api/cameras').then(function(list){
    cameras = list;
    renderWall();
    var rows = $('camRows');
    rows.innerHTML = '';
    list.forEach(function(c){
      var tr = document.createElement('tr');
      var detail = c.ip || c.main_url || '';
      tr.innerHTML = '<td>' + esc(c.name) + '</td><td>' + esc(detail) + '</td><td>' + (c.record_mode === 'motion' ? 'Motion only' : (c.record_mode === 'person' ? 'Person only' : (c.record_mode === 'off' ? 'Do not record' : 'Continuous'))) + '</td><td>' + (c.enabled ? (c.record ? 'Yes' : 'Preview only') : 'Disabled') + '</td>';
      var td = document.createElement('td');
      var eb = document.createElement('button'); eb.className = 'ghost'; eb.textContent = 'Edit';
      eb.addEventListener('click', function(){ fillForm(c); showTab('dev'); });
      var db = document.createElement('button'); db.className = 'danger'; db.textContent = 'Delete';
      db.addEventListener('click', function(){
        if (!confirm('Delete camera "' + c.name + '"? Its recordings stay on disk.')) return;
        api('/api/cameras?id=' + encodeURIComponent(c.id), 'DELETE').then(loadCameras).catch(function(e){ alert(e.message); });
      });
      td.appendChild(eb); td.appendChild(document.createTextNode(' ')); td.appendChild(db);
      tr.appendChild(td); rows.appendChild(tr);
    });
    var opts = '<option value="">All cameras</option>';
    list.forEach(function(c){ opts += '<option value="' + esc(c.id) + '">' + esc(c.name) + '</option>'; });
    $('playCam').innerHTML = opts;
    $('evCam').innerHTML = opts;
  }).catch(function(e){ $('status').textContent = 'Could not load cameras: ' + e.message; });
}

var curMotion = {record_mode: 'continuous', sensitivity: 50, zone_grid: '', human_detect: false, human_confidence: 50};

function fillForm(c){
  $('formTitle').textContent = c.id ? 'Edit camera — ' + c.name : 'Add camera';
  $('fId').value = c.id || '';
  $('fName').value = c.name || '';
  $('fIP').value = c.ip || '';
  $('fUser').value = c.username || '';
  $('fPass').value = c.password || '';
  $('fMain').value = c.main_url || '';
  $('fSub').value = c.sub_url || '';
  $('fEnabled').checked = c.enabled !== false;
  $('fRecord').checked = c.record !== false;
  // Motion settings are edited on the Settings → Motion page;
  // the form keeps the camera's current values so saving here
  // never resets them. A brand-new camera defaults to Motion
  // only (like the desktop editor).
  curMotion = {record_mode: c.record_mode || (c.id ? 'continuous' : 'motion'),
    sensitivity: c.sensitivity == null ? 50 : c.sensitivity,
    zone_grid: c.zone_grid || '',
    human_detect: !!c.human_detect,
    human_confidence: c.human_confidence == null ? 50 : c.human_confidence};
  $('fMsg').textContent = '';
}
function readForm(){
  return {id: $('fId').value, name: $('fName').value, ip: $('fIP').value,
    username: $('fUser').value, password: $('fPass').value,
    main_url: $('fMain').value, sub_url: $('fSub').value,
    enabled: $('fEnabled').checked, record: $('fRecord').checked,
    record_mode: curMotion.record_mode, sensitivity: curMotion.sensitivity,
    zone_grid: curMotion.zone_grid, human_detect: curMotion.human_detect,
    human_confidence: curMotion.human_confidence};
}
$('fSave').addEventListener('click', function(){
  $('fMsg').textContent = 'Saving…';
  api('/api/cameras', 'POST', readForm()).then(function(){
    $('fMsg').textContent = '✓ Saved.';
    $('fMsg').className = 'ok';
    fillForm({}); loadCameras(); refreshStatus();
  }).catch(function(e){ $('fMsg').textContent = e.message; $('fMsg').className = 'err'; });
});
$('fTest').addEventListener('click', function(){
  $('fMsg').textContent = 'Connecting…'; $('fMsg').className = '';
  api('/api/test', 'POST', readForm()).then(function(r){
    $('fMsg').textContent = (r.ok ? '✓ ' : '✗ ') + r.message;
    $('fMsg').className = r.ok ? 'ok' : 'err';
  }).catch(function(e){ $('fMsg').textContent = e.message; $('fMsg').className = 'err'; });
});
$('fCancel').addEventListener('click', function(){ fillForm({}); });

function loadClips(){
  var q = '';
  if ($('playCam').value) q += 'camera=' + encodeURIComponent($('playCam').value);
  if ($('playDate').value) q += (q ? '&' : '') + 'date=' + encodeURIComponent($('playDate').value);
  api('/api/clips' + (q ? '?' + q : '')).then(function(list){
    var box = $('clipList');
    box.innerHTML = '';
    if (!list.length) { box.innerHTML = '<p class="note">No clips match.</p>'; return; }
    list.slice(0, 200).forEach(function(c){
      var wrap = document.createElement('div');
      wrap.className = 'clip';
      wrap.style.display = 'flex';
      wrap.style.justifyContent = 'space-between';
      wrap.style.alignItems = 'center';
      wrap.style.gap = '10px';
      var a = document.createElement('a');
      a.className = 'clip'; a.href = 'javascript:void(0)'; a.style.background = 'none'; a.style.margin = '0'; a.style.padding = '0'; a.style.flex = '1';
      a.innerHTML = '▶ ' + esc(c.camera_name) + ' <small>· ' + fmtTime(c.start) + ' · ' + fmtBytes(c.size) + '</small>';
      a.addEventListener('click', function(){
        $('player').style.display = '';
        $('vid').src = '/clip?f=' + encodeURIComponent(c.token);
        $('vidName').textContent = c.camera_name + ' · ' + fmtTime(c.start);
        $('vid').play().catch(function(){});
      });
      wrap.appendChild(a);
      if (myRole === 'admin') {
        var del = document.createElement('button');
        del.className = 'danger'; del.textContent = 'Delete';
        del.addEventListener('click', function(){
          if (!confirm('Delete this clip? The file is removed from the PC.')) return;
          api('/api/clips?token=' + encodeURIComponent(c.token), 'DELETE').then(loadClips).catch(function(e){ alert(e.message); });
        });
        wrap.appendChild(del);
      }
      box.appendChild(wrap);
    });
  }).catch(function(e){ $('clipList').innerHTML = '<p class="err">' + esc(e.message) + '</p>'; });
}
$('playGo').addEventListener('click', loadClips);

function loadEvents(){
  var q = 'limit=200';
  if ($('evCam').value) q += '&camera=' + encodeURIComponent($('evCam').value);
  api('/api/events?' + q).then(function(list){
    var box = $('evList');
    box.innerHTML = '';
    if (!list.length) { box.innerHTML = '<p class="note">No motion events yet.</p>'; return; }
    list.forEach(function(e){
      var d = document.createElement('div');
      d.className = 'ev';
      var end = e.open ? 'still going' : new Date(e.end).toLocaleTimeString();
      var label = '<b>' + esc(e.camera_name) + '</b> · ' + fmtTime(e.start) + ' – ' + esc(end) + (e.open ? ' <small>(open)</small>' : '');
      if (e.kind === 'person') {
        label = '👤 <b>' + esc(e.camera_name) + '</b> · Person #' + (e.track_id || '?') + ' · ' + fmtTime(e.start) + ' – ' + esc(end) + (e.confidence ? ' <small>(' + Math.round(e.confidence * 100) + '% sure)</small>' : '');
      }
      d.innerHTML = label;
      box.appendChild(d);
    });
  }).catch(function(e){ $('evList').innerHTML = '<p class="err">' + esc(e.message) + '</p>'; });
}
$('evGo').addEventListener('click', loadEvents);

// Settings sub-pages: General / Motion & Recording / Storage.
function showSubPage(name){
  ['general', 'motion', 'storage'].forEach(function(p){
    $('subpage-' + p).style.display = p === name ? '' : 'none';
  });
  if (name === 'motion') loadMotionPage();
}
$('subGeneral').addEventListener('click', function(){ showSubPage('general'); });
$('subMotion').addEventListener('click', function(){ showSubPage('motion'); });
$('subStorage').addEventListener('click', function(){ showSubPage('storage'); });

// Detection zone grid: 16x9 toggle cells overlaid on the camera
// picture (v1.6). The picture comes from /api/camera-snapshot; if
// it cannot be fetched, the plain button grid below it is shown
// instead, so the page is never blocked by a missing picture.
// Both editors share one zoneGrid string and paint together.
var zoneGrid = '';
function toggleZoneCell(idx){
  var g = zoneGrid.split('');
  g[idx] = g[idx] === '1' ? '0' : '1';
  zoneGrid = g.join('');
  paintZoneGrid();
}
function buildZoneGrid(){
  var overlay = $('mZone');
  var plain = $('mZonePlain');
  overlay.innerHTML = '';
  plain.innerHTML = '';
  for (var i = 0; i < 144; i++) {
    (function(idx){
      // Overlay cell: a semi-transparent tint over the picture —
      // teal when on, dark when off; the 1px grid gaps let the
      // picture show through as the grid lines.
      var cell = document.createElement('div');
      cell.style.cursor = 'pointer';
      cell.addEventListener('click', function(){ toggleZoneCell(idx); });
      overlay.appendChild(cell);
      // Plain-grid fallback cell (camera picture unavailable).
      var b = document.createElement('button');
      b.className = 'ghost';
      b.style.padding = '2px 0';
      b.style.minWidth = '0';
      b.addEventListener('click', function(){ toggleZoneCell(idx); });
      plain.appendChild(b);
    })(i);
  }
  paintZoneGrid();
}
function paintZoneGrid(){
  if (zoneGrid.length !== 144) zoneGrid = new Array(145).join('1');
  var on = 0;
  var overlayCells = $('mZone').children;
  var plainCells = $('mZonePlain').children;
  for (var i = 0; i < 144; i++) {
    var onCell = zoneGrid[i] === '1';
    if (onCell) on++;
    overlayCells[i].style.background = onCell ? 'rgba(22,133,115,0.35)' : 'rgba(0,0,0,0.55)';
    plainCells[i].textContent = onCell ? '■' : '·';
    plainCells[i].style.opacity = onCell ? '1' : '0.45';
  }
  $('mZoneCount').textContent = on + ' of 144 cells enabled (' + Math.round(100 * on / 144) + '% of the frame).';
}
$('mZoneAll').addEventListener('click', function(){ zoneGrid = new Array(145).join('1'); paintZoneGrid(); });
$('mZoneClear').addEventListener('click', function(){ zoneGrid = new Array(145).join('0'); paintZoneGrid(); });
// loadZoneImage fetches a fresh still for the zone picker. On
// success the overlaid picker is shown; on failure the plain
// grid stays up with a note.
function loadZoneImage(id){
  var img = $('mZoneImg');
  $('mZoneNote').textContent = 'Getting a fresh picture from the camera…';
  img.onload = function(){
    $('mZoneWrap').style.display = 'block';
    $('mZonePlain').style.display = 'none';
    $('mZoneNote').textContent = 'Tap a cell on the picture to turn it on or off — for example, turn off the cells covering a road or a tree that sways. With the whole frame on, everything counts.';
  };
  img.onerror = function(){
    $('mZoneWrap').style.display = 'none';
    $('mZonePlain').style.display = 'grid';
    $('mZoneNote').textContent = 'Camera picture unavailable — using the plain grid below. Check the camera is reachable, then tap Refresh image.';
  };
  img.src = '/api/camera-snapshot?id=' + encodeURIComponent(id) + '&t=' + Date.now();
}
$('mZoneRefresh').addEventListener('click', function(){ if ($('mCam').value) loadZoneImage($('mCam').value); });
// Person-only recording needs human detection: choosing it
// turns the checkbox on (saving forces it on too).
function refreshAIModeNote(){
  if ($('mMode').value === 'person') {
    $('mAI').checked = true;
  }
}
$('mMode').addEventListener('change', refreshAIModeNote);
$('mSens').addEventListener('input', function(){ $('mSensVal').textContent = '— ' + $('mSens').value; });
$('mAIConf').addEventListener('input', function(){ $('mAIVal').textContent = '— ' + $('mAIConf').value + '%'; });

function loadMotionPage(){
  var sel = $('mCam');
  sel.innerHTML = '';
  cameras.forEach(function(c){
    var o = document.createElement('option');
    o.value = c.id; o.textContent = c.name;
    sel.appendChild(o);
  });
  if (cameras.length) loadMotionCamera(cameras[0].id);
}
function loadMotionCamera(id){
  var c = null;
  cameras.forEach(function(x){ if (x.id === id) c = x; });
  if (!c) return;
  $('mCam').value = id;
  $('mMode').value = c.record_mode || 'continuous';
  $('mSens').value = c.sensitivity == null ? 50 : c.sensitivity;
  $('mSensVal').textContent = '— ' + $('mSens').value;
  zoneGrid = c.zone_grid && c.zone_grid.length === 144 ? c.zone_grid : new Array(145).join('1');
  paintZoneGrid();
  $('mAI').checked = !!c.human_detect;
  refreshAIModeNote();
  loadZoneImage(id);
  $('mAIConf').value = c.human_confidence == null ? 50 : c.human_confidence;
  $('mAIVal').textContent = '— ' + $('mAIConf').value + '%';
  curMotion = {record_mode: c.record_mode || 'continuous',
    sensitivity: c.sensitivity == null ? 50 : c.sensitivity,
    zone_grid: c.zone_grid || '',
    human_detect: !!c.human_detect,
    human_confidence: c.human_confidence == null ? 50 : c.human_confidence};
}
$('mCam').addEventListener('change', function(){ loadMotionCamera($('mCam').value); });
buildZoneGrid();

function loadSettings(){
  api('/api/settings').then(function(s){
    $('sDir').value = s.recordings_dir || '';
    $('sSeg').value = s.segment_minutes;
    $('sDays').value = s.retention_days;
    $('sGB').value = s.max_total_gb;
    $('sFPS').value = s.preview_fps;
    $('sLayout').value = String(s.grid_layout || 4);
    $('mPre').value = s.pre_buffer_seconds == null ? 15 : s.pre_buffer_seconds;
    $('mPost').value = s.post_buffer_seconds == null ? 20 : s.post_buffer_seconds;
  }).catch(function(e){ $('sMsg').textContent = e.message; $('sMsg').className = 'err'; });
}
$('sSave').addEventListener('click', function(){
  // The Motion page edits the selected camera, not the settings
  // object — save it first, then the app-wide settings.
  var saveCam = Promise.resolve();
  if (cameras.length && $('mCam').value) {
    var c = null;
    cameras.forEach(function(x){ if (x.id === $('mCam').value) c = x; });
    if (c) {
      var dto = {id: c.id, name: c.name, ip: c.ip, username: c.username, password: c.password,
        main_url: c.main_url, sub_url: c.sub_url, enabled: c.enabled, record: c.record,
        record_mode: $('mMode').value, sensitivity: parseInt($('mSens').value, 10),
        zone_grid: zoneGrid, human_detect: $('mAI').checked,
        human_confidence: parseInt($('mAIConf').value, 10)};
      saveCam = api('/api/cameras', 'POST', dto);
    }
  }
  saveCam.then(function(){
    var dto = {recordings_dir: $('sDir').value, segment_minutes: parseInt($('sSeg').value, 10),
      retention_days: parseInt($('sDays').value, 10), max_total_gb: parseInt($('sGB').value, 10),
      preview_fps: parseInt($('sFPS').value, 10), grid_layout: parseInt($('sLayout').value, 10),
      pre_buffer_seconds: parseInt($('mPre').value, 10), post_buffer_seconds: parseInt($('mPost').value, 10)};
    $('sMsg').textContent = 'Saving…'; $('sMsg').className = '';
    return api('/api/settings', 'POST', dto);
  }).then(function(){
    $('sMsg').textContent = '✓ Saved — settings applied.'; $('sMsg').className = 'ok';
    loadCameras(); refreshStatus();
  }).catch(function(e){ $('sMsg').textContent = e.message; $('sMsg').className = 'err'; });
});

function loadUsers(){
  api('/api/users').then(function(list){
    var rows = $('userRows');
    rows.innerHTML = '';
    list.forEach(function(u){
      var tr = document.createElement('tr');
      tr.innerHTML = '<td>' + esc(u.username) + (u.built_in ? ' <small>(built-in)</small>' : '') + '</td><td>' + esc(u.role) + '</td>';
      var td = document.createElement('td');
      var eb = document.createElement('button'); eb.className = 'ghost'; eb.textContent = 'Edit';
      eb.addEventListener('click', function(){
        $('userFormTitle').textContent = 'Edit user — ' + u.username;
        $('uName').value = u.username;
        $('uPass').value = '';
        $('uRole').value = u.role;
        $('uMsg').textContent = '';
      });
      td.appendChild(eb);
      if (!u.built_in) {
        var db = document.createElement('button'); db.className = 'danger'; db.textContent = 'Delete';
        db.addEventListener('click', function(){
          if (!confirm('Delete user "' + u.username + '"?')) return;
          api('/api/users?username=' + encodeURIComponent(u.username), 'DELETE').then(loadUsers).catch(function(e){ alert(e.message); });
        });
        td.appendChild(document.createTextNode(' ')); td.appendChild(db);
      }
      tr.appendChild(td); rows.appendChild(tr);
    });
  }).catch(function(e){ alert(e.message); });
}
$('uSave').addEventListener('click', function(){
  var dto = {username: $('uName').value, password: $('uPass').value, role: $('uRole').value};
  $('uMsg').textContent = 'Saving…'; $('uMsg').className = '';
  api('/api/users', 'POST', dto).then(function(){
    $('uMsg').textContent = '✓ Saved.'; $('uMsg').className = 'ok';
    $('uName').value = ''; $('uPass').value = '';
    $('userFormTitle').textContent = 'Add user';
    loadUsers();
  }).catch(function(e){ $('uMsg').textContent = e.message; $('uMsg').className = 'err'; });
});
$('uCancel').addEventListener('click', function(){
  $('uName').value = ''; $('uPass').value = ''; $('uRole').value = 'viewer';
  $('userFormTitle').textContent = 'Add user'; $('uMsg').textContent = '';
});

$('logoutBtn').addEventListener('click', function(){
  fetch('/api/logout', {method: 'POST'}).then(function(){ window.location.href = '/ui/login'; });
});

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(function(){ /* LAN http: install still works via browser menu */ });
}
api('/api/me').then(function(me){
  myRole = me.role || '';
  applyRole();
}).catch(function(){ /* api() already redirects on 401 */ });
loadCameras();
refreshStatus();
setInterval(refreshStatus, 10000);
</script>
</body></html>`

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}
