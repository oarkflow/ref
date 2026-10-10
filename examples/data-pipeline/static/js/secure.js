// Starts the secure transport: fh's WebAssembly client (github.com/oarkflow/fh/wasm).
//
// It fetches the pins and a one-time registration grant from /secure-config.json
// (the only plain request the page makes), checks the client's own files against
// the integrity values in that answer, registers this browser's device key, and
// then replaces the page's fetch with the secure one. From then on every request
// the console makes, including sign-in, travels as an encrypted, device-bound,
// replay-protected envelope whose reply is signed and verified before it is
// decrypted. If any step fails, nothing is sent: the console shows why.
import { createSecureFetch, clearDevice } from '/static/wasm/index.js';

var TAG_KEY = 'pipeline.device.principal';

async function start(forget) {
  var res = await fetch('/secure-config.json', { credentials: 'same-origin', cache: 'no-store' });
  if (!res.ok) throw new Error('the server would not start a secure session (' + res.status + ')');
  var cfg = await res.json();
  // The device is registered for one principal (anonymous, or a signed-in user).
  // When that is not who this page is for, or the server no longer knows it
  // (it was restarted), forget it and register afresh.
  var last = null;
  try { last = localStorage.getItem(TAG_KEY); } catch (e) { /* storage may be unavailable */ }
  if (forget || last !== cfg.principalTag) await clearDevice();
  var secure = await createSecureFetch(Object.assign({}, cfg, {
    credentials: 'same-origin',
    clientBuild: 'pipeline-console-1',
    deviceName: 'Browser',
    installGlobal: true
  }));
  try { localStorage.setItem(TAG_KEY, cfg.principalTag); } catch (e) { /* ignore */ }
  return { secure: secure, cfg: cfg };
}

(async function () {
  var C = window.C, up;
  try {
    try { up = await start(false); } catch (first) { up = await start(true); }
    C.secureReady(up.secure, up.cfg);
  } catch (e) {
    C.secureFailed(e);
  }
})();
