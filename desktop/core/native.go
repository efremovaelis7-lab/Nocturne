package core

// nativeScript is added to the page by the desktop builds. It connects the
// page with the window: downloads into "Загрузки" (WebKit has no download
// bar), a safe quit that waits for the last save, and a heartbeat on Windows.
//
// The editor code lives inside a closure, so the script talks to it only
// through standard browser events: a synthetic "visibilitychange" makes the
// editor send a pending save immediately (its own handler does that when a
// window is hidden), and a synthetic "beforeunload" tells whether anything
// is still unsaved (the editor cancels that event while a save is pending).
const nativeScript = `(function(){
'use strict';
var N = window.__NOCTURNE_NATIVE || {};
var root = document.documentElement;
root.classList.add('nocturne-desktop');
if (N.platform) root.classList.add('nocturne-' + N.platform);
window.__nocturneMessages = [];
function key(){ return (window.__NOCTURNE_BOOT && window.__NOCTURNE_BOOT.key) || ''; }
function post(msg){
  try { window.webkit.messageHandlers.nocturne.postMessage(msg); return true; }
  catch (e) { window.__nocturneMessages.push(msg); return false; }
}
function sleep(ms){ return new Promise(function(r){ setTimeout(r, ms); }); }
var toastTimer = null;
function say(text){
  var t = document.querySelector('#toast'); if (!t) return;
  var show = function(){
    t.textContent = text; t.classList.add('open');
    clearTimeout(toastTimer); toastTimer = setTimeout(function(){ t.classList.remove('open'); }, 3800);
  };
  if (t.classList.contains('open')) setTimeout(show, 3300); else show();
}
function triggerFlush(){
  try {
    Object.defineProperty(document, 'visibilityState', {configurable: true, get: function(){ return 'hidden'; }});
    document.dispatchEvent(new Event('visibilitychange'));
  } catch (e) {
  } finally {
    try { delete document.visibilityState; } catch (e) {}
  }
}
function isDirty(){
  var ev = new Event('beforeunload', {cancelable: true});
  window.dispatchEvent(ev);
  return ev.defaultPrevented;
}
function saveLabel(){ var x = document.querySelector('#saveStatus'); return x ? (x.textContent || '') : ''; }
var pendingDownloads = 0;

// Resolves true when everything is saved, or false with a reason.
async function settle(maxMs){
  var t0 = Date.now();
  while (pendingDownloads > 0 && Date.now() - t0 < 120000) await sleep(100);
  triggerFlush();
  var t1 = Date.now(), lastFlush = t1;
  while (isDirty()) {
    if (/НЕ СОХРАНЕНО|ОШИБКА/.test(saveLabel())) return {ok: false, message: saveLabel().replace(/^[^A-Za-zА-Яа-яЁё]+/, '')};
    if (Date.now() - t1 > maxMs) return {ok: false, message: 'Сохранение не успело завершиться'};
    if (Date.now() - lastFlush > 1000) { triggerFlush(); lastFlush = Date.now(); }
    await sleep(50);
  }
  return {ok: true};
}
window.__nocturneSettle = settle;

// Called by the Mac window before the app quits.
window.__nocturneBeforeQuit = function(){
  settle(20000).then(function(r){
    post(r.ok ? {type: 'ready-to-quit'} : {type: 'quit-blocked', message: r.message});
  });
  return true;
};

if (N.platform === 'mac' || N.platform === 'windows') {
  // Full archives can be gigabytes (music). The server writes them straight
  // into Downloads; the page only receives an empty ZIP as a placeholder,
  // which is recognised below and turned into a rename of the real file.
  var MARKER = new Uint8Array([80,75,5,6,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]);
  var nativeArchive = null, nativeArchiveSize = 0;
  var nativeFetch = window.fetch.bind(window);
  var fmtSize = function(n){
    if (n >= 1e9) return (n / 1e9).toFixed(1).replace('.', ',') + ' ГБ';
    if (n >= 1e6) return Math.round(n / 1e6) + ' МБ';
    return Math.max(1, Math.round(n / 1024)) + ' КБ';
  };
  window.fetch = function(input, init){
    var url = typeof input === 'string' ? input : (input && input.url) || '';
    var method = String((init && init.method) || (input && input.method) || 'GET').toUpperCase();
    if (method === 'GET' && /^(https?:\/\/[^/]+)?\/api\/backup(\?|$)/.test(url)) {
      return (async function(){
        var r = await nativeFetch('/api/native/backup', {method: 'POST', headers: {'X-Nocturne-Key': key()}});
        if (!r.ok) return r;
        var x = await r.json();
        nativeArchive = {path: x.path, name: x.name, size: x.size, at: Date.now()};
        nativeArchiveSize = x.size || 0;
        return new Response(new Blob([MARKER], {type: 'application/zip'}), {status: 200, headers: {'Content-Type': 'application/zip'}});
      })();
    }
    return nativeFetch(input, init);
  };
  var nativeConfirm = window.confirm.bind(window);
  window.confirm = function(message){
    var text = String(message == null ? '' : message);
    if (nativeArchiveSize) text = text.replace(/Защитная копия \(\d+ КБ\)/, 'Защитная копия (' + fmtSize(nativeArchiveSize) + ')');
    return nativeConfirm(text);
  };
  var isMarker = async function(blob){
    if (!blob || blob.size !== MARKER.length) return false;
    var b = new Uint8Array(await blob.arrayBuffer());
    for (var i = 0; i < b.length; i++) if (b[i] !== MARKER[i]) return false;
    return true;
  };
  var saveDownload = async function(href, name){
    pendingDownloads++;
    var blob = null;
    try {
      blob = await (await nativeFetch(href)).blob();
      if (nativeArchive && Date.now() - nativeArchive.at < 30 * 60000 && await isMarker(blob)) {
        var a = nativeArchive; nativeArchive = null;
        var rr = await nativeFetch('/api/native/rename?from=' + encodeURIComponent(a.path) + '&name=' + encodeURIComponent(name || a.name), {method: 'POST', headers: {'X-Nocturne-Key': key()}});
        var xx = await rr.json().catch(function(){ return {}; });
        var finalName = rr.ok ? xx.name : a.name, finalPath = rr.ok ? xx.path : a.path;
        post({type: 'downloaded', path: finalPath, name: finalName});
        say('Архив «' + finalName + '» (' + fmtSize(a.size || 0) + ') сохранён в папку «Загрузки»');
        return;
      }
      var r = await nativeFetch('/api/native/download?name=' + encodeURIComponent(name || 'NOCTURNE'), {
        method: 'POST', headers: {'X-Nocturne-Key': key(), 'Content-Type': 'application/octet-stream'}, body: blob
      });
      var x = await r.json().catch(function(){ return {}; });
      if (!r.ok) throw new Error(x.error || ('HTTP ' + r.status));
      post({type: 'downloaded', path: x.path, name: x.name});
      say('Файл «' + x.name + '» сохранён в папку «Загрузки»');
    } catch (e) {
      // RC3.8.11: when the app does not answer (the copy is needed most then),
      // Edge saves the file itself into "Загрузки".
      if (blob && N.platform === 'windows' && !(blob.size === MARKER.length)) {
        try {
          var u = URL.createObjectURL(blob), a2 = document.createElement('a');
          a2.href = u; a2.download = name || 'NOCTURNE';
          nativeClick.call(a2);
          setTimeout(function(){ URL.revokeObjectURL(u); }, 60000);
          say('Файл «' + (name || 'NOCTURNE') + '» сохраняется браузером в «Загрузки»');
          return;
        } catch (e2) {}
      }
      say('Не удалось сохранить файл: ' + (e && e.message || e));
    } finally { pendingDownloads--; }
  };
  var isFileHref = function(a){ return !!a && a.hasAttribute('download') && /^(blob:|data:)/i.test(a.href || ''); };
  var nativeClick = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function(){
    if (isFileHref(this)) { saveDownload(this.href, this.getAttribute('download')); return; }
    return nativeClick.apply(this, arguments);
  };
  document.addEventListener('click', function(e){
    var a = e.target && e.target.closest ? e.target.closest('a[download]') : null;
    if (isFileHref(a)) { e.preventDefault(); e.stopPropagation(); saveDownload(a.href, a.getAttribute('download')); }
  }, true);
  {
  // WebKit leaves ⌘B / ⌘I / ⌘U to a Format menu; in Edge Ctrl+U would open
  // the page source. Both run through the editor's own "Ж / К / Ч" buttons,
  // so the editor saves the change as usual (matched by key position, so it
  // works with the Russian layout too).
  var formatKeys = {KeyB: 'bold', KeyI: 'italic', KeyU: 'underline'};
  window.addEventListener('keydown', function(e){
    var mod = N.platform === 'mac' ? (e.metaKey && !e.ctrlKey) : (e.ctrlKey && !e.metaKey);
    if (!mod || e.altKey || e.shiftKey) return;
    var cmd = formatKeys[e.code];
    var t = e.target;
    if (!cmd || !t || !t.isContentEditable) return;
    e.preventDefault();
    var all = Array.prototype.slice.call(document.querySelectorAll('[data-rich-cmd="' + cmd + '"]'));
    var btn = all.filter(function(b){ return b.offsetParent !== null; })[0] || all[0];
    if (btn) {
      btn.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, view: window}));
      btn.click();
    } else {
      document.execCommand(cmd, false, null);
    }
  }, true);
  if (N.platform === 'mac') post({type: 'hello'});
  }
}

// Audio: WebKit's media engine may refuse to fetch music from the local
// server by itself, so music files are loaded by the page and handed to the
// player from memory. Diagnostics go to music-debug.log.
function nlog(text){ try { fetch('/api/native/log', {method: 'POST', headers: {'X-Nocturne-Key': key()}, body: String(text)}).catch(function(){}); } catch (e) {} }
if (N.platform === 'mac') {
  var srcDesc = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, 'src');
  var nativePlay = HTMLMediaElement.prototype.play;
  var musicRe = /\/media\/music\/([^?#]+)/;
  Object.defineProperty(HTMLMediaElement.prototype, 'src', {
    configurable: true, enumerable: true,
    get: function(){ return srcDesc.get.call(this); },
    set: function(v){
      var m = musicRe.exec(String(v || ''));
      if (!m) { this.__nocturnePending = null; return srcDesc.set.call(this, v); }
      var el = this, token = {};
      el.__nocturneToken = token;
      el.__nocturnePending = (async function(){
        var r = await fetch('/media/music/' + m[1], {cache: 'no-store'});
        if (!r.ok) throw new Error(r.status === 404 ? 'Файл отсутствует в папке Музыка' : 'HTTP ' + r.status);
        var b = await r.blob();
        if (el.__nocturneToken !== token) return false;
        if (el.__nocturneUrl) URL.revokeObjectURL(el.__nocturneUrl);
        el.__nocturneUrl = URL.createObjectURL(new Blob([b], {type: r.headers.get('Content-Type') || 'audio/mpeg'}));
        srcDesc.set.call(el, el.__nocturneUrl);
        return true;
      })();
      el.__nocturnePending.catch(function(err){ nlog('music load failed ' + m[1] + ': ' + (err && err.message)); });
    }
  });
  HTMLMediaElement.prototype.play = function(){
    var el = this, p = el.__nocturnePending;
    if (p) {
      el.__nocturnePending = null;
      return p.then(function(ok){ return ok === false ? undefined : nativePlay.call(el); });
    }
    return nativePlay.call(el);
  };
}
window.addEventListener('error', function(e){
  var el = e.target;
  if (!el || el.tagName !== 'AUDIO') return;
  nlog('audio error code=' + (el.error && el.error.code) + ' msg=' + (el.error && el.error.message || '') + ' src=' + String(el.currentSrc || '').slice(0, 80));
}, true);
setTimeout(function(){ nlog('native ready ' + (N.version || '')); }, 4000);

async function windowsQuit(){
  var r = await settle(20000);
  if (!r.ok) { say('Не удалось безопасно закрыть NOCTURNE: ' + r.message); return; }
  try { await fetch('/api/quit', {method: 'POST', headers: {'X-Nocturne-Key': key()}}); } catch (e) {}
  window.close();
  setTimeout(function(){
    document.body.innerHTML = '<div style="min-height:85vh;display:grid;place-items:center;background:#16120f;color:#f0dcc0;font:22px Georgia;text-align:center;padding:30px"><div>☾<h1 style="font:36px Georgia;margin:18px 0">NOCTURNE</h1><p>Все изменения сохранены.</p><p style="font-size:15px;opacity:.7">Можно закрыть это окно.</p></div></div>';
  }, 500);
}

// The "Завершить NOCTURNE" button closes the window instead of stopping
// only the server.
window.addEventListener('click', function(e){
  var b = e.target && e.target.closest ? e.target.closest('[data-act="quit"]') : null;
  if (!b) return;
  e.preventDefault(); e.stopImmediatePropagation();
  if (N.platform === 'mac') post({type: 'quit'}); else windowsQuit();
}, true);

if (N.platform === 'windows') {
  var beat = function(){
    var k = key(); if (!k) return;
    fetch('/api/native/heartbeat', {method: 'POST', headers: {'X-Nocturne-Key': k}, keepalive: true}).catch(function(){});
  };
  setInterval(beat, 5000);
  setTimeout(beat, 800);
  document.addEventListener('visibilitychange', beat);
  // The window is closing (or reloading): tell the app, so it can stop even
  // when Edge keeps running in the background.
  window.addEventListener('pagehide', function(){
    var k = key(); if (!k) return;
    fetch('/api/native/bye', {method: 'POST', headers: {'X-Nocturne-Key': k}, keepalive: true}).catch(function(){});
  });
}
})();
`
