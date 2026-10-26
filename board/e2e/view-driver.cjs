// Drives roadie's /view in a headless browser, through Vibium, for
// test_e2e.py --via view: the e2e checks' input goes in as a person's
// mouse, keys and wheel on /view's picture, and /view sends it to roadie.
//
// Usage: node view-driver.cjs <path to the vibium package> <view URL>
//
// One command a line on stdin, JSON; one answer a line on stdout:
//   {"op": "rect"}                          where the picture is, in the page
//   {"op": "move", "x": 10.5, "y": 20}      the mouse, in the page's pixels
//   {"op": "down"} / {"op": "up"}           the left button
//   {"op": "wheel", "dy": 25, "count": 10, "gap": 30}
//                                           count turns of dy pixels, gap ms apart
//   {"op": "type", "text": "abc"}
//   {"op": "mode", "mode": "touch"}         /view's Mouse/Touch switch
//   {"op": "eval", "expr": "..."}           an expression's value, for debugging
//   {"op": "quit"}
// Answers are {"ok": true, ...} or {"ok": false, "error": "..."}.

const readline = require('readline');
const { browser } = require(process.argv[2]);
const viewURL = process.argv[3];

const say = (obj) => process.stdout.write(JSON.stringify(obj) + '\n');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  const b = await browser.start({ headless: true });
  const page = await b.page();
  // Tall enough for a phone's picture at its own size.
  await page.setViewport({ width: 1000, height: 1600 });
  await page.go(viewURL);
  // The picture, then a moment for /view's input socket, which it opens
  // as it starts and keeps to itself.
  for (let i = 0; i < 100; i++) {
    const ready = await page.evaluate(
      `(() => { const i = document.getElementById('feed'); return !!(i && i.naturalWidth > 0); })()`);
    if (ready) break;
    await sleep(100);
  }
  await sleep(2000);
  say({ ok: true, ready: true });

  const ops = {
    async rect() {
      return JSON.parse(await page.evaluate(`JSON.stringify((() => {
        const r = document.getElementById('feed').getBoundingClientRect();
        return { x: r.left, y: r.top, width: r.width, height: r.height };
      })())`));
    },
    async move(c) { await page.mouse.move(c.x, c.y); },
    async down() { await page.mouse.down(); },
    async up() { await page.mouse.up(); },
    async wheel(c) {
      for (let i = 0; i < (c.count || 1); i++) {
        if (i > 0) await sleep(c.gap || 30);
        await page.mouse.wheel(0, c.dy);
      }
    },
    async type(c) { await page.keyboard.type(c.text); },
    // For looking into a failure: a JavaScript expression's value, as JSON.
    async eval(c) { return { value: await page.evaluate(c.expr) }; },
    async mode(c) {
      const id = c.mode === 'touch' ? 'modeTouchBtn' : 'modeMouseBtn';
      await page.evaluate(`document.getElementById('${id}').click()`);
    },
  };

  const lines = readline.createInterface({ input: process.stdin });
  for await (const line of lines) {
    let cmd;
    try { cmd = JSON.parse(line); } catch (e) { say({ ok: false, error: 'not JSON' }); continue; }
    if (cmd.op === 'quit') break;
    const op = ops[cmd.op];
    if (!op) { say({ ok: false, error: `no op ${cmd.op}` }); continue; }
    try {
      const out = await op(cmd);
      say(Object.assign({ ok: true }, out || {}));
    } catch (e) {
      say({ ok: false, error: String(e && e.message || e) });
    }
  }
  await b.stop();
})().catch((e) => {
  say({ ok: false, error: String(e && e.message || e), fatal: true });
  process.exit(1);
});
