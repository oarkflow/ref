// Registers resources/static/sw.js. Kept as its own file, not inlined in a
// template, so it stays possible to update this one JS file without a
// template re-render, and so it works identically across every layout
// without duplicating the same script tag in each of them (base, auth,
// error — see resources/templates/layouts/).
//
// See sw.js's own doc comment: this worker deliberately does not cache
// anything, so registering it here has no effect on page freshness — it
// exists to be real and controlling, not to be idle. Registration itself
// is best-effort: an unsupported browser or a blocked script simply never
// calls this, and the page works exactly the same either way.
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
}
