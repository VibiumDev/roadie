// Roadie's extras for the shared viewer: what a phone on a Roadie adds
// to the shell, the same whether the page is Roadie's own or a station's
// relaying it. The host prepends window.__roadieDevice (platform, HID
// backend, USB devices) before this file.
(function () {
  var shell = window.__roadieShell;
  if (!shell) return;
  // A watching page changes nothing: none of the phone's buttons, no
  // rotate or lock, no capture settings, crop or resets.
  if (shell.watching) return;
  var dev = window.__roadieDevice || {};
  var plat = dev.platform;
  var $ = function (id) { return document.getElementById(id); };
  var hostFetch = shell.fetch;
  function row(labelText, html, title) {
    var r = document.createElement('div');
    r.className = 'qrow';
    if (title) r.title = title;
    var l = document.createElement('label');
    l.className = 'qlabel';
    l.textContent = labelText;
    r.appendChild(l);
    var span = document.createElement('span');
    span.style.cssText = 'display:contents;';
    span.innerHTML = html;
    r.appendChild(span);
    return r;
  }

  // --- The capture's own settings: quality (the MJPEG stream's JPEG),
  // frame rate and size. They are the host's: read from it, and sent to
  // it only when someone changes one here.
  var qualityRow = row('Quality', '<input id="qrange" type="range" min="30" max="95"><span id="qval" class="qval"></span>', 'JPEG quality of the MJPEG stream');
  qualityRow.id = 'qualityRow';
  qualityRow.style.marginTop = '0'; qualityRow.style.marginBottom = '8px';
  var fpsRow = row('FPS', '<select id="fpsSelect"><option value="auto">Auto</option><option value="10">10</option><option value="15">15</option><option value="20">20</option><option value="30">30</option></select>');
  fpsRow.style.marginTop = '0'; fpsRow.style.marginBottom = '8px';
  var sizeRow = row('Size', '<select id="resSelect"><option value="1920x1080">1080p</option><option value="1280x720">720p</option><option value="640x480">480p</option></select>');
  sizeRow.style.marginTop = '0';
  shell.addRow(qualityRow, 'top', true);
  shell.addRow(fpsRow, 'top');
  shell.addRow(sizeRow, 'top');
  var qrange = $('qrange'), qval = $('qval'), fpsSelect = $('fpsSelect'), resSelect = $('resSelect');
  shell.set({quality: function () { return parseInt(qrange.value, 10) || 0; }});
  function applyToUI(d) {
    if (d.quality !== undefined) { qrange.value = d.quality; qval.textContent = d.quality; }
    if (d.fps !== undefined) fpsSelect.value = d.fps_auto ? 'auto' : d.fps;
    if (d.width !== undefined && d.height !== undefined) resSelect.value = d.width + 'x' + d.height;
  }
  var settingsTimer = null;
  function sendSettings(obj) {
    clearTimeout(settingsTimer);
    settingsTimer = setTimeout(function () {
      hostFetch('/api/settings', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(obj)}).catch(function () {});
    }, 300);
  }
  (function loadSettings() {
    hostFetch('/api/settings').then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); }).then(applyToUI).catch(function () {
      // Not ours to change (no admin on this hold): the rows go.
      [qualityRow, fpsRow, sizeRow].forEach(function (r) { r.style.display = 'none'; r.hidden = true; });
    });
  })();
  qrange.oninput = function () { qval.textContent = qrange.value; sendSettings({quality: parseInt(qrange.value, 10)}); };
  fpsSelect.onchange = function () {
    if (fpsSelect.value === 'auto') sendSettings({fps_auto: true});
    else sendSettings({fps: parseInt(fpsSelect.value, 10), fps_auto: false});
  };
  resSelect.onchange = function () { var parts = resSelect.value.split('x'); sendSettings({width: parseInt(parts[0], 10), height: parseInt(parts[1], 10)}); };

  // --- The crop: auto, off, or the one saved for the way the device is
  // turned. Chosen and kept by the host, not in this browser.
  var cropRow = row('Crop', '<div class="seg"><button id="cropAutoBtn" title="Trim whatever is black at the edges">Auto</button><button id="cropOffBtn" title="Show the whole frame">Off</button><button id="cropSavedBtn" title="Use the crop saved for the way the device is turned">Saved</button></div>');
  var cropRow2 = document.createElement('div');
  cropRow2.className = 'qrow sub';
  cropRow2.innerHTML = '<button id="cropSaveBtn" title="Save the crop of what is on screen now, for the way the device is turned. Show something bright to its edges first, such as a white page." class="btn2">Save</button><button id="cropClearBtn" title="Forget the saved crops" class="btn2">Clear</button>';
  var cropStatus = document.createElement('div');
  cropStatus.id = 'cropStatus'; cropStatus.className = 'err'; cropStatus.hidden = true;
  cropStatus.classList.add('qnote');
  shell.addRow(cropRow, 'middle'); shell.addRow(cropRow2, 'middle'); shell.addRow(cropStatus, 'middle');
  var cropBtns = {auto: $('cropAutoBtn'), off: $('cropOffBtn'), saved: $('cropSavedBtn')};
  var cropSaveBtn = $('cropSaveBtn'), cropClearBtn = $('cropClearBtn');
  function showCrop(d) {
    for (var u in cropBtns) cropBtns[u].classList.toggle('on', d.use === u);
    var saved = [];
    ['portrait', 'landscape'].forEach(function (o) {
      var c = d.saved && d.saved[o];
      if (c) saved.push(o + ' ' + c.width + '×' + c.height + (d.mode === 'saved' && d.orientation === o ? ' (in use)' : ''));
    });
    var any = saved.length > 0;
    cropBtns.saved.disabled = cropClearBtn.disabled = !any;
    cropBtns.saved.style.opacity = cropClearBtn.style.opacity = any ? '' : '0.4';
    var text = any ? 'Saved: ' + saved.join(', ') + '.' : 'No crop saved.';
    if (d.use === 'saved' && d.mode !== 'saved') text += ' None for ' + (d.orientation || 'this way') + ': auto for now.';
    cropBtns.saved.title = 'Use the crop saved for the way the device is turned. ' + text;
  }
  var cropErrorTimer = null;
  function cropError(msg) {
    cropStatus.textContent = msg;
    cropStatus.hidden = false;
    clearTimeout(cropErrorTimer);
    cropErrorTimer = setTimeout(function () { cropStatus.hidden = true; }, 8000);
  }
  var cropAllowed = true;
  function cropRequest(method, body) {
    if (!cropAllowed) return;
    var o = {method: method};
    if (body) { o.headers = {'Content-Type': 'application/json'}; o.body = JSON.stringify(body); }
    hostFetch('/api/crop', o).then(function (r) {
      if (r.status === 403 && method === 'GET') { cropAllowed = false; [cropRow, cropRow2, cropStatus].forEach(function (x) { x.style.display = 'none'; }); return null; }
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    }).then(function (d) { if (d) { cropStatus.hidden = true; showCrop(d); } }).catch(function (e) { cropError(e.message); });
  }
  Object.keys(cropBtns).forEach(function (u) { cropBtns[u].onclick = function () { cropRequest('PUT', {use: u}); }; });
  cropSaveBtn.onclick = function () { cropRequest('POST'); };
  cropClearBtn.onclick = function () { cropRequest('DELETE'); };
  cropRequest('GET');
  shell.on('menu', function (open) { if (open) cropRequest('GET'); });

  // --- Resets: hold a button. Only with admin on the device.
  if (shell.admin()) {
    var gadget = dev.hidBackend === 'linux-gadget';
    var head = document.createElement('div');
    head.className = 'qrow divider';
    head.innerHTML = '<label class="qlabel">Resets</label><button id="resetHelpBtn" aria-controls="resetHelp" aria-expanded="false" title="What each reset does" class="qhelp">?</button>';
    var help = document.createElement('div');
    help.id = 'resetHelp'; help.className = 'hint'; help.hidden = true;
    help.classList.add('qnote');
    var videoReset = row('Video', '<button id="resetVideoBtn" class="danger"></button>');
    var hidReset = row(gadget ? 'USB' : 'HID', '<button id="resetHIDBtn" class="danger"></button>');
    var relayReset = row('Relay', '<button id="resetRelayBtn" class="danger"></button>');
    shell.addRow(head, 'bottom'); shell.addRow(help, 'bottom'); shell.addRow(videoReset, 'bottom'); shell.addRow(hidReset, 'bottom');
    if (!gadget) shell.addRow(relayReset, 'bottom');
    function setupResetBtn(id, url) {
      var btn = $(id);
      var fill = document.createElement('div');
      fill.style.cssText = 'position:absolute;left:0;top:0;bottom:0;width:0;background:rgba(255,80,80,0.4);transition:none;pointer-events:none;';
      btn.appendChild(fill);
      var label = document.createElement('span');
      label.style.position = 'relative';
      label.textContent = 'Reset';
      btn.appendChild(label);
      var raf = null, startTime = 0, HOLD_MS = 1500;
      function animateFill() {
        var elapsed = Date.now() - startTime;
        var pct = Math.min(0.2 + (elapsed / HOLD_MS) * 0.8, 1);
        fill.style.width = (pct * 100) + '%';
        if (pct >= 1) {
          cancelHold();
          btn.style.minWidth = btn.offsetWidth + 'px';
          label.innerHTML = '<svg style="animation:spin 1s linear infinite" xmlns="http://www.w3.org/2000/svg" width="14" height="14" fill="currentColor" viewBox="0 0 16 16"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36a.25.25 0 0 1 .192-.41m-11 2h3.932a.25.25 0 0 0 .192-.41L2.692 6.23a.25.25 0 0 0-.384 0L.342 8.59A.25.25 0 0 0 .534 9"/><path fill-rule="evenodd" d="M8 3c-1.552 0-2.94.707-3.857 1.818a.5.5 0 1 1-.771-.636A6.002 6.002 0 0 1 13.917 7H12.9A5 5 0 0 0 8 3M3.1 9a5.002 5.002 0 0 0 8.757 2.182.5.5 0 1 1 .771.636A6.002 6.002 0 0 1 2.083 9z"/></svg>';
          btn.disabled = true;
          var done = function () { setTimeout(function () { label.textContent = 'Reset'; btn.disabled = false; btn.style.minWidth = ''; }, 2000); };
          hostFetch(url, {method: 'POST'}).then(done, done);
        } else {
          raf = requestAnimationFrame(animateFill);
        }
      }
      function cancelHold() {
        if (raf) { cancelAnimationFrame(raf); raf = null; }
        fill.style.width = '0';
        startTime = 0;
      }
      btn.addEventListener('pointerdown', function (e) { e.preventDefault(); startTime = Date.now(); raf = requestAnimationFrame(animateFill); });
      btn.addEventListener('pointerup', cancelHold);
      btn.addEventListener('pointerleave', cancelHold);
    }
    setupResetBtn('resetVideoBtn', '/api/capture/reset');
    setupResetBtn('resetHIDBtn', '/api/usb/reset');
    if (!gadget) setupResetBtn('resetRelayBtn', '/api/relay/reset');
    var devs = dev.usbDevices || [];
    var named = devs.length < 2 ? devs.join('') : devs.slice(0, -1).join(', ') + ' and ' + devs[devs.length - 1];
    var seen = named ? 'the target sees its ' + named + ' unplugged and plugged back in' : 'the target sees it unplugged and plugged back in';
    var rows = [['Video', 'replugs the capture dongle, which renegotiates HDMI; the picture drops for a few seconds.']];
    if (gadget) rows.push(['USB', 'the phone sees Roadie’s ' + (named || 'USB devices') + ' unplugged and plugged back in. Ends a touch the phone is stuck on.']);
    else { rows.push(['HID', 'reboots the HID board; ' + seen + '.']); rows.push(['Relay', 'reboots the relay board on this machine; the target sees nothing.']); }
    rows.forEach(function (r) {
      var p = document.createElement('div');
      p.style.margin = '0 0 4px';
      var b = document.createElement('b');
      b.textContent = r[0];
      p.appendChild(b);
      p.appendChild(document.createTextNode(': ' + r[1]));
      help.appendChild(p);
    });
    var hold = document.createElement('div');
    hold.textContent = 'Hold a button to reset.';
    help.appendChild(hold);
    $('resetHIDBtn').title = gadget ? 'Hold: the phone sees Roadie’s ' + (named || 'USB devices') + ' unplugged and plugged back in' : 'Hold: reboots the HID board';
    var helpBtn = $('resetHelpBtn');
    helpBtn.onclick = function () { help.hidden = !help.hidden; helpBtn.setAttribute('aria-expanded', String(!help.hidden)); };
  }

  // --- The phone's own buttons, as keyboard shortcuts the phone already
  // takes from a hardware keyboard. Home is GUI+H on iOS (Cmd+H) and
  // Android (Meta+H); on a computer the same keys hide the app or start
  // dictation, so these are offered for phones only. Android also gets
  // Back (Escape), Recents (Alt+Tab) and volume (the keyboard volume
  // keys). iOS has no system-wide Back or Recents shortcut, and its
  // volume is fixed while Roadie's audio is its output.
  if (plat === 'ios' || plat === 'android') {
    var GUI = 227, ALT = 226;
    var SVG = function (paths) { return '<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16">' + paths + '</svg>'; };
    var homePaths = plat === 'ios' ? '<path d="M8 .8.6 7.3l.8.9.6-.5V15h4.5v-4h3v4H14V7.7l.6.5.8-.9z"/>' : '<path fill-rule="evenodd" d="M8 2.5a5.5 5.5 0 1 1 0 11 5.5 5.5 0 0 1 0-11m0 1.5a4 4 0 1 0 0 8 4 4 0 0 0 0-8"/>';
    var vol = function (extra) { return '<path d="M6.717 3.55A.5.5 0 0 1 7 4v8a.5.5 0 0 1-.812.39L3.825 10.5H1.5A.5.5 0 0 1 1 10V6a.5.5 0 0 1 .5-.5h2.325l2.363-1.89a.5.5 0 0 1 .529-.06"/>' + extra; };
    if (plat === 'android') {
      shell.addButton({id: 'volUpBtn', title: 'Volume up', svg: SVG(vol('<path d="M12 5.5a.5.5 0 0 1 .5.5v1.5H14a.5.5 0 0 1 0 1h-1.5V10a.5.5 0 0 1-1 0V8.5H10a.5.5 0 0 1 0-1h1.5V6a.5.5 0 0 1 .5-.5"/>')), onClick: function () { shell.keys([128]); }});
      shell.addButton({id: 'volDownBtn', title: 'Volume down', svg: SVG(vol('<path d="M10 7.5h4a.5.5 0 0 1 0 1h-4a.5.5 0 0 1 0-1"/>')), onClick: function () { shell.keys([129]); }});
      shell.addButton({id: 'backBtn', title: 'Back (Esc)', svg: SVG('<path d="M11.5 2.8v10.4a.5.5 0 0 1-.77.42L3.4 8.42a.5.5 0 0 1 0-.84l7.33-5.2a.5.5 0 0 1 .77.42"/>'), onClick: function () { shell.keys([41]); }});
    }
    shell.addButton({id: 'homeBtn', title: plat === 'ios' ? 'Home (⌘H)' : 'Home (Meta+H)', svg: SVG(homePaths), onClick: function () { shell.keys([GUI, 11]); }});
    if (plat === 'android') {
      shell.addButton({id: 'recentsBtn', title: 'Recent apps (Alt+Tab)', svg: SVG('<path fill-rule="evenodd" d="M3 3.5a.5.5 0 0 1 .5-.5h9a.5.5 0 0 1 .5.5v9a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5zm1.5 1v7h7v-7z"/>'), onClick: function () { shell.keys([ALT, 43]); }});
    }
    // Rotate: /api/rotate turns an iPhone through AssistiveTouch's menu,
    // in a few seconds; the shell holds input back meanwhile, told by
    // the host's health.
    if (plat === 'ios') {
      var rotBtn = shell.addButton({id: 'rotateBtn', title: 'Rotate', svg: SVG('<path fill-rule="evenodd" d="M8 3a5 5 0 1 0 4.546 2.914.5.5 0 0 1 .908-.417A6 6 0 1 1 8 2z"/><path d="M8 4.466V.534a.25.25 0 0 1 .41-.192l2.36 1.966c.12.1.12.284 0 .384L8.41 4.658A.25.25 0 0 1 8 4.466"/><rect x="5.9" y="5.1" width="4.2" height="5.8" rx="0.7" fill="none" stroke="currentColor" stroke-width="1.1"/>')});
      var contentW = 0, contentH = 0;
      var nextTurn = function () {
        var w = contentW || shell.img.naturalWidth, h = contentH || shell.img.naturalHeight;
        return w > h ? 'portrait' : 'landscape';
      };
      // The icon is the turn it makes: the phone turns left, so an arrow
      // counterclockwise round a wide screen to landscape, clockwise
      // round a tall one back.
      var updateRotateBtn = function () {
        var land = nextTurn() === 'landscape';
        var svg = rotBtn.querySelector('svg'), rect = svg.querySelector('rect');
        svg.style.transform = land ? 'scaleX(-1)' : '';
        rect.setAttribute('x', land ? 5.1 : 5.9); rect.setAttribute('y', land ? 5.9 : 5.1);
        rect.setAttribute('width', land ? 5.8 : 4.2); rect.setAttribute('height', land ? 4.2 : 5.8);
        if (!shell.isBusy()) rotBtn.title = land ? 'Rotate to landscape' : 'Rotate to portrait';
      };
      updateRotateBtn();
      shell.on('health', function (data) {
        if (data.content) { contentW = data.content.width; contentH = data.content.height; updateRotateBtn(); }
      });
      shell.on('busy', function (on) { rotBtn.classList.toggle('busy', on); if (!on) updateRotateBtn(); });
      rotBtn.onclick = function () {
        if (shell.isBusy()) return;
        var to = nextTurn();
        shell.busy(true, 'Rotating the screen…');
        rotBtn.title = 'Rotating to ' + to + '…';
        hostFetch('/api/rotate', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({to: to})})
          .then(function (r) { return r.ok ? '' : r.text(); }, function (e) { return e.message; })
          .then(function (err) {
            shell.busy(false);
            updateRotateBtn();
            if (err) shell.note(err.trim());
          });
      };
    }
    // Lock and wake, like the phone's side button: iOS locks on
    // Ctrl+Cmd+Q and wakes on any key, Shift being one that types
    // nothing; Android's power key does both. While the screen is dark
    // (no signal) the button wakes; otherwise it locks.
    var LOCK = '<path d="M8 1a3 3 0 0 0-3 3v2.5H4.5A1.5 1.5 0 0 0 3 8v5.5A1.5 1.5 0 0 0 4.5 15h7a1.5 1.5 0 0 0 1.5-1.5V8a1.5 1.5 0 0 0-1.5-1.5H11V4a3 3 0 0 0-3-3m-1.5 3a1.5 1.5 0 1 1 3 0v2.5h-3z"/>';
    var WAKE = '<path d="M8 4.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7"/><path d="M8 0a.6.6 0 0 1 .6.6v1.8a.6.6 0 0 1-1.2 0V.6A.6.6 0 0 1 8 0m0 13a.6.6 0 0 1 .6.6v1.8a.6.6 0 0 1-1.2 0v-1.8A.6.6 0 0 1 8 13m8-5a.6.6 0 0 1-.6.6h-1.8a.6.6 0 0 1 0-1.2h1.8A.6.6 0 0 1 16 8M3 8a.6.6 0 0 1-.6.6H.6a.6.6 0 0 1 0-1.2h1.8A.6.6 0 0 1 3 8m10.66-5.66a.6.6 0 0 1 0 .85l-1.27 1.27a.6.6 0 1 1-.85-.85l1.27-1.27a.6.6 0 0 1 .85 0m-9.2 9.2a.6.6 0 0 1 0 .85l-1.27 1.27a.6.6 0 1 1-.85-.85l1.27-1.27a.6.6 0 0 1 .85 0m9.2 2.12a.6.6 0 0 1-.85 0l-1.27-1.27a.6.6 0 1 1 .85-.85l1.27 1.27a.6.6 0 0 1 0 .85m-9.2-9.2a.6.6 0 0 1-.85 0L2.34 3.19a.6.6 0 1 1 .85-.85l1.27 1.27a.6.6 0 0 1 0 .85"/>';
    var lockBtn = shell.addButton({id: 'lockBtn', title: 'Lock', svg: SVG(LOCK)});
    var lockKeys = plat === 'ios' ? [224, GUI, 20] : [102];   // Ctrl+Cmd+Q; Power
    var wakeKeys = plat === 'ios' ? [225] : [102];            // Shift; Power
    var screenDark = false;
    var updateLockBtn = function () {
      lockBtn.title = screenDark ? (plat === 'ios' ? 'Wake (Shift)' : 'Wake (Power)') : (plat === 'ios' ? 'Lock (Ctrl+⌘Q)' : 'Lock (Power)');
      lockBtn.querySelector('svg').innerHTML = screenDark ? WAKE : LOCK;
    };
    updateLockBtn();
    lockBtn.onclick = function () { shell.keys(screenDark ? wakeKeys : lockKeys); };
    shell.on('health', function (data) {
      var dark = data.status === 'no_signal';
      if (dark !== screenDark) { screenDark = dark; updateLockBtn(); }
    });
  }
  // An iPhone scrolls only for a run of wheel notches close together.
  if (plat === 'ios') shell.set({wheelRuns: true});
})();
