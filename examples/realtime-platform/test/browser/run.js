// Drives the real demo page, in real Chrome, through group calls.
const { spawn, execFileSync } = require('child_process');
const fs = require('fs'), os = require('os'), path = require('path');
const { launchChrome, newUser, sleep } = require('./lib');

// Where things are: the demo application, and the two programs it runs against. See README.md.
const APP = process.env.APPDIR || path.resolve(__dirname, '../..');
const REF = process.env.REFBIN || path.join(APP, '.data', 'refbin');
const RTCD = process.env.RTCDBIN || path.join(APP, '.data', 'rtcd');
const PORT = +process.env.PORT || 18090;
const BASE = `http://127.0.0.1:${PORT}`;
const only = process.argv.slice(2); // scenario names; none means all
const results = []; let failed = 0;

function check(name, ok, detail = '') { results.push({ name, ok, detail }); if (!ok) failed++; console.log(`${ok ? '  PASS' : '  FAIL'}  ${name}${detail ? '  — ' + detail : ''}`); }

async function startServer(tmp) {
  // The call service is a separate process; the application only mints tokens for it.
  const RTCD_PORT = PORT + 400, RTCD_URL = `http://127.0.0.1:${RTCD_PORT}`;
  const rtcdCfg = path.join(tmp, 'rtcd.bcl');
  fs.writeFileSync(rtcdCfg, `
listen "127.0.0.1:${RTCD_PORT}"
database { driver "sqlite" dsn "file:${tmp}/rtcd.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" }
audit { dsn "file:${tmp}/rtcd-audit.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" file "${tmp}/rtcd-audit.jsonl" }
key "demo" { key env.required("RTCD_API_KEY") tenant "demo" }
token_secret env.required("RTCD_TOKEN_SECRET")
allowed_origins ["${BASE}"]
udp_port_min 50000
udp_port_max 50200
record_dir "${tmp}/rec"
record_layer "${process.env.RT_RECORD_LAYER || 'highest'}"
`);
  const base = { ...process.env, RTCD_API_KEY: 'browser-test-rtcd-key-0123456789', RTCD_TOKEN_SECRET: 'browser-test-rtcd-secret-0123456789' };
  const r = spawn(RTCD, ['-config', rtcdCfg], { env: base, stdio: ['ignore', 'pipe', 'pipe'] });
  let rout = ''; r.stdout.on('data', d => rout += d); r.stderr.on('data', d => rout += d);
  if (process.env.RTCD_LOG) { const ws = fs.createWriteStream(process.env.RTCD_LOG); r.stdout.pipe(ws, { end: false }); r.stderr.pipe(ws, { end: false }); }
  for (let i = 0; i < 100; i++) { try { const x = await fetch(RTCD_URL + '/healthz'); if (x.ok) break; } catch {} await sleep(100); if (i === 99) throw new Error('rtcd did not start: ' + rout); }

  const env = { ...base, SESSION_SECRET: 'browser-test-session-secret-0123456789abcdef', DEMO_PASSWORD: 'demo-password', OPS_API_KEY: 'browser-test-ops-key-0123456789',
    RTCD_URL, RT_DB: `file:${tmp}/rt.db?_pragma=busy_timeout(5000)`, RT_LOG: `${tmp}/app.log` };
  const p = spawn(REF, ['-addr', `127.0.0.1:${PORT}`, APP], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  let out = ''; p.stdout.on('data', d => out += d); p.stderr.on('data', d => out += d);
  const kill = p.kill.bind(p); p.kill = (...a) => { r.kill(...a); return kill(...a); };
  for (let i = 0; i < 100; i++) { try { const x = await fetch(BASE + '/api/me'); if (x.status) return { p, out: () => out + rout }; } catch {} await sleep(150); }
  throw new Error('server did not start: ' + out);
}

const STATS = `(async () => {
  const out = { tiles: [], inbound: [], outbound: [], pair: null, state: call && call.pc && call.pc.iceConnectionState };
  document.querySelectorAll('#tiles figure').forEach(f => { const v = f.querySelector('video'); out.tiles.push({ user: f.dataset.user, w: v.videoWidth, h: v.videoHeight, t: v.currentTime, ready: v.readyState, speaking: f.classList.contains('speaking'), pinned: f.classList.contains('pinned') }); });
  if (!call || !call.pc) return JSON.stringify(out);
  const rep = await call.pc.getStats(); const codecs = {};
  rep.forEach(r => { if (r.type === 'codec') codecs[r.id] = r.mimeType; });
  rep.forEach(r => {
    if (r.type === 'inbound-rtp' && r.kind === 'video') out.inbound.push({ ssrc: r.ssrc, pk: r.packetsReceived, fr: r.framesReceived, key: r.keyFramesDecoded, drop: r.framesDropped, fir: r.firCount, jb: r.jitterBufferEmittedCount, frames: r.framesDecoded, w: r.frameWidth, h: r.frameHeight, codec: codecs[r.codecId], lost: r.packetsLost, pli: r.pliCount, nack: r.nackCount, freeze: r.freezeCount });
    if (r.type === 'outbound-rtp' && r.kind === 'video') out.outbound.push({ rid: r.rid, active: r.active, w: r.frameWidth, h: r.frameHeight, frames: r.framesEncoded, bytes: r.bytesSent, codec: codecs[r.codecId], sm: r.scalabilityMode });
    if (r.type === 'candidate-pair' && r.nominated && r.state === 'succeeded') out.pair = { rtt: r.currentRoundTripTime };
  });
  try { out.sfu = await sfu('stats'); } catch (e) { out.sfuError = e.message; }
  return JSON.stringify(out);
})()`;

const stats = async p => JSON.parse(await p.eval(STATS));

async function login(page, user) {
  await page.eval(`document.getElementById('user').value='${user}'; document.getElementById('pw').value='demo-password'; document.getElementById('go').click(); true`);
  await page.until(`typeof me === 'string' && me === '${user}' && typeof chans !== 'undefined' && !document.getElementById('app').classList.contains('hidden')`, 'login', 15000);
}

async function setupRoom(pages, name) {
  const [ada, ...rest] = pages;
  await ada.eval(`(async()=>{ const c = await api('/api/channels',{name:${JSON.stringify(name)},members:['bob','cy']}); await refresh(c.id); return c.id; })()`);
  for (const p of rest) {
    await p.until(`(async()=>{ await refresh(); return !!chans.find(x=>x.name===${JSON.stringify(name)}); })()`, 'channel list', 15000);
    await p.eval(`(async()=>{ await openChan(chans.find(x=>x.name===${JSON.stringify(name)}).id); return true; })()`);
  }
}

async function groupCall(pages, codec) {
  const codecs = Array.isArray(codec) ? codec : [codec, codec, codec];   // one per person, or all the same
  const [ada, bob, cy] = pages;
  await ada.eval(`document.getElementById('codec').value='${codecs[0]}'; document.getElementById('groupbtn').click(); true`);
  for (const [i, p] of [[1, bob], [2, cy]]) {
    await p.until(`call && call.mode === 'sfu' && document.getElementById('accept') && !document.getElementById('accept').classList.contains('hidden')`, 'incoming call', 20000);
    await p.eval(`call.codec='${codecs[i]}'; document.getElementById('accept').click(); true`);
  }
}

async function everyoneSeesEveryone(pages, label, timeout = 40000) {
  const names = ['ada', 'bob', 'cy'];
  for (let i = 0; i < 3; i++) {
    const p = pages[i], others = names.filter((_, j) => j !== i);
    try {
      await p.until(`(()=>{ const t=[...document.querySelectorAll('#tiles figure')]; return t.length===2 && t.every(f=>{const v=f.querySelector('video'); return v.videoWidth>0 && v.readyState>=2}); })()`, `2 decoded tiles`, timeout);
    } catch (e) { const s = await stats(p).catch(() => ({})); check(`${label}: ${names[i]} sees ${others.join(' and ')}`, false, e.message + ' ' + JSON.stringify(s).slice(0, 400)); continue; }
    const s1 = await stats(p); await sleep(1500); const s2 = await stats(p);
    const users = s2.tiles.map(t => t.user).sort().join(',');
    const before = new Map(s1.inbound.map(x => [x.ssrc, x.frames])); const advancing = s2.inbound.length === 2 && s2.inbound.every(x => x.frames > (before.get(x.ssrc) ?? 0));
    check(`${label}: ${names[i]} sees ${others.join(' and ')} (decoding, frames advancing)`, users === others.sort().join(',') && advancing, `tiles=${users} inbound=${JSON.stringify(s2.inbound.map(x => [x.ssrc, x.w + 'x' + x.h, x.frames, '+' + (x.frames - (before.get(x.ssrc) ?? 0)), 'freeze' + x.freeze]))}`);
  }
}

async function main() {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rt-browser-'));
  const server = await startServer(tmp);
  const chrome = await launchChrome(9333, process.env.CHROME_FLAGS ? process.env.CHROME_FLAGS.split(' ') : []);
  console.log('Chrome', chrome.version.Browser, ' server', BASE);
  const pages = [];
  try {
    for (const u of ['ada', 'bob', 'cy']) { const p = await newUser(chrome, u, BASE + '/'); await login(p, u); pages.push(p); }
    check('three users signed in, in three separate browser profiles', true);
    const scenarios = require('./scenarios');
    for (const [name, fn] of Object.entries(scenarios)) {
      if (only.length && !only.includes(name)) continue;
      console.log(`\n== ${name}`);
      try { await fn({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone, sleep, tmp, BASE, login }); }
      catch (e) { check(`${name} ran to completion`, false, e.message); }
      // leave any call before the next scenario
      for (const p of pages) await p.eval(`(async()=>{ if (call) { hangup(true); } return true; })()`).catch(() => {});
      await sleep(1500);
    }
    const errs = pages.flatMap(p => p.errors.map(e => `${p.name}: ${e}`));
    check('no uncaught JavaScript errors in any page', errs.length === 0, errs.slice(0, 3).join(' | '));
  } finally {
    if (failed || process.env.AUDIT_DUMP) {
      // The call service keeps a complete audit log: what it refused or could not do explains a failure.
      try {
        const key = 'browser-test-rtcd-key-0123456789', url = `http://127.0.0.1:${PORT + 400}`;
        for (const outcome of ['error', 'denied']) {
          const r = await fetch(url + '/v1/audit/query', { method: 'POST', headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json' }, body: JSON.stringify({ outcome, limit: 100 }) });
          const j = await r.json(); const by = {};
          for (const e of j.events || []) { const k = `${e.action} ${e.code}`; by[k] = (by[k] || 0) + 1; }
          console.log(`service audit, ${outcome}:`, JSON.stringify(by));
        }
      } catch (e) { console.log('could not read the audit log:', e.message); }
    }
    if (process.env.CHROME_LOG) fs.writeFileSync(process.env.CHROME_LOG, chrome.log());
    pages.forEach(p => p.conn.close());
    chrome.proc.kill(); server.p.kill();
    console.log(`\n${results.length - failed} passed, ${failed} failed`);
    fs.writeFileSync(path.join(tmp, 'results.json'), JSON.stringify(results, null, 2));
    console.log('artifacts:', tmp);
  }
  process.exit(failed ? 1 : 0);
}
main().catch(e => { console.error(e); process.exit(2); });
