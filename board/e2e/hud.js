// roadie HUD: a sci-fi skin over the e2e test page, for demos and
// recordings (test_e2e.py --hud). It is visual only: it listens to the same
// events page.html reports, cancels none of them, takes no pointer events
// and reports nothing, so the checks run exactly as they do without it.
// The run's progress comes from the harness at /status.
(() => {
'use strict';

const CSS = `
body.hud { background: #02060c; }
body.hud > header, body.hud #log, body.hud #banner, body.hud #cursor,
body.hud #touch, body.hud #typed { display: none !important; }
body.hud #pad { background: none; }
#hud {
  --c: #5ff3ff; --c2: #ffb347; --ok: #59ffb0; --bad: #ff4d5e; --dim: #1d6a7a;
  --ry: 50%;
  position: fixed; inset: 0; pointer-events: none; overflow: hidden; z-index: 10;
  color: var(--c); -webkit-user-select: none; user-select: none;
  font-family: "Avenir Next Condensed", "Avenir Next", "Roboto Condensed",
               "DejaVu Sans Condensed", "Arial Narrow", sans-serif;
}
#hud * { box-sizing: border-box; }
#hud .mono { font-family: ui-monospace, "SF Mono", Menlo, "DejaVu Sans Mono", monospace; }
.hud-bg { position: absolute; inset: 0; background:
  radial-gradient(ellipse at 50% var(--ry), rgba(40,170,210,.24), transparent 58%),
  radial-gradient(ellipse at 50% 115%, rgba(255,140,40,.12), transparent 50%),
  #02060c; }
.hud-grid { position: absolute; inset: -40px;
  background-image: linear-gradient(rgba(95,243,255,.08) 1px, transparent 1px),
                    linear-gradient(90deg, rgba(95,243,255,.08) 1px, transparent 1px);
  background-size: 28px 28px; animation: hud-drift 14s linear infinite;
  -webkit-mask-image: radial-gradient(ellipse at 50% 50%, #000 25%, transparent 78%);
          mask-image: radial-gradient(ellipse at 50% 50%, #000 25%, transparent 78%); }
@keyframes hud-drift { to { transform: translate(28px, 28px); } }
.hud-scan { position: absolute; inset: 0;
  background: repeating-linear-gradient(0deg, rgba(0,0,0,.22) 0 1px, transparent 1px 3px); }
.hud-sweep { position: absolute; left: 0; right: 0; height: 140px; top: -140px;
  background: linear-gradient(180deg, transparent, rgba(95,243,255,.07), transparent);
  animation: hud-sweep 6s linear infinite; }
@keyframes hud-sweep { to { top: 100%; } }
#hud canvas, #hud svg.hud-track { position: absolute; inset: 0; width: 100%; height: 100%; }

.hud-corner { position: absolute; width: 26px; height: 26px; border: 2px solid var(--c); opacity: .55; }
.hud-corner.tl { top: 6px; left: 6px; border-right: 0; border-bottom: 0; }
.hud-corner.tr { top: 6px; right: 6px; border-left: 0; border-bottom: 0; }
.hud-corner.bl { bottom: 6px; left: 6px; border-right: 0; border-top: 0; }
.hud-corner.br { bottom: 6px; right: 6px; border-left: 0; border-top: 0; }

.hud-top { position: absolute; top: 14px; left: 18px; right: 18px;
  display: flex; justify-content: space-between; align-items: flex-start; }
.hud-brand b { display: block; font-size: 24px; font-weight: 700; letter-spacing: .42em;
  text-shadow: 0 0 14px var(--c); }
.hud-brand span { display: block; font-size: 9px; letter-spacing: .4em; opacity: .7; margin-top: 2px; }
.hud-clock { font-size: 11px; text-align: right; line-height: 1.35; opacity: .9; }
.hud-clock i { display: block; font-style: normal; color: var(--c2); }
.hud-sub { position: absolute; top: 62px; left: 18px; right: 18px; font-size: 9px;
  letter-spacing: .18em; opacity: .85; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.hud-sub .dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%;
  background: var(--ok); box-shadow: 0 0 8px var(--ok); margin-right: 6px;
  animation: hud-pulse 1.4s ease-in-out infinite; vertical-align: 0; }
@keyframes hud-pulse { 50% { opacity: .25; } }
.hud-tele { position: absolute; right: 18px; top: 80px; font-size: 9px; line-height: 1.45;
  text-align: right; opacity: .75; }
.hud-tele div { animation: hud-in .25s ease-out; }

.hud-reactor { position: absolute; left: 50%; top: var(--ry); width: 210px; height: 210px;
  transform: translate(-50%, -50%); transition: top .9s cubic-bezier(.3,.8,.2,1); }
.hud-reactor svg { width: 100%; height: 100%; overflow: visible; }
.hud-reactor circle { fill: none; transform-box: fill-box; transform-origin: center; }
.r-halo { stroke: rgba(95,243,255,.12); stroke-width: 14; }
.r-ticks { stroke: var(--c); stroke-width: 6; stroke-dasharray: 1.2 6.2; opacity: .55;
  animation: hud-spin 40s linear infinite; }
.r-track { stroke: rgba(95,243,255,.14); stroke-width: 5; }
.r-prog { stroke: var(--c); stroke-width: 5; stroke-linecap: round;
  filter: drop-shadow(0 0 6px var(--c)); transform: rotate(-90deg);
  transition: stroke-dashoffset .8s cubic-bezier(.3,.8,.2,1), stroke .4s; }
.r-dash { stroke: var(--c2); stroke-width: 2; stroke-dasharray: 26 14; opacity: .8;
  animation: hud-spin 9s linear infinite reverse; }
.r-seg { stroke: var(--c); stroke-width: 9; stroke-dasharray: 60 34.2; opacity: .35;
  animation: hud-spin 5s linear infinite; }
.r-core { stroke: var(--c); stroke-width: 1.5; opacity: .9; }
.hud-reactor.busy .r-seg { opacity: .7; animation-duration: 1.6s; }
@keyframes hud-spin { to { transform: rotate(360deg); } }
.hud-core { position: absolute; inset: 0; display: flex; flex-direction: column;
  align-items: center; justify-content: center; }
.hud-core b { font-size: 34px; font-weight: 700; letter-spacing: .06em;
  text-shadow: 0 0 16px var(--c); }
.hud-core span { font-size: 9px; letter-spacing: .38em; opacity: .75; margin-top: 2px; }
#hud.win .r-prog { stroke: var(--ok); filter: drop-shadow(0 0 10px var(--ok)); }
#hud.win .hud-core b { color: var(--ok); text-shadow: 0 0 18px var(--ok); }
#hud.fail .r-prog { stroke: var(--bad); filter: drop-shadow(0 0 10px var(--bad)); }
#hud.fail .hud-core b { color: var(--bad); text-shadow: 0 0 18px var(--bad); }

.hud-phase { position: absolute; left: 0; right: 0; top: calc(var(--ry) - 214px);
  text-align: center; transition: top .9s cubic-bezier(.3,.8,.2,1); }
.hud-phase small { display: block; font-size: 10px; letter-spacing: .5em; color: var(--c2); }
.hud-phase b { display: block; font-size: 22px; font-weight: 700; letter-spacing: .26em;
  text-shadow: 0 0 14px var(--c); margin-top: 2px; }
.hud-phase.go { animation: hud-phase .7s cubic-bezier(.2,.9,.2,1); }
@keyframes hud-phase { from { clip-path: inset(0 50% 0 50%); opacity: 0; }
                       to { clip-path: inset(0 0 0 0); opacity: 1; } }

.hud-checks { position: absolute; left: 50%; top: calc(var(--ry) + 150px);
  width: min(90vw, 420px); transform: translateX(-50%); margin: 0; padding: 0;
  list-style: none; transition: top .9s cubic-bezier(.3,.8,.2,1); }
.hud-checks li { display: flex; justify-content: space-between; align-items: center;
  font-size: 12px; letter-spacing: .24em; padding: 4px 10px; margin: 2px 0;
  border-left: 2px solid var(--dim); opacity: .45; transition: all .35s;
  background: linear-gradient(90deg, rgba(95,243,255,.09), transparent 80%); }
.hud-checks li .st { font-size: 10px; letter-spacing: .14em; }
.hud-checks li.active { opacity: 1; color: var(--c2); border-color: var(--c2);
  background: linear-gradient(90deg, rgba(255,179,71,.16), transparent 80%); }
.hud-checks li.active .st { animation: hud-pulse .8s steps(2) infinite; }
.hud-checks li.ok { opacity: 1; border-color: var(--ok); }
.hud-checks li.ok .st { color: var(--ok); text-shadow: 0 0 8px var(--ok); }
.hud-checks li.bad { opacity: 1; color: var(--bad); border-color: var(--bad);
  background: linear-gradient(90deg, rgba(255,77,94,.16), transparent 80%); }

.hud-notes { position: absolute; left: 0; right: 0; top: calc(var(--ry) - 160px);
  display: flex; justify-content: center; gap: 10px; opacity: 0;
  transition: opacity .4s, top .9s cubic-bezier(.3,.8,.2,1); }
.hud-notes.show { opacity: 1; }
.hud-notes div { width: 48px; padding: 4px 0 3px; text-align: center; font-size: 14px;
  letter-spacing: .08em; border: 1px solid rgba(95,243,255,.35); background: rgba(4,24,34,.6);
  transition: all .15s; }
.hud-notes div small { display: block; font-size: 8px; opacity: .7; letter-spacing: .05em; }
.hud-notes div.on { color: var(--c2); border-color: var(--c2); transform: scale(1.15);
  box-shadow: 0 0 16px var(--c2); }
.hud-notes div.done { color: var(--ok); border-color: var(--ok); }
.hud-wave.sound { border-color: var(--c2); }
.hud-comms { position: absolute; left: 16px; right: 16px; bottom: 16px; min-height: 78px;
  padding: 8px 14px 10px; border: 1px solid rgba(95,243,255,.38);
  background: rgba(4,24,34,.6);
  clip-path: polygon(0 0, calc(100% - 16px) 0, 100% 16px, 100% 100%, 16px 100%, 0 calc(100% - 16px)); }
.hud-comms .lbl { display: flex; justify-content: space-between; font-size: 9px;
  letter-spacing: .36em; opacity: .75; }
.hud-typed { font-size: 19px; font-weight: 600; color: #eafdff; line-height: 1.3;
  text-shadow: 0 0 10px var(--c); word-break: break-all; min-height: 25px; margin-top: 5px; }
.hud-typed .w { white-space: nowrap; }
.hud-typed .ch { display: inline-block; white-space: pre; animation: hud-mat .4s ease-out; }
@keyframes hud-mat { from { transform: scale(2.4) translateY(-6px); opacity: 0;
                            color: var(--c2); filter: blur(3px); }
                     to { transform: none; opacity: 1; filter: none; } }
.hud-typed::after { content: "\\258C"; color: var(--c); animation: hud-pulse 1s steps(1) infinite; }

.hud-lock { position: absolute; width: 0; height: 0; transition: opacity .9s; }
.hud-lock.out { opacity: 0; }
.hud-lock i { position: absolute; left: 0; top: 0; border-radius: 50%; }
.hud-lock .a { width: 58px; height: 58px; border: 2px solid; border-color: var(--c2) transparent;
  animation: hud-close .55s cubic-bezier(.2,.9,.2,1) both, hud-rot 2.2s linear infinite; }
.hud-lock .b { width: 88px; height: 88px; border: 1px dashed var(--c);
  animation: hud-close2 .7s cubic-bezier(.2,.9,.2,1) both, hud-rot 6s linear infinite reverse; }
.hud-lock .p { width: 8px; height: 8px; background: var(--c2); box-shadow: 0 0 12px var(--c2);
  transform: translate(-50%, -50%); }
.hud-lock em { position: absolute; left: 38px; top: -46px; font-style: normal; font-size: 10px;
  letter-spacing: .12em; color: var(--c2); white-space: nowrap; border-bottom: 1px solid var(--c2);
  padding: 0 4px 2px; animation: hud-in .3s .35s both; }
@keyframes hud-close { from { width: 190px; height: 190px; opacity: 0; } }
@keyframes hud-close2 { from { width: 260px; height: 260px; opacity: 0; } }
@keyframes hud-rot { from { transform: translate(-50%, -50%) rotate(0); }
                     to { transform: translate(-50%, -50%) rotate(360deg); } }
.hud-hair { position: absolute; opacity: 0; animation: hud-hair 1.8s ease-out forwards; }
.hud-hair.h { left: 0; right: 0; height: 1px;
  background: linear-gradient(90deg, transparent, var(--c), transparent); }
.hud-hair.v { top: 0; bottom: 0; width: 1px;
  background: linear-gradient(180deg, transparent, var(--c), transparent); }
@keyframes hud-hair { 15% { opacity: .75; } 100% { opacity: 0; } }
.hud-wave { position: absolute; border-radius: 50%; border: 2px solid var(--c);
  transform: translate(-50%, -50%); animation: hud-wave .8s ease-out forwards; }
@keyframes hud-wave { from { width: 16px; height: 16px; opacity: 1; }
                      to { width: 240px; height: 240px; opacity: 0; border-width: 1px; } }
@keyframes hud-in { from { opacity: 0; transform: translateX(-6px); } }

.hud-boot { position: absolute; inset: 0; background: #02060c; display: flex;
  flex-direction: column; justify-content: center; padding: 0 26px; font-size: 12px;
  line-height: 1.9; transition: opacity .9s; }
.hud-boot.gone { opacity: 0; }
.hud-boot div { white-space: pre; overflow: hidden; text-overflow: ellipsis; animation: hud-in .2s ease-out; }
.hud-boot b { color: var(--ok); font-weight: 400; }
.hud-boot .big { font-size: 18px; letter-spacing: .3em; color: var(--c2); margin-top: 8px; }

.hud-final { position: absolute; left: 0; right: 0; top: calc(var(--ry) - 236px);
  text-align: center; opacity: 0; transition: opacity .6s; }
.hud-final.show { opacity: 1; animation: hud-phase .9s cubic-bezier(.2,.9,.2,1); }
.hud-final small { display: block; font-size: 10px; letter-spacing: .5em; color: var(--c2); }
.hud-final b { display: block; font-size: 25px; font-weight: 700; letter-spacing: .2em;
  margin: 3px 0; color: var(--ok); text-shadow: 0 0 18px var(--ok); }
.hud-final span { font-size: 11px; letter-spacing: .3em; opacity: .85; }
#hud.fail .hud-final b { color: var(--bad); text-shadow: 0 0 18px var(--bad); }
.hud-flash { position: absolute; inset: 0; background: radial-gradient(circle at 50% var(--ry),
  rgba(89,255,176,.35), transparent 60%); animation: hud-flash 1.4s ease-out forwards; }
#hud.fail .hud-flash { background: radial-gradient(circle at 50% var(--ry), rgba(255,77,94,.35), transparent 60%); }
@keyframes hud-flash { from { opacity: 1; } to { opacity: 0; } }
`;

const R_PROG = 78;
const CIRC = 2 * Math.PI * R_PROG;

const HTML = `
<div class="hud-bg"></div><div class="hud-grid"></div><div class="hud-scan"></div>
<div class="hud-sweep"></div>
<svg class="hud-track"><polyline id="hud-path" fill="none" stroke="rgba(95,243,255,.35)"
  stroke-width="1" stroke-dasharray="4 5"/></svg>
<canvas id="hud-trail"></canvas><canvas id="hud-mark"></canvas>
<i class="hud-corner tl"></i><i class="hud-corner tr"></i>
<i class="hud-corner bl"></i><i class="hud-corner br"></i>
<div class="hud-top">
  <div class="hud-brand"><b>ROADIE</b><span>REMOTE OPERATIONS</span></div>
  <div class="hud-clock mono"><span id="hud-clock">--:--:--.---</span><i id="hud-elapsed">T+00:00.0</i></div>
</div>
<div class="hud-sub mono"><span class="dot"></span><span id="hud-link">ESTABLISHING LINK</span></div>
<div class="hud-tele mono" id="hud-tele"></div>
<div class="hud-phase" id="hud-phase"><small>&nbsp;</small><b>&nbsp;</b></div>
<div class="hud-reactor" id="hud-reactor">
  <svg viewBox="-105 -105 210 210">
    <circle class="r-halo" r="98"/>
    <circle class="r-ticks" r="92"/>
    <circle class="r-track" r="${R_PROG}"/>
    <circle class="r-prog" id="hud-prog" r="${R_PROG}"
      stroke-dasharray="${CIRC}" stroke-dashoffset="${CIRC}"/>
    <circle class="r-dash" r="66"/>
    <circle class="r-seg" r="15"/>
    <circle class="r-core" r="44"/>
  </svg>
  <div class="hud-core"><b id="hud-count">--</b><span id="hud-core-lbl">STANDBY</span></div>
</div>
<div class="hud-final" id="hud-final"><small></small><b></b><span></span></div>
<ul class="hud-checks" id="hud-checks"></ul>
<div class="hud-notes mono" id="hud-notes"></div>
<div class="hud-comms"><div class="lbl"><span>KEYBOARD UPLINK</span><span id="hud-kcount" class="mono">000</span></div>
  <div class="hud-typed mono" id="hud-typed"></div></div>
<div class="hud-boot mono" id="hud-boot"></div>
`;

const style = document.createElement('style');
style.textContent = CSS;
document.head.append(style);
document.body.classList.add('hud');
const root = document.createElement('div');
root.id = 'hud';
root.setAttribute('aria-hidden', 'true');
root.innerHTML = HTML;
document.body.append(root);
const $ = (id) => document.getElementById(id);

// The run as the harness last described it.
let booted = false;
let state = {};
let phaseShown = null;
let planKey = '';
let finished = false;

// --- text effects ---------------------------------------------------------
const GLYPHS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789#%&*+<>/\\=';
function decode(el, text, ms = 450) {
  const t0 = performance.now();
  const step = (now) => {
    const k = Math.min(1, (now - t0) / ms);
    const n = Math.floor(text.length * k);
    let s = text.slice(0, n);
    for (let i = n; i < text.length; i++) {
      s += text[i] === ' ' ? ' ' : GLYPHS[(Math.random() * GLYPHS.length) | 0];
    }
    el.textContent = s;
    if (k < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

// --- clock ----------------------------------------------------------------
const born = performance.now();
const pad = (n, w = 2) => String(n).padStart(w, '0');
(function tick() {
  const d = new Date();
  $('hud-clock').textContent =
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
  const t = (performance.now() - born) / 1000;
  $('hud-elapsed').textContent = `T+${pad(Math.floor(t / 60))}:${pad(Math.floor(t % 60))}.${Math.floor(t * 10) % 10}`;
  requestAnimationFrame(tick);
})();

// --- telemetry ------------------------------------------------------------
function tele(text) {
  const el = $('hud-tele');
  const d = document.createElement('div');
  d.textContent = text;
  el.append(d);
  while (el.childNodes.length > 5) el.firstChild.remove();
}

// --- target locks ---------------------------------------------------------
const locks = [];
function lock(x, y) {
  const el = document.createElement('div');
  el.className = 'hud-lock';
  el.style.left = x + 'px';
  el.style.top = y + 'px';
  el.innerHTML = '<i class="b"></i><i class="a"></i><i class="p"></i><em class="mono"></em>';
  root.append(el);
  decode(el.querySelector('em'), `TARGET LOCK ${Math.round(x)},${Math.round(y)}`, 500);
  for (const dir of ['h', 'v']) {
    const h = document.createElement('div');
    h.className = 'hud-hair ' + dir;
    if (dir === 'h') h.style.top = y + 'px'; else h.style.left = x + 'px';
    root.append(h);
    setTimeout(() => h.remove(), 1900);
  }
  setTimeout(() => el.classList.add('out'), 2200);
  setTimeout(() => el.remove(), 3200);
  locks.push(`${x},${y}`);
  $('hud-path').setAttribute('points', locks.slice(-8).join(' '));
}
function wave(x, y) {
  const w = document.createElement('div');
  w.className = 'hud-wave';
  w.style.left = x + 'px';
  w.style.top = y + 'px';
  root.append(w);
  setTimeout(() => w.remove(), 900);
}

// --- trails ---------------------------------------------------------------
// Taps and drags leave a cyan trail that fades. The drag check traces the
// Vibium mark, which is drawn on its own canvas in the mark's red-to-orange,
// redrawn whole as one neon line, and kept for the finish.
const canvas = $('hud-trail');
const ctx = canvas.getContext('2d');
const markCanvas = $('hud-mark');
const mctx = markCanvas.getContext('2d');
const dpr = window.devicePixelRatio || 1;
function size() {
  for (const [c, x] of [[canvas, ctx], [markCanvas, mctx]]) {
    c.width = innerWidth * dpr;
    c.height = innerHeight * dpr;
    x.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  drawMark();
}
let last = null;
function trail(x, y) {
  ctx.globalCompositeOperation = 'source-over';
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  if (last) {
    ctx.shadowColor = '#5ff3ff';
    ctx.shadowBlur = 18;
    ctx.strokeStyle = 'rgba(160,250,255,.95)';
    ctx.lineWidth = 3.5;
    ctx.beginPath();
    ctx.moveTo(last.x, last.y);
    ctx.lineTo(x, y);
    ctx.stroke();
  }
  ctx.shadowBlur = 24;
  ctx.fillStyle = '#ffb347';
  ctx.beginPath();
  ctx.arc(x, y, 3, 0, Math.PI * 2);
  ctx.fill();
  last = {x, y};
}
(function fade() {
  ctx.globalCompositeOperation = 'destination-out';
  ctx.shadowBlur = 0;
  ctx.fillStyle = 'rgba(0,0,0,.012)';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  requestAnimationFrame(fade);
})();

const strokes = [];
let pen = null;  // the stroke being drawn, while the pointer is held
let markQueued = false;
function drawMark() {
  markQueued = false;
  mctx.clearRect(0, 0, innerWidth, innerHeight);
  if (!strokes.length) return;
  const g = mctx.createLinearGradient(innerWidth * 0.14, 0, innerWidth * 0.86, 0);
  g.addColorStop(0, '#e8343a');
  g.addColorStop(1, '#ff8000');
  mctx.lineCap = 'round';
  mctx.lineJoin = 'round';
  const path = () => {
    mctx.beginPath();
    for (const st of strokes) {
      st.forEach((p, i) => (i ? mctx.lineTo(p.x, p.y) : mctx.moveTo(p.x, p.y)));
    }
  };
  // A wide glow, the coloured line, then a hot core.
  for (const [width, blur, style] of [[9, 26, g], [4.5, 10, g], [1.4, 0, 'rgba(255,236,210,.9)']]) {
    mctx.shadowColor = '#ff5a1f';
    mctx.shadowBlur = blur;
    mctx.strokeStyle = style;
    mctx.lineWidth = width;
    path();
    mctx.stroke();
  }
  if (pen && pen.length) {
    const p = pen[pen.length - 1];
    mctx.shadowColor = '#ffd27a';
    mctx.shadowBlur = 28;
    mctx.fillStyle = '#fff4dc';
    mctx.beginPath();
    mctx.arc(p.x, p.y, 4.5, 0, Math.PI * 2);
    mctx.fill();
  }
}
function queueMark() {
  if (!markQueued) { markQueued = true; requestAnimationFrame(drawMark); }
}
size();
window.addEventListener('resize', size);

// One place every held point goes: the mark while it is being traced,
// otherwise the fading trail.
function ink(x, y, start) {
  if (state.phase === 'drag') {
    if (start || !pen) { pen = []; strokes.push(pen); }
    pen.push({x, y});
    queueMark();
  } else {
    if (start) last = null;
    trail(x, y);
  }
}
function lift() {
  last = null;
  if (pen) { pen = null; queueMark(); }
}

// --- input, observed (never cancelled) --------------------------------------
let mouseDown = false;
window.addEventListener('pointerdown', (e) => {
  if (state.phase !== 'drag') lock(e.clientX, e.clientY);
  tele(`PTR▼ ${e.pointerType.toUpperCase()} ${Math.round(e.clientX)},${Math.round(e.clientY)}`);
  if (e.pointerType !== 'touch') { mouseDown = true; ink(e.clientX, e.clientY, true); }
});
window.addEventListener('pointerup', (e) => {
  if (state.phase !== 'drag') wave(e.clientX, e.clientY);
  tele(`PTR▲ ${Math.round(e.clientX)},${Math.round(e.clientY)}`);
  mouseDown = false;
  lift();
});
window.addEventListener('pointermove', (e) => {
  if (e.pointerType !== 'touch' && mouseDown) ink(e.clientX, e.clientY, false);
});
window.addEventListener('touchstart', (e) => {
  const t = e.changedTouches[0];
  if (t) ink(t.clientX, t.clientY, true);
});
window.addEventListener('touchmove', (e) => {
  for (const t of e.changedTouches) ink(t.clientX, t.clientY, false);
});
window.addEventListener('touchend', lift);

// The soundcheck: page.html plays the motif and says when each note sounds.
window.addEventListener('roadie:motif', (e) => {
  const row = $('hud-notes');
  row.innerHTML = '';
  const notes = e.detail.notes;
  const boxes = notes.map((n) => {
    const d = document.createElement('div');
    d.textContent = n.name;
    const hz = document.createElement('small');
    hz.textContent = `${Math.round(n.hz)} HZ`;
    d.append(hz);
    row.append(d);
    return d;
  });
  row.classList.add('show');
  const core = $('hud-reactor').getBoundingClientRect();
  notes.forEach((n, i) => {
    setTimeout(() => {
      boxes[i].className = 'on';
      tele(`TONE ${n.name} ${Math.round(n.hz)} Hz`);
      for (const d of [0, 180]) {
        setTimeout(() => {
          wave(core.left + core.width / 2, core.top + core.height / 2);
          root.lastChild.classList.add('sound');
        }, d);
      }
    }, n.at * 1000);
    setTimeout(() => { boxes[i].className = 'done'; }, (n.at + n.secs) * 1000);
  });
  const last = notes[notes.length - 1];
  setTimeout(() => row.classList.remove('show'), (last.at + last.secs + 4) * 1000);
});

// Letters go in word groups that never break, with plain spaces between,
// so lines wrap between words.
let keys = 0;
window.addEventListener('keydown', (e) => {
  const out = $('hud-typed');
  const tail = out.lastChild;
  if (e.key === ' ') {
    out.append(' ');
  } else if (e.key.length === 1) {
    let word = tail && tail.nodeType === 1 ? tail : null;
    if (!word) {
      word = document.createElement('span');
      word.className = 'w';
      out.append(word);
    }
    const ch = document.createElement('span');
    ch.className = 'ch';
    ch.textContent = e.key;
    word.append(ch);
  } else if (e.key === 'Backspace' && tail) {
    if (tail.nodeType === 1 && tail.lastChild) tail.lastChild.remove();
    if (tail.nodeType !== 1 || !tail.firstChild) tail.remove();
  }
  keys++;
  $('hud-kcount').textContent = pad(keys, 3);
  tele(`KEY ${JSON.stringify(e.key)} ${e.code}`);
});

// --- the run, from the harness ----------------------------------------------

function boot(s) {
  if (booted) return;
  booted = true;
  const link = s.link || {};
  const rows = [
    ['ROADIE // REMOTE OPERATIONS', null],
    ['─'.repeat(30), null],
    ['HOST', link.host],
    ['HID BACKEND', link.backend],
    ['USB LINK', link.usb_state],
    ['TARGET', `${(link.target || 'device').toUpperCase()} · ${screen.width}×${screen.height} @${devicePixelRatio}X`],
    ['OPERATOR', 'NONE'],
    ['─'.repeat(30), null],
  ].filter(([, v]) => v !== undefined);
  const el = $('hud-boot');
  rows.forEach(([k, v], i) => setTimeout(() => {
    const d = document.createElement('div');
    if (v === null) d.textContent = k;
    else {
      d.textContent = (k + ' ').padEnd(14, '.') + ' ';
      const b = document.createElement('b');
      d.append(b);
      decode(b, String(v || '—').toUpperCase(), 380);
    }
    el.append(d);
  }, 120 + i * 260));
  const t = 120 + rows.length * 260 + 250;
  setTimeout(() => {
    const d = document.createElement('div');
    d.className = 'big';
    el.append(d);
    decode(d, 'ALL SYSTEMS ARMED', 500);
  }, t);
  setTimeout(() => el.classList.add('gone'), t + 1400);
  setTimeout(() => el.remove(), t + 2400);
}

function render(s) {
  state = s;
  const link = s.link || {};
  if (link.backend) {
    $('hud-link').textContent = ['LINK', link.usb_state, link.backend, link.target]
      .filter(Boolean).join(' · ').toUpperCase();
  }
  if (typeof s.center_y === 'number' && s.center_y > 120 && s.center_y < innerHeight - 120) {
    root.style.setProperty('--ry', s.center_y + 'px');
  }

  const plan = s.plan || [];
  const results = s.results || [];
  const key = JSON.stringify(plan);
  const list = $('hud-checks');
  if (key !== planKey) {
    planKey = key;
    list.innerHTML = '';
    for (const p of plan) {
      const li = document.createElement('li');
      li.dataset.id = p.id;
      li.innerHTML = '<span></span><span class="st mono">STANDBY</span>';
      li.firstChild.textContent = p.title;
      list.append(li);
    }
  }
  let total = 0, passed = 0, done = 0, failed = 0;
  for (const p of plan) {
    const all = results.filter((r) => r.phase === p.id);
    const bypassed = all.length > 0 && all.every((r) => r.skipped);
    const mine = all.filter((r) => !r.skipped);
    const ok = mine.filter((r) => r.passed).length;
    const bad = mine.length - ok;
    if (!bypassed) total += p.checks;
    passed += ok; failed += bad; done += mine.length;
    const li = list.querySelector(`li[data-id="${p.id}"]`);
    if (!li) continue;
    const st = li.lastChild;
    let cls = '', label = 'STANDBY';
    if (bypassed) { label = 'N/A'; }
    else if (bad) { cls = 'bad'; label = '✗ FAULT'; }
    else if (s.phase === p.id) { cls = 'active'; label = `ENGAGED ${ok}/${p.checks}`; }
    else if (mine.length >= p.checks) { cls = 'ok'; label = '✓ NOMINAL'; }
    if (li.className !== cls) li.className = cls;
    if (st.textContent !== label) st.textContent = label;
  }
  if (total) {
    $('hud-count').textContent = `${passed}/${total}`;
    $('hud-prog').setAttribute('stroke-dashoffset', CIRC * (1 - done / total));
  }
  $('hud-reactor').classList.toggle('busy', !!s.phase && !s.done);

  if (s.phase && s.phase !== phaseShown) {
    phaseShown = s.phase;
    const idx = plan.findIndex((p) => p.id === s.phase);
    const p = plan[idx];
    const el = $('hud-phase');
    el.classList.remove('go');
    void el.offsetWidth;
    el.classList.add('go');
    el.firstChild.textContent = `PHASE ${pad(idx + 1)} / ${pad(plan.length)}`;
    decode(el.lastChild, p ? p.title : s.phase.toUpperCase(), 500);
    $('hud-core-lbl').textContent = 'RUNNING';
  }

  if (s.done && !finished) {
    finished = true;
    const win = failed === 0 && done > 0;
    root.classList.add(win ? 'win' : 'fail');
    $('hud-phase').style.opacity = '0';
    $('hud-core-lbl').textContent = win ? 'NOMINAL' : 'FAULT';
    const f = $('hud-final');
    f.children[0].textContent = 'DIAGNOSTIC COMPLETE';
    decode(f.children[1], win ? 'ALL SYSTEMS NOMINAL' : 'FAULT DETECTED', 700);
    f.children[2].textContent =
      `${passed}/${total} CHECKS · ${failed} FAULT${failed === 1 ? '' : 'S'}`;
    f.classList.add('show');
    const flash = document.createElement('div');
    flash.className = 'hud-flash';
    root.append(flash);
    setTimeout(() => flash.remove(), 1500);
  }
}

let first = true;
async function poll() {
  try {
    const r = await fetch('/status', {cache: 'no-store'});
    if (r.ok) {
      const s = await r.json();
      if (first) { first = false; boot(s); }
      render(s);
    }
  } catch (e) { /* the harness has gone: keep the last picture */ }
  if (!finished) setTimeout(poll, 250);
}
poll();
setTimeout(() => boot(state), 1500); // no harness answer: boot anyway
})();
