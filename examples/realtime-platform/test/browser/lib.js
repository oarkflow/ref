// A small DevTools-protocol driver: launches Chrome with a fake camera and
// microphone, gives each user an isolated profile, and runs code in the page.
const { spawn } = require('child_process');
const fs = require('fs'), os = require('os'), path = require('path');

const CHROME = process.env.CHROME || ({
  darwin: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  linux: 'google-chrome',
  win32: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
}[process.platform] || 'google-chrome');
const sleep = ms => new Promise(r => setTimeout(r, ms));

class Conn {
  constructor(url) { this.url = url; this.id = 0; this.pending = new Map(); this.handlers = []; }
  async open() {
    this.ws = new WebSocket(this.url);
    await new Promise((res, rej) => { this.ws.onopen = res; this.ws.onerror = e => rej(new Error('ws error ' + this.url)); });
    this.ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && this.pending.has(m.id)) { const { res, rej } = this.pending.get(m.id); this.pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); }
      else this.handlers.forEach(h => h(m));
    };
    return this;
  }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((res, rej) => { this.pending.set(id, { res, rej }); this.ws.send(JSON.stringify({ id, method, params })); });
  }
  on(h) { this.handlers.push(h); }
  close() { try { this.ws.close(); } catch {} }
}

async function launchChrome(port, extra = []) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'chrome-'));
  const args = [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${dir}`,
    '--no-first-run', '--no-default-browser-check', '--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream',
    '--autoplay-policy=no-user-gesture-required', '--disable-features=WebRtcHideLocalIpsWithMdns',
    '--allow-loopback-in-peer-connection', '--disable-gpu', '--mute-audio', ...extra, 'about:blank',
  ];
  const proc = spawn(CHROME, args, { stdio: ['ignore', 'ignore', 'pipe'] });
  let log = ''; proc.stderr.on('data', d => { log += d; });
  for (let i = 0; i < 100; i++) {
    try { const r = await fetch(`http://127.0.0.1:${port}/json/version`); if (r.ok) return { proc, dir, port, version: await r.json(), log: () => log }; } catch {}
    await sleep(100);
  }
  throw new Error('chrome did not start: ' + log);
}

class Page {
  constructor(name, conn) { this.name = name; this.conn = conn; this.errors = []; this.console = []; }
  async eval(expr, { timeout = 60000 } = {}) {
    const r = await Promise.race([
      this.conn.send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true }),
      sleep(timeout).then(() => { throw new Error(`${this.name}: evaluate timed out: ${expr.slice(0, 80)}`); }),
    ]);
    if (r.exceptionDetails) throw new Error(`${this.name}: ${r.exceptionDetails.exception?.description || r.exceptionDetails.text}`);
    return r.result.value;
  }
  async until(expr, what, timeoutMs = 30000, every = 250) {
    const end = Date.now() + timeoutMs; let last;
    while (Date.now() < end) {
      try { last = await this.eval(expr); if (last) return last; } catch (e) { last = e.message; }
      await sleep(every);
    }
    throw new Error(`${this.name}: timed out after ${timeoutMs}ms waiting for ${what} (last: ${JSON.stringify(last)})`);
  }
}

async function newUser(chrome, name, url) {
  const browser = await new Conn(chrome.version.webSocketDebuggerUrl).open();
  const { browserContextId } = await browser.send('Target.createBrowserContext', { disposeOnDetach: true });
  const { targetId } = await browser.send('Target.createTarget', { url, browserContextId });
  const list = await (await fetch(`http://127.0.0.1:${chrome.port}/json/list`)).json();
  const t = list.find(x => x.id === targetId);
  const conn = await new Conn(t.webSocketDebuggerUrl).open();
  const page = new Page(name, conn);
  conn.on(m => {
    if (m.method === 'Runtime.exceptionThrown') page.errors.push(m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text);
    if (m.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(m.params.type)) page.console.push(m.params.type + ': ' + m.params.args.map(a => a.value ?? a.description).join(' '));
  });
  await conn.send('Runtime.enable'); await conn.send('Page.enable');
  await page.until('document.readyState === "complete" && typeof api === "function"', 'page load', 15000);
  page.browser = browser; page.targetId = targetId; page.browserContextId = browserContextId;
  return page;
}

module.exports = { launchChrome, newUser, sleep, Conn, Page };
