// Roadie's WebRTC transport for the viewer: H.264 from the host's encoder
// over a peer connection, with the MJPEG feed as the fallback. The page
// loads this whenever the host can encode; it claims the picture before
// the page's own script starts the MJPEG stream when WebRTC is wanted, and
// the page's menu can switch either way at any time (window.__roadieRTC).
//
// The page maps pointer input against the #feed image's size and position,
// so #feed stays: see-through, sized like the video, on top of it. The
// video sits exactly underneath and follows it every frame: zoom is a
// transform on the image and alignment moves it, neither of which a
// ResizeObserver sees. Input itself still travels the page's WebSocket in
// this version.
(function () {
  if (!window.RTCPeerConnection) return;

  var img = document.getElementById('feed');
  var video = document.getElementById('rtcVideo');
  var viewer = document.getElementById('viewer');
  var statsEl = document.getElementById('rtcStats');

  var active = false;      // WebRTC is wanted
  var playing = false;     // a picture is being shown over WebRTC
  var fallen = false;      // it failed and the page shows MJPEG
  var showStats = false;
  var pc = null, ws = null, control = null;
  var attempts = 0;
  var firstFrameTimer = null;
  var lastStatus = null;
  var capBps = 0;          // the menu's bitrate cap; 0 is none
  var hid = null;          // the "hid" data channel: input, as /api/hid/ws takes it
  var audioTx = null;      // the audio line: the phone's sound in, the mic out
  var audioEl = document.getElementById('rtcAudio');
  var audioMuted = true;   // the page's mute, applied to the sound over WebRTC

  function log(msg) { if (window.console) console.log('webrtc: ' + msg); }

  function teardown() {
    clearTimeout(firstFrameTimer);
    if (ws) { var w = ws; ws = null; try { w.onclose = null; w.close(); } catch (e) {} }
    if (pc) { var p = pc; pc = null; try { p.close(); } catch (e) {} }
    control = null;
    hid = null;
    audioTx = null;
    if (audioEl) audioEl.srcObject = null;
  }

  // The page's speaker and mic follow the transport: told whenever the
  // picture starts or stops coming over WebRTC.
  function changed() {
    try { if (window.__roadieTransportChanged) window.__roadieTransportChanged(); } catch (e) {}
  }

  // Back to the page's MJPEG feed.
  function showMJPEG() {
    teardown();
    playing = false;
    changed();
    placedSize = '';
    placed = '';
    video.style.display = 'none';
    video.srcObject = null;
    img.style.opacity = '';
    window.__roadieExternalFeed = false;
    if (window.__roadieFeed) window.__roadieFeed.start();
  }

  function fallBack(reason) {
    if (fallen || !active) return;
    fallen = true;
    log('falling back to MJPEG: ' + reason);
    showMJPEG();
    try { window.dispatchEvent(new CustomEvent('roadie-transport', {detail: {transport: 'mjpeg', reason: reason}})); } catch (e) {}
  }

  // A see-through image of the video's size, so the page lays #feed out as
  // it would the MJPEG picture and maps input the same way.
  function placeholder(w, h) {
    return 'data:image/svg+xml,' + encodeURIComponent(
      '<svg xmlns="http://www.w3.org/2000/svg" width="' + w + '" height="' + h + '"></svg>');
  }
  var placedSize = '';
  function placeFeed() {
    if (!playing || !video.videoWidth || !video.videoHeight) return;
    var size = video.videoWidth + 'x' + video.videoHeight;
    if (size === placedSize) return;
    placedSize = size;
    img.src = placeholder(video.videoWidth, video.videoHeight);
  }
  var placed = '';
  function syncVideo() {
    var r = img.getBoundingClientRect(), v = viewer.getBoundingClientRect();
    var key = [r.left - v.left, r.top - v.top, r.width, r.height].join(',');
    if (key === placed) return;
    placed = key;
    video.style.left = (r.left - v.left) + 'px';
    video.style.top = (r.top - v.top) + 'px';
    video.style.width = r.width + 'px';
    video.style.height = r.height + 'px';
  }
  // Once a frame while playing: cheap, and it catches every way the page
  // can move or scale the image.
  function follow() {
    if (playing) syncVideo();
    window.requestAnimationFrame(follow);
  }
  window.requestAnimationFrame(follow);
  video.addEventListener('resize', placeFeed);
  video.addEventListener('loadedmetadata', placeFeed);

  video.addEventListener('playing', function () {
    if (!active) return;
    playing = true;
    attempts = 0;
    clearTimeout(firstFrameTimer);
    placeFeed();
    video.style.display = 'block';
    img.style.opacity = '0';
    syncVideo();
    changed();
  });

  function send(obj) {
    if (ws && ws.readyState === 1) ws.send(JSON.stringify(obj));
  }

  function connect() {
    attempts++;
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var sock = new WebSocket(proto + '//' + location.host + '/api/webrtc/signal');
    ws = sock;
    var pending = [];
    sock.onopen = function () { sock.send(JSON.stringify({type: 'hello'})); };
    sock.onerror = function () { log('signaling error'); };
    sock.onclose = function () {
      if (ws !== sock) return;
      ws = null;
      if (fallen || !active) return;
      // Roadie restarted, or the connection failed: try again a few times,
      // then give up on WebRTC until asked again.
      if (attempts < 4) {
        log('signaling closed; retrying');
        if (pc) { try { pc.close(); } catch (e) {} pc = null; }
        setTimeout(function () { if (active && !fallen) connect(); }, 1500);
      } else {
        fallBack('signaling closed ' + attempts + ' times');
      }
    };
    sock.onmessage = function (ev) {
      var m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type === 'offer') {
        pc = new RTCPeerConnection({});
        pc.ontrack = function (e) {
          if (e.track.kind === 'audio') {
            // The phone's sound, on an audio element the page mutes or not.
            if (!audioEl) return;
            audioEl.srcObject = new MediaStream([e.track]);
            audioEl.muted = audioMuted;
            audioEl.play().catch(function () {});
            return;
          }
          // As little buffering as the browser allows: the picture is
          // interactive, so a late frame is worth less than a prompt one.
          try {
            if ('jitterBufferTarget' in e.receiver) e.receiver.jitterBufferTarget = 0;
            if ('playoutDelayHint' in e.receiver) e.receiver.playoutDelayHint = 0;
          } catch (err) {}
          video.srcObject = (e.streams && e.streams[0]) || new MediaStream([e.track]);
          video.play().catch(function () {});
        };
        pc.onicecandidate = function (e) {
          if (e.candidate) send({type: 'candidate', candidate: e.candidate.toJSON()});
        };
        pc.ondatachannel = function (e) {
          if (e.channel.label === 'control') {
            control = e.channel;
            control.binaryType = 'arraybuffer';
            control.onmessage = onControl;
            control.onopen = function () { if (capBps) sendCap(); };
            if (control.readyState === 'open' && capBps) sendCap();
          } else if (e.channel.label === 'hid') {
            hid = e.channel;
          }
        };
        pc.onconnectionstatechange = function () {
          if (!pc) return;
          if (pc.connectionState === 'failed') {
            if (sock === ws) sock.close(); else fallBack('connection failed');
          }
        };
        pc.setRemoteDescription({type: 'offer', sdp: m.sdp}).then(function () {
          pending.forEach(function (c) { pc.addIceCandidate(c).catch(function () {}); });
          pending = [];
          // The audio line both ways, so the mic can go back on it later
          // without another offer.
          pc.getTransceivers().forEach(function (t) {
            if (t.receiver && t.receiver.track && t.receiver.track.kind === 'audio') {
              audioTx = t;
              try { t.direction = 'sendrecv'; } catch (err) {}
            }
          });
          return pc.createAnswer();
        }).then(function (answer) {
          return pc.setLocalDescription(answer).then(function () {
            send({type: 'answer', sdp: answer.sdp});
          });
        }).catch(function (err) { fallBack('offer: ' + err); });
      } else if (m.type === 'candidate' && m.candidate) {
        if (pc && pc.remoteDescription) pc.addIceCandidate(m.candidate).catch(function () {});
        else pending.push(m.candidate);
      } else if (m.type === 'error') {
        fallBack(m.error || 'error');
      }
    };
    clearTimeout(firstFrameTimer);
    firstFrameTimer = setTimeout(function () {
      if (!playing && !fallen && active) fallBack('no video within 8 s');
    }, 8000);
  }

  function sendCap() {
    if (control && control.readyState === 'open') control.send(JSON.stringify({type: 'bitrate', bps: capBps}));
  }

  // The control channel: the encoder's own view of things, once a second.
  function onControl(ev) {
    var data = ev.data, m;
    if (data instanceof ArrayBuffer) data = new TextDecoder().decode(data);
    if (typeof data !== 'string') return;
    try { m = JSON.parse(data); } catch (e) { return; }
    if (m.type === 'stats') lastStatus = m;
  }

  // The stats box, once a second while it is on: which transport shows the
  // picture, and its numbers. Over WebRTC they come from getStats (also on
  // window.__roadieRTCStats for scripts); over MJPEG from the page's own
  // count of frames and bytes.
  var lastMJPEG = null;
  function renderStats() {
    if (!statsEl) return;
    if (!showStats) { statsEl.style.display = 'none'; return; }
    if (playing) { readStats(); return; }
    var m = window.__roadieMJPEGStats ? window.__roadieMJPEGStats() : null;
    if (!m) return;
    var fps = 0, kbps = 0, audioKbps = 0, micKbps = 0;
    if (lastMJPEG) {
      fps = m.frames - lastMJPEG.frames;
      kbps = Math.round((m.bytes - lastMJPEG.bytes) * 8 / 1000);
      if (m.audio && lastMJPEG.audio) audioKbps = Math.round((m.audio.bytes - lastMJPEG.audio.bytes) * 8 / 1000);
      if (m.mic && lastMJPEG.mic) micKbps = Math.round((m.mic.bytes - lastMJPEG.mic.bytes) * 8 / 1000);
    }
    lastMJPEG = m;
    var lines = [
      'video    MJPEG, JPEG quality ' + m.quality,
      'picture  ' + (m.size || '?') + ', ' + fps + ' fps, ' + kbps + ' kbps',
      'audio    ' + (m.audio ? m.audio.format + ', ' + audioKbps + ' kbps' : 'off') +
        (m.mic ? ', mic PCM 16 kHz ' + micKbps + ' kbps' : '')
    ];
    statsEl.textContent = lines.join('\n');
    statsEl.style.display = 'block';
  }
  setInterval(renderStats, 1000);

  function readStats() {
    if (!pc || !pc.getStats) return;
    pc.getStats().then(function (report) {
      var out = {};
      report.forEach(function (st) {
        if (st.type === 'inbound-rtp' && st.kind === 'video') {
          out.framesDecoded = st.framesDecoded;
          out.fps = st.framesPerSecond;
          out.width = st.frameWidth; out.height = st.frameHeight;
          out.bytes = st.bytesReceived;
          out.packetsLost = st.packetsLost;
          out.pliCount = st.pliCount;
          if (st.jitterBufferEmittedCount) out.jitterBufferMs = Math.round(1000 * st.jitterBufferDelay / st.jitterBufferEmittedCount);
          if (st.framesDecoded) out.decodeMs = Math.round(1000 * st.totalDecodeTime / st.framesDecoded * 10) / 10;
          if (st.framesDecoded && st.totalProcessingDelay) out.processingMs = Math.round(1000 * st.totalProcessingDelay / st.framesDecoded);
        } else if (st.type === 'inbound-rtp' && st.kind === 'audio') {
          out.audioBytes = st.bytesReceived;
        } else if (st.type === 'candidate-pair' && st.nominated && st.currentRoundTripTime !== undefined) {
          out.rttMs = Math.round(st.currentRoundTripTime * 1000 * 10) / 10;
        }
      });
      var prev = window.__roadieRTCStats;
      if (prev && out.bytes !== undefined && prev.bytes !== undefined) {
        out.kbps = Math.round((out.bytes - prev.bytes) * 8 / 1000);
      }
      if (prev && out.audioBytes !== undefined && prev.audioBytes !== undefined) {
        out.audioKbps = Math.round((out.audioBytes - prev.audioBytes) * 8 / 1000);
      }
      if (lastStatus) out.encoder = lastStatus;
      window.__roadieRTCStats = out;
      if (showStats && statsEl && playing) {
        var ms = function (v) { return v !== undefined ? v + ' ms' : '?'; };
        var lines = [
          'video    WebRTC, H.264',
          'picture  ' + (out.width || '?') + 'x' + (out.height || '?') + ', ' + (out.fps || 0) + ' fps, ' + (out.kbps || 0) + ' kbps',
          'delay    round trip ' + ms(out.rttMs) + ', buffer ' + ms(out.jitterBufferMs) + ', decode ' + ms(out.decodeMs)
        ];
        if (lastStatus && lastStatus.status) {
          lines.push('encoder  ' + lastStatus.status.encoder + ', ' + (lastStatus.status.fps || 0) + ' fps, ' + (lastStatus.kbps || 0) + ' kbps sent, target ' + Math.round((lastStatus.status.bitrate || 0) / 1000) + ' kbps');
          if (lastStatus.status.audio) {
            lines.push('audio    ' + lastStatus.status.audio + ', ' + (out.audioKbps || 0) + ' kbps' +
              (lastStatus.status.mic ? ', mic ' + lastStatus.status.mic + ' ' + (lastStatus.status.mic_kbps || 0) + ' kbps' : ''));
          }
        }
        if (out.packetsLost) lines.push('lost     ' + out.packetsLost + ' packets');
        statsEl.textContent = lines.join('\n');
        statsEl.style.display = 'block';
      }
    }).catch(function () {});
  }

  function start() {
    if (active) return;
    active = true;
    fallen = false;
    attempts = 0;
    window.__roadieExternalFeed = true;
    if (window.__roadieFeed) window.__roadieFeed.stop();
    connect();
  }
  function stop() {
    if (!active) return;
    active = false;
    fallen = false;
    showMJPEG();
  }

  // For the page's menu, scripts and tests.
  window.__roadieRTC = {
    start: start,
    stop: stop,
    active: function () { return active && !fallen; },
    isPlaying: function () { return playing && !fallen; },
    fellBack: function () { return fallen; },
    showStats: function (on) {
      showStats = !!on;
      lastMJPEG = null;
      renderStats();
    },
    statsShown: function () { return showStats; },
    requestKeyframe: function () { if (control && control.readyState === 'open') control.send(JSON.stringify({type: 'keyframe'})); },
    capBitrate: function (bps) { capBps = bps || 0; sendCap(); },
    // Input over the "hid" channel; false when it is not open, so the
    // page sends over its WebSocket instead.
    sendHID: function (msg) {
      if (!hid || hid.readyState !== 'open') return false;
      hid.send(JSON.stringify(msg));
      return true;
    },
    setMuted: function (m) {
      audioMuted = !!m;
      if (!audioEl) return;
      audioEl.muted = audioMuted;
      // Unmuting is a click, which lets a browser that blocks sound
      // without one start playing now.
      if (!audioMuted && audioEl.srcObject) audioEl.play().catch(function () {});
    },
    // The viewer's microphone track onto the audio line, or null to stop;
    // false when there is no line to put it on.
    setMic: function (track) {
      if (!audioTx || !audioTx.sender) return false;
      audioTx.sender.replaceTrack(track).catch(function () {});
      return true;
    }
  };

  // Which transport to begin with: ?transport= for this page load, else
  // what this browser chose in the menu, else WebRTC, since the host
  // offers it (this script is only loaded when it does).
  var wanted = window.__roadieTransport;
  var saved = {};
  try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch (e) {}
  if (!wanted) wanted = saved.transport || 'webrtc';
  showStats = /[?&]stats=1/.test(location.search) || !!saved.stats;
  if (wanted === 'webrtc') start();
})();
