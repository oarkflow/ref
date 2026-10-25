// Each scenario drives the real demo page in real Chrome, with three users in three
// separate browser profiles. Calls are end-to-end encrypted, as the service requires.
const room = p => p + '-' + Date.now();

module.exports = {
  // A client may restart ICE at any moment, even when nothing is wrong. The server must
  // keep the old path working until the new one is selected, or the restarting
  // browser's video freezes for seconds (it keeps sending to sockets the server closed).
  // With the pion/ice fork this stays under a second; without it 9 of 14 runs stalled
  // for 3 to 10 seconds.
  async ice_restart_of_a_healthy_connection({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone, sleep }) {
    await setupRoom(pages, room('restart'));
    await groupCall(pages, 'vp8');
    await everyoneSeesEveryone(pages, 'before the restart');
    const [ada, bob, cy] = pages;
    await cy.eval(`restartIce('test: a healthy connection restarted by its client'); true`);
    // Bob's view of cy, in half-second steps for ten seconds: how many steps had a stream that did not advance?
    let prev = (await stats(bob)).inbound, stalled = 0;
    for (let i = 0; i < 20; i++) {
      await sleep(500);
      const now = (await stats(bob)).inbound;
      if (now.some(x => x.frames <= (prev.find(y => y.ssrc === x.ssrc) || { frames: 0 }).frames)) stalled++;
      prev = now;
    }
    check('no viewer\'s video froze for more than two seconds after a client restarted ICE', stalled <= 4, `${stalled} of 20 half-second steps had a frozen stream`);
    await everyoneSeesEveryone(pages, 'after the restart');
  },

  async p2p_call_is_authenticated({ pages, check, setupRoom, sleep }) {
    await setupRoom(pages, room('p2p'));
    const [ada, bob] = pages;
    await ada.eval('document.getElementById("callbtn").click(); true');
    await bob.until('call && !document.getElementById("accept").classList.contains("hidden")', 'incoming p2p call', 20000);
    await bob.eval('document.getElementById("accept").click(); true');
    for (const p of [ada, bob]) {
      await p.until('(()=>{ const v=document.querySelector("#tiles video"); return v && v.videoWidth>0 && v.readyState>=2; })()', 'p2p video', 40000);
    }
    const lock = p => p.eval('document.getElementById("lock").textContent');
    const [la, lb] = [await lock(ada), await lock(bob)];
    check('both sides show the call as end-to-end encrypted', /End-to-end encrypted/.test(la) && /End-to-end encrypted/.test(lb), la + ' | ' + lb);
    const code = t => (t.match(/Safety code: ([\d ]+)/) || [])[1];
    check('both sides compute the same safety code from the signed fingerprints', code(la) && code(la) === code(lb), code(la) + ' vs ' + code(lb));
    const frames = async p => p.eval('(async()=>{ let n=0; (await call.pc.getStats()).forEach(r=>{ if(r.type==="inbound-rtp"&&r.kind==="video") n=r.framesDecoded; }); return n; })()');
    const f1 = await frames(bob); await sleep(2000); const f2 = await frames(bob);
    check('video is flowing', f2 > f1 + 10, f1 + ' -> ' + f2);
    check('the call is p2p, not through the SFU', await ada.eval('call.mode') === 'p2p');
  },


  async e2ee_group_call({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone, sleep }) {
    await setupRoom(pages, room('e2ee'));
    await groupCall(pages, 'vp8');
    await everyoneSeesEveryone(pages, 'end-to-end encrypted VP8');
    const ada = pages[0];
    check('the call is flagged end-to-end encrypted', await ada.eval('!!call.e2ee'));
    const w = await ada.eval('e2e && e2e.stats ? e2e.stats : null');
    await sleep(2500);
    const w2 = await ada.eval('e2e.stats');
    check('frames are really being encrypted and decrypted by the worker', w2.encrypted > 100 && w2.decrypted > 100, JSON.stringify(w2));
    check('nothing was refused in a healthy call', (w2.dropped || 0) < 20, JSON.stringify(w2.reasons));
    const codes = await Promise.all(pages.map(p => p.eval('e2e.session.safetyCode()')));
    check('everybody computes the same safety code', new Set(codes).size === 1, codes.join(' | '));
    const sample = await ada.eval('sfu("sample")');
    check('the server reports the call as encrypted', sample.e2ee === true);
    const hex = sample.sample.video || '';
    // A sample is the first bytes of one RTP payload the server holds. The E2EE
    // header sits only at the start of a frame, so look at several packets.
    let sawHeader = false, sawPlainKeyframe = false, last = '';
    for (let i = 0; i < 12 && !sawHeader; i++) {
      const smp = (await ada.eval('sfu("sample")')).sample.video || '';
      last = smp;
      if (/e2[0-9a-f]{2}[0-9a-f]{8}0000/.test(smp)) sawHeader = true;
      if (/9d012a/.test(smp)) sawPlainKeyframe = true; // the VP8 keyframe start code, if the picture were readable
      await sleep(120);
    }
    check('the payload the server holds carries the E2EE header (so the picture inside is encrypted)', sawHeader && !sawPlainKeyframe, last.slice(0, 48));
    const st = await stats(ada);
    check('every inbound video is VP8', st.inbound.every(i => /vp8/i.test(i.codec)), JSON.stringify(st.inbound.map(i => i.codec)));
  },


  async e2ee_leaver_is_locked_out({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone, sleep }) {
    await setupRoom(pages, room('e2eeleave'));
    await groupCall(pages, 'vp8');
    await everyoneSeesEveryone(pages, 'before a member is removed');
    const [ada, bob, cy] = pages;
    const kidBefore = await ada.eval('e2e.session.kid');
    // cy leaves; the others must rekey.
    await cy.eval('hangup(true); true');
    await ada.until(`e2e.session.kid !== ${kidBefore}`, 'ada rekeys after cy leaves', 30000);
    await bob.until(`e2e.stats && e2e.stats.decrypted > 0 && !${JSON.stringify(false)}`, 'bob still decrypting', 15000);
    await sleep(3000);
    const a = await stats(ada);
    check('ada and bob still see each other after the rekey', a.inbound.length >= 1 && a.inbound.every(i => i.frames > 0));
    const b1 = await bob.eval('e2e.stats.decrypted'); await sleep(2500); const b2 = await bob.eval('e2e.stats.decrypted');
    check('bob keeps decrypting new frames with the new key', b2 > b1 + 20, b1 + ' -> ' + b2);
  },


  async e2ee_refuses_without_encryption({ pages, check, setupRoom, sleep }) {
    await setupRoom(pages, room('e2eeoff'));
    const bob = pages[1];
    // Take away the one browser feature the scheme needs; the page must refuse rather than send plaintext.
    // The page is shared with the scenarios that follow, so what is removed is put back afterwards.
    await bob.eval('window.__savedScriptTransform = window.RTCRtpScriptTransform; window.RTCRtpScriptTransform = undefined; delete window.RTCRtpScriptTransform; true');
    try {
      await pages[0].eval('document.getElementById("groupbtn").click(); true');
      await bob.until('!!call', 'bob is invited', 20000);
      await bob.eval('document.getElementById("accept").click(); true');
      await sleep(3000);
      check('a browser that cannot encrypt does not join', await bob.eval('!call || !call.joined'));
      const rooms = await pages[0].eval('sfu("stats").then(s=>s.streams.map(x=>x.publisher))');
      check('and nothing from it reaches the server', !rooms.includes('bob'), JSON.stringify(rooms));
    } finally {
      await bob.eval('window.RTCRtpScriptTransform = window.__savedScriptTransform; true');
    }
  },


  async vp8_simulcast({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone }) {
    await setupRoom(pages, room('vp8'));
    await groupCall(pages, 'vp8');
    await everyoneSeesEveryone(pages, 'VP8 simulcast');
    const s = await stats(pages[0]);
    check('ada publishes three real simulcast layers (rids q,h,f)', s.outbound.map(o => o.rid).sort().join(',') === 'f,h,q', JSON.stringify(s.outbound.map(o => [o.rid, o.w])));
    check('every inbound video is VP8', s.inbound.every(i => /vp8/i.test(i.codec)), JSON.stringify(s.inbound.map(i => i.codec)));
  },


  async vp9_svc({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone }) {
    await setupRoom(pages, room('vp9'));
    await groupCall(pages, 'vp9');
    await everyoneSeesEveryone(pages, 'VP9 SVC');
    const s = await stats(pages[0]);
    check('ada publishes ONE VP9 stream with spatial layers (L3T3)', s.outbound.length === 1 && /vp9/i.test(s.outbound[0].codec) && /L3/.test(s.outbound[0].sm || ''), JSON.stringify(s.outbound));
    check('every inbound video is VP9', s.inbound.every(i => /vp9/i.test(i.codec)), JSON.stringify(s.inbound.map(i => i.codec)));
    const sizes = s.inbound.map(i => i.w);
    check('small tiles receive a lower spatial layer than the 1280-wide source', sizes.every(w => w > 0 && w < 1280), JSON.stringify(sizes));
  },


  async adapts_to_tiles_and_data_saver({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone, sleep }) {
    await setupRoom(pages, room('adapt'));
    await groupCall(pages, 'vp8');
    await everyoneSeesEveryone(pages, 'adaptation setup');
    const bob = pages[1];
    // Pin ada on bob's screen: a bigger tile should be sent a bigger layer.
    const before = await stats(bob);
    await bob.eval(`document.querySelector('#tiles figure[data-user="ada"]').click(); true`);
    await bob.until(`(async()=>{ const s = await sfu('stats'); const a = s.streams.find(x=>x.publisher==='ada'); return a && (a.layer==='h'||a.layer==='f'); })()`, 'pinned tile moves up a layer', 40000);
    const pinned = await stats(bob);
    const adaW = pinned.tiles.find(t => t.user === 'ada').w, cyW = pinned.tiles.find(t => t.user === 'cy').w;
    check('pinning a tile raises its quality above the others', adaW > cyW, `ada ${adaW}px, cy ${cyW}px`);
    check('both people are still on screen while one is favoured', pinned.tiles.length === 2 && pinned.tiles.every(t => t.w > 0));
    // Data saver: everyone drops to the smallest layer and nobody disappears.
    await bob.eval(`document.getElementById('saver').click(); true`);
    await bob.until(`(async()=>{ const s = await sfu('stats'); return s.streams.length===2 && s.streams.every(x=>x.layer==='q'); })()`, 'data saver forces the smallest layer', 40000);
    const saved = await stats(bob);
    check('data saver keeps both people visible on the smallest layer', saved.sfu.streams.every(s => s.layer === 'q') && saved.tiles.length === 2, JSON.stringify(saved.sfu.streams.map(s => [s.publisher, s.layer])));
    check('data saver caps the video bandwidth', saved.sfu.usage_kbps <= 400, `${saved.sfu.usage_kbps} kbps`);
    await bob.eval(`document.getElementById('saver').click(); true`);
  },


  async survives_a_network_change({ pages, check, stats, setupRoom, groupCall, everyoneSeesEveryone, sleep }) {
    await setupRoom(pages, room('net'));
    await groupCall(pages, 'vp8');
    await everyoneSeesEveryone(pages, 'before the switch');
    const bob = pages[1];
    const pcBefore = await bob.eval(`window.__pc0 = call.pc; true`);
    const f0 = (await stats(bob)).inbound.map(i => i.frames);
    // What the page does when the network changes: restart ICE on the same connection.
    await bob.eval(`restartIce('browser test: simulated network change')`);
    await bob.until(`call && call.pc.iceConnectionState === 'connected' || call.pc.iceConnectionState === 'completed'`, 'ICE reconnected', 30000);
    await sleep(2500);
    const f1 = (await stats(bob)).inbound.map(i => i.frames);
    check('video keeps flowing after an ICE restart', f1.length === 2 && f1.every((f, i) => f > f0[i]), `${f0} -> ${f1}`);
    check('the same connection was kept (not a rejoin)', await bob.eval(`call.pc === window.__pc0`));
    // Now a full rejoin (what happens if the server forgot us).
    await bob.eval(`rejoinSfu()`);
    await everyoneSeesEveryone([pages[0], pages[1], pages[2]], 'after a rejoin');
    // The "online" event path.
    await pages[2].eval(`window.dispatchEvent(new Event('online')); true`);
    await sleep(3000);
    await everyoneSeesEveryone(pages, 'after an online event');
  },
};
