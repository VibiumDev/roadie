// Roadie's WebRTC transport for the viewer: H.264 from the host's encoder
// over a peer connection, with the MJPEG feed as the fallback. The page
// loads this when asked for ?transport=webrtc; it claims the picture before
// the page's own script starts the MJPEG stream.
//
// The page maps pointer input against the #feed image's size and position,
// so #feed stays: see-through, sized like the video, on top of it. The
// video sits exactly underneath. Input itself still travels the page's
// WebSocket in this version.
(function () {
  if (window.__roadieTransport !== 'webrtc' || !window.RTCPeerConnection) return;
  window.__roadieExternalFeed = true;

  var img = document.getElementById('feed');
  var video = document.getElementById('rtcVideo');
  var viewer = document.getElementById('viewer');
  var statsEl = document.getElementById('rtcStats');
  var showStats = /[?&]stats=1/.test(location.search);

  var pc = null, ws = null, control = null;
  var playing = false;     // a picture has been shown over WebRTC
  var fallen = false;      // we gave up and the page shows MJPEG
  var attempts = 0;
  var firstFrameTimer = null, statsTimer = null;
  var lastStatus = null;

  function log(msg) { if (window.console) console.log('webrtc: ' + msg); }

  function teardown() {
    clearTimeout(firstFrameTimer);
    clearInterval(statsTimer);
    if (ws) { var w = ws; ws = null; try { w.onclose = null; w.close(); } catch (e) {} }
    if (pc) { var p = pc; pc = null; try { p.close(); } catch (e) {} }
    control = null;
  }

  // Back to MJPEG: the page's watchdog starts /stream within a second once
  // the flag is down.
  function fallBack(reason) {
    if (fallen) return;
    fallen = true;
    log('falling back to MJPEG: ' + reason);
    teardown();
    video.style.display = 'none';
    video.srcObject = null;
    img.style.opacity = '';
    if (statsEl) statsEl.style.display = 'none';
    window.__roadieExternalFeed = false;
  }

  // A see-through image of the video's size, so the page lays #feed out as
  // it would the MJPEG picture and maps input the same way.
  function placeholder(w, h) {
    return 'data:image/svg+xml,' + encodeURIComponent(
      '<svg xmlns="http://www.w3.org/2000/svg" width="' + w + '" height="' + h + '"></svg>');
  }
  var placedSize = '';
  function placeFeed() {
    if (!video.videoWidth || !video.videoHeight) return;
    var size = video.videoWidth + 'x' + video.videoHeight;
    if (size === placedSize) return;
    placedSize = size;
    img.src = placeholder(video.videoWidth, video.videoHeight);
  }
  function syncVideo() {
    if (!playing) return;
    var r = img.getBoundingClientRect(), v = viewer.getBoundingClientRect();
    video.style.left = (r.left - v.left) + 'px';
    video.style.top = (r.top - v.top) + 'px';
    video.style.width = r.width + 'px';
    video.style.height = r.height + 'px';
  }
  video.addEventListener('resize', placeFeed);
  video.addEventListener('loadedmetadata', placeFeed);
  if (window.ResizeObserver) {
    var ro = new ResizeObserver(syncVideo);
    ro.observe(img);
    ro.observe(viewer);
  }
  window.addEventListener('resize', syncVideo);
  // The page's own onload sets the image's display; the position follows.
  img.addEventListener('load', function () { setTimeout(syncVideo, 0); });

  video.addEventListener('playing', function () {
    playing = true;
    attempts = 0;
    clearTimeout(firstFrameTimer);
    placeFeed();
    video.style.display = 'block';
    img.style.opacity = '0';
    syncVideo();
    if (showStats && statsEl) { statsEl.style.display = 'block'; }
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
      if (fallen) return;
      // Roadie restarted, or the connection failed: try again a few times,
      // then give up on WebRTC for this page load.
      if (attempts < 4) {
        log('signaling closed; retrying');
        if (pc) { try { pc.close(); } catch (e) {} pc = null; }
        setTimeout(connect, 1500);
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
            control.onmessage = onControl;
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
      if (!playing && !fallen) fallBack('no video within 8 s');
    }, 8000);
    clearInterval(statsTimer);
    statsTimer = setInterval(readStats, 1000);
  }

  // The control channel: the encoder's own view of things, once a second.
  function onControl(ev) {
    var m;
    try { m = JSON.parse(ev.data); } catch (e) { return; }
    if (m.type === 'stats') lastStatus = m;
  }

  // What the browser sees: frames, delay in its jitter buffer, decode time
  // and the round trip. Kept on window.__roadieRTCStats for scripts, and
  // shown over the picture with ?stats=1.
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
        } else if (st.type === 'candidate-pair' && st.nominated && st.currentRoundTripTime !== undefined) {
          out.rttMs = Math.round(st.currentRoundTripTime * 1000 * 10) / 10;
        }
      });
      var prev = window.__roadieRTCStats;
      if (prev && out.bytes !== undefined && prev.bytes !== undefined) {
        out.kbps = Math.round((out.bytes - prev.bytes) * 8 / 1000);
      }
      if (lastStatus) out.encoder = lastStatus;
      window.__roadieRTCStats = out;
      if (showStats && statsEl && playing) {
        statsEl.textContent =
          (out.width || '?') + 'x' + (out.height || '?') + ' ' + (out.fps || 0) + ' fps ' + (out.kbps || 0) + ' kbps\n' +
          'rtt ' + (out.rttMs !== undefined ? out.rttMs : '?') + ' ms, jitter buffer ' + (out.jitterBufferMs !== undefined ? out.jitterBufferMs : '?') + ' ms, decode ' + (out.decodeMs !== undefined ? out.decodeMs : '?') + ' ms' +
          (lastStatus && lastStatus.status ? '\nencoder ' + lastStatus.status.encoder + ' ' + Math.round((lastStatus.status.bitrate || 0) / 1000) + ' kbps target, ' + (lastStatus.kbps || 0) + ' kbps out, ' + (lastStatus.status.fps || 0) + ' fps' : '');
      }
    }).catch(function () {});
  }

  // For scripts and tests.
  window.__roadieRTC = {
    requestKeyframe: function () { if (control && control.readyState === 'open') control.send(JSON.stringify({type: 'keyframe'})); },
    capBitrate: function (bps) { if (control && control.readyState === 'open') control.send(JSON.stringify({type: 'bitrate', bps: bps})); },
    isPlaying: function () { return playing && !fallen; },
    fellBack: function () { return fallen; }
  };

  connect();
})();
