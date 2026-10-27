"use strict";
// ProcessGate runtime UI: a no-build client for the generated pipeline API.
// All values are inserted with textContent / attributes, never as HTML.
const $ = (s, r = document) => r.querySelector(s);
const state = { token: null, email: "", roles: [] };
try { Object.assign(state, JSON.parse(sessionStorage.getItem("pg") || "{}")); } catch (e) { /* ignore */ }

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else if (k === "value") el.value = v;
    else if (k === "checked") el.checked = !!v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) if (kid != null && kid !== false) el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  return el;
}
const title = s => String(s || "").replace(/[_-]+/g, " ").replace(/\b\w/g, c => c.toUpperCase());
const when = s => (s ? new Date(s).toLocaleString() : "");
function toast(msg) { const t = $("#toast"); t.textContent = msg; t.classList.add("on"); clearTimeout(toast.t); toast.t = setTimeout(() => t.classList.remove("on"), Math.min(2600 + msg.length * 40, 9000)); }
function save() { sessionStorage.setItem("pg", JSON.stringify(state)); }

async function api(method, path, body, raw) {
  const headers = {};
  if (state.token) headers.authorization = "Bearer " + state.token;
  let payload;
  if (raw) payload = raw; else if (body !== undefined) { headers["content-type"] = "application/json"; payload = JSON.stringify(body); }
  const res = await fetch(path, { method, headers, body: payload });
  let data = null;
  try { data = await res.json(); } catch (e) { /* empty */ }
  if (res.status === 401 && state.token) { state.token = null; save(); location.hash = "#/login"; }
  return { ok: res.ok, status: res.status, data, error: data && data.error };
}
const fail = r => toast((r.error && (r.error.message || r.error.code)) || "Request failed (" + r.status + ")");

function chrome() {
  const top = $("#top"); top.replaceChildren(...[
    h("a", { class: "brand", href: "#/" }, "ProcessGate"),
    state.token ? h("a", { href: "#/" }, "Pipelines") : null,
    state.token ? h("a", { href: "#/notifications" }, "Notifications") : null,
    state.roles.includes("admin") ? h("a", { href: "#/admin" }, "Admin") : null,
    h("span", { class: "sp" }),
    h("a", { href: "/docs" }, "API docs"),
    state.token ? h("span", { class: "mute" }, state.email) : null,
    state.token ? h("button", { onclick: () => { state.token = null; save(); location.hash = "#/login"; } }, "Sign out") : h("a", { href: "#/login" }, "Sign in")].filter(Boolean));
}

// ---- auth ---------------------------------------------------------------
function field(label, input, opts = {}) {
  return h("label", { class: "f" }, h("span", {}, label, opts.required ? h("b", { class: "req" }, " *") : null), input,
    opts.help ? h("div", { class: "help" }, opts.help) : null, opts.error ? h("div", { class: "err" }, opts.error) : null);
}
function loginPage() {
  const email = h("input", { type: "email", name: "email", autocomplete: "username", required: true });
  const password = h("input", { type: "password", name: "password", autocomplete: "current-password", required: true });
  const code = h("input", { name: "code", inputmode: "numeric", autocomplete: "one-time-code", placeholder: "only if you registered an authenticator" });
  const form = h("form", { class: "card", style: "max-width:420px;margin:40px auto", onsubmit: async e => {
    e.preventDefault();
    const body = { email: email.value, password: password.value };
    if (code.value) body.code = code.value;
    const r = await api("POST", "/api/auth/login", body);
    if (!r.ok) return fail(r);
    Object.assign(state, { token: r.data.token, email: r.data.user ? r.data.user.email : email.value, roles: r.data.roles || [] }); save();
    location.hash = "#/";
  } }, h("h1", {}, "Sign in"), field("Email", email), field("Password", password), field("Authenticator code", code),
    h("div", { class: "row" }, h("button", { class: "primary", type: "submit" }, "Sign in"), h("a", { href: "#/register" }, "Create an account")));
  return form;
}
function registerPage() {
  const email = h("input", { type: "email", required: true }), password = h("input", { type: "password", minlength: 12, required: true });
  const out = h("div");
  return h("form", { class: "card", style: "max-width:460px;margin:40px auto", onsubmit: async e => {
    e.preventDefault();
    const r = await api("POST", "/api/auth/register", { email: email.value, password: password.value });
    if (!r.ok) return fail(r);
    const uri = (r.data.uri || ""), secret = (uri.match(/secret=([^&]+)/) || [])[1] || "";
    const code = h("input", { inputmode: "numeric" });
    out.replaceChildren(h("p", {}, "Add this key to an authenticator app, then enter the 6-digit code."),
      h("p", {}, h("code", {}, secret)), field("Code", code),
      h("button", { class: "primary", type: "button", onclick: async () => {
        const c = await api("POST", "/api/auth/register/confirm", { email: email.value, password: password.value, code: code.value });
        if (!c.ok) return fail(c); toast("Account activated"); location.hash = "#/login";
      } }, "Confirm"));
  } }, h("h1", {}, "Create account"), field("Email", email), field("Password (12+ characters)", password),
    h("button", { class: "primary", type: "submit" }, "Register"), out);
}

// ---- pipelines ----------------------------------------------------------
async function pipelinesPage() {
  const names = (await api("GET", "/api/pipelines")).data;
  const list = Array.isArray(names) ? names : null;
  const box = h("div", { class: "grid" });
  const q = h("input", { placeholder: "Filter pipelines…", "aria-label": "Filter pipelines" });
  const draw = () => {
    const f = q.value.toLowerCase();
    box.replaceChildren(...(list || []).filter(n => (n.name + " " + (n.title || "")).toLowerCase().includes(f))
      .map(n => h("a", { class: "tile", href: "#/p/" + n.name }, h("b", {}, n.title || title(n.name)), h("span", { class: "mute" }, n.name))));
  };
  q.addEventListener("input", draw);
  if (!list) return h("div", { class: "card" }, "Could not list pipelines.");
  draw();
  return h("div", {}, h("h1", {}, "Pipelines"), h("p", { class: "mute" }, list.length + " available"), h("div", { class: "card" }, q), box);
}
async function pipelinePage(name) {
  const d = await api("GET", "/api/pipelines/" + name);
  if (!d.ok) return h("div", { class: "card" }, "Pipeline not found.");
  const p = d.data;
  const cases = await api("GET", "/api/pipelines/" + name + "/cases");
  const rows = Array.isArray(cases.data) ? cases.data : [];
  return h("div", {},
    h("h1", {}, p.title || title(name)), h("p", { class: "mute" }, p.description || ""),
    h("div", { class: "row", style: "margin-bottom:16px" }, h("button", { class: "primary", onclick: async () => {
      const r = await api("POST", "/api/pipelines/" + name + "/cases", {});
      if (!r.ok) return fail(r); location.hash = "#/c/" + name + "/" + r.data.case.id;
    } }, "Start a case")),
    h("div", { class: "card" }, h("h2", {}, "Stages"), h("div", { class: "steps" }, p.stages.map((s, i) => h("span", { class: "pill" }, (i + 1) + ". " + (s.title || title(s.name)))))),
    h("div", { class: "card" }, h("h2", {}, "Cases"), rows.length ? h("table", {}, h("thead", {}, h("tr", {}, ["Number", "Stage", "Status", "Updated", ""].map(x => h("th", {}, x)))),
      h("tbody", {}, rows.map(c => h("tr", {}, h("td", {}, c.number), h("td", {}, c.stage_title || title(c.stage)), h("td", {}, statusPill(c.status)), h("td", {}, when(c.updated_at)),
        h("td", {}, h("a", { href: "#/c/" + name + "/" + c.id }, "Open")))))) : h("p", { class: "mute" }, "No cases yet.")));
}
function statusPill(s) { return h("span", { class: "pill " + ({ completed: "ok", approved: "ok", active: "cur", in_progress: "cur", rejected: "bad", suspended: "warn" }[s] || "") }, title(s)); }

// ---- case ---------------------------------------------------------------
function control(inp, ro, errors, uploads) {
  const id = "f_" + inp.path.replace(/\W/g, "_");
  const err = errors[inp.path];
  const val = inp.value == null ? "" : inp.value;
  let el;
  const common = { id, name: inp.path, disabled: ro, required: false, placeholder: inp.placeholder };
  const opts = inp.choices && inp.choices.length ? inp.choices.map(c => [c.value, c.label || c.value]) : (inp.options || []).map(o => [o, title(o)]);
  switch (inp.kind) {
    case "textarea": el = h("textarea", { ...common, rows: 3 }, String(val)); break;
    case "select": case "radio":
      el = h("select", common, h("option", { value: "" }, "— select —"), opts.map(([v, l]) => h("option", { value: v, selected: String(val) === String(v) }, l))); break;
    case "boolean": case "checkbox": case "toggle":
      el = h("input", { ...common, type: "checkbox", checked: val === true || val === "true" }); break;
    case "number": case "integer": case "currency": el = h("input", { ...common, type: "number", step: "any", min: inp.min, max: inp.max, value: val }); break;
    case "date": el = h("input", { ...common, type: "date", value: String(val).slice(0, 10) }); break;
    case "datetime": el = h("input", { ...common, type: "datetime-local", value: String(val).slice(0, 16) }); break;
    case "email": el = h("input", { ...common, type: "email", value: val }); break;
    case "file": {
      const have = Array.isArray(val) ? val : (val ? [val] : []);
      el = h("div", {}, have.length ? h("div", { class: "help" }, "Uploaded: " + have.map(f => f.filename || f.name || "file").join(", ")) : null,
        ro ? null : h("input", { type: "file", id, accept: (inp.accept || []).join(","), multiple: inp.max_files > 1, onchange: e => { uploads[inp.path] = e.target.files; } }));
      break;
    }
    default: el = h("input", { ...common, type: inp.masked ? "password" : "text", value: val, pattern: inp.pattern, minlength: inp.min_length, maxlength: inp.max_length });
  }
  el.dataset.path = inp.path; el.dataset.kind = inp.kind;
  return h("label", { class: "f", style: inp.span ? "grid-column:span " + Math.min(inp.span, 3) : null }, h("span", {}, inp.label || title(inp.name), inp.required ? h("b", { class: "req" }, " *") : null),
    el, inp.help ? h("div", { class: "help" }, inp.help) : null, inp.flag ? h("div", { class: "err" }, "Correction requested: " + (inp.flag.comment || "")) : null, err ? h("div", { class: "err" }, err) : null);
}
function collect(root) {
  const data = {};
  root.querySelectorAll("[data-path]").forEach(el => {
    if (el.disabled || el.dataset.kind === "file") return;
    const [f, ...rest] = el.dataset.path.split("."); const k = rest.join(".");
    data[f] = data[f] || {};
    let v = el.type === "checkbox" ? el.checked : el.value;
    if ((el.type === "number") && v !== "") v = Number(v);
    if (v === "" && el.type !== "checkbox") return;
    data[f][k] = v;
  });
  return data;
}
async function casePage(name, id, stageArg) {
  const base = "/api/pipelines/" + name + "/cases/" + id;
  const c = await api("GET", base);
  if (!c.ok) return h("div", { class: "card" }, "Case not found.");
  const cur = (c.data.case || c.data).stage;
  const stage = stageArg || cur;
  const v = await api("GET", base + "/stages/" + stage);
  if (!v.ok) {
    const d = await api("GET", "/api/pipelines/" + name);
    const st = d.ok ? d.data.stages.find(x => x.name === stage) : null;
    const cs0 = c.data.case || c.data;
    return h("div", {}, h("p", {}, h("a", { href: "#/p/" + name }, "← " + title(name))), h("h1", {}, (cs0.title || title(name)) + " · " + cs0.number),
      h("div", { class: "row", style: "margin-bottom:12px" }, statusPill(cs0.status), h("span", { class: "mute" }, "Current stage: " + title(cur))),
      d.ok ? h("div", { class: "steps" }, d.data.stages.map(s => h("a", { class: "pill " + (s.name === cur ? "cur" : ""), href: "#/c/" + name + "/" + id + "/" + s.name }, s.title || title(s.name)))) : null,
      h("div", { class: "card" }, h("h2", {}, (st && st.title) || title(stage)),
        h("p", {}, (v.error && v.error.message) || "You cannot open this stage."),
        st && st.roles && st.roles.length ? h("p", { class: "mute" }, "Handled by: " + st.roles.join(", ")) : null),
      h("div", { class: "card" }, h("h2", {}, "History"), (c.data.history || []).slice(-12).reverse().map(x => h("div", { class: "help" }, when(x.at) + " · " + title(x.action) + (x.stage ? " · " + x.stage : "")))));
  }
  const view = v.data, errors = {}, uploads = {};
  const reload = () => route();
  const formRoot = h("div");
  const paintErrors = details => { Object.keys(errors).forEach(k => delete errors[k]); (details || []).forEach(d => { errors[d.path] = d.message; }); };
  const groups = (view.page && view.page.groups) || [];
  let tab = 0;
  const drawForm = () => {
    const keep = collect(formRoot);
    formRoot.replaceChildren();
    const show = (view.page && view.page.layout === "wizard" || view.page && view.page.layout === "tabbed") && groups.length > 1 ? [groups[tab]] : groups;
    if (show.length !== groups.length) formRoot.append(h("div", { class: "tabs" }, groups.map((g, i) => h("button", { type: "button", class: i === tab ? "on" : "", onclick: () => { tab = i; drawForm(); } }, g.title || title(g.name)))));
    for (const g of show) {
      formRoot.append(h("section", { class: "card" }, groups.length > 1 && show.length === groups.length ? h("h2", {}, g.title || title(g.name)) : null,
        g.forms.map(f => h("div", {}, h("h2", {}, f.title || title(f.name)),
          f.repeatable ? h("p", { class: "mute" }, (f.entries || []).length + " entries (repeatable form; edit through the API)") : null,
          h("div", { class: "cols" }, f.inputs.map(i => control(i, !i.editable || g.mode !== "editable" || !view.can_edit, errors, uploads)))))));
    }
    void keep;
  };
  drawForm();
  const submit = async (action, extra) => {
    const data = collect(formRoot);
    const comment = $("#comment") ? $("#comment").value : "";
    for (const [path, files] of Object.entries(uploads)) for (const file of files) {
      const fd = new FormData(); fd.append("file", file);
      const up = await api("POST", base + "/stages/" + stage + "/files/" + path, undefined, fd);
      if (!up.ok) return fail(up);
    }
    const r = action ? await api("POST", base + "/stages/" + stage + "/actions/" + action, { data, comment, ...extra })
      : await api("PUT", base + "/stages/" + stage, { data });
    if (!r.ok) { paintErrors(r.error && r.error.details); drawForm(); return fail(r); }
    toast(action ? "Done" : "Draft saved"); reload();
  };
  const work = view.work || {}, sla = work.sla;
  const toIn = h("input", { placeholder: "user id or reason", "aria-label": "assign to or reason" });
  const workBtns = (work.operations || []).filter(o => !o.startsWith("note")).map(op => h("button", { onclick: async () => {
    const r = await api("POST", base + "/stages/" + stage + "/work/" + op, { to: toIn.value, reason: toIn.value, comment: toIn.value });
    r.ok ? (toast(title(op) + " ok"), reload()) : fail(r);
  } }, title(op)));
  const noteBody = h("textarea", { rows: 2, placeholder: "Add a note" });
  const notes = view.notes || [];
  const left = h("div", {},
    h("div", { class: "steps" }, (view.progress || []).map(s => h("a", { class: "pill " + (s.name === cur ? "cur" : s.status === "completed" ? "ok" : ""), href: "#/c/" + name + "/" + id + "/" + s.name }, s.title || title(s.name)))),
    (view.corrections || []).length ? h("div", { class: "card err" }, "Corrections requested on " + view.corrections.length + " field(s).") : null,
    formRoot,
    (view.nodes || []).map(n => {
      const verdicts = {};
      const rows = n.kind === "review" ? groups.flatMap(g => g.forms).filter(f => (n.forms || []).includes(f.name)).flatMap(f => f.inputs.filter(i => i.value !== undefined && i.value !== null && i.value !== "").map(i => {
        const st = h("select", { "aria-label": "Verdict for " + i.label }, h("option", { value: "" }, "— not reviewed —"),
          h("option", { value: "verified", selected: i.verdict && i.verdict.status === "verified" }, "Verified"),
          h("option", { value: "flagged", selected: i.verdict && i.verdict.status === "flagged" }, "Flagged"));
        const cm = h("input", { placeholder: "comment (needed when flagged)", value: i.verdict && i.verdict.comment || "" });
        verdicts[i.path] = { st, cm };
        const shown = i.kind === "file" ? (Array.isArray(i.value) ? i.value.length + " file(s)" : i.value ? "1 file" : "") : (i.value === true ? "Yes" : i.value === false ? "No" : i.value);
        return h("tr", {}, h("td", {}, i.label || title(i.name)), h("td", { class: "mute" }, shown == null ? "" : String(shown)), h("td", {}, st), h("td", {}, cm));
      })) : [];
      const payload = extra => { const v = {}; for (const [p, x] of Object.entries(verdicts)) if (x.st.value) v[p] = { status: x.st.value, comment: x.cm.value }; return { comment: ($("#comment") || {}).value || "", verdicts: v, ...extra }; };
      return h("section", { class: "card" }, h("h2", {}, n.title || title(n.name)), h("div", { class: "row" }, statusPill(n.status), h("span", { class: "mute" }, (n.approvals || []).length + "/" + (n.required_approvals || 1) + " approvals")),
        n.description ? h("p", { class: "mute" }, n.description) : null,
        rows.length ? h("table", { style: "margin-top:8px" }, h("thead", {}, h("tr", {}, ["Field", "Value", "Verdict", "Comment"].map(x => h("th", {}, x)))), h("tbody", {}, rows)) : null,
        h("div", { class: "row", style: "margin-top:8px" }, rows.length ? h("button", { type: "button", onclick: () => Object.values(verdicts).forEach(x => { x.st.value = "verified"; }) }, "Mark all verified") : null,
          (n.operations || []).map(op => h("button", { class: op.startsWith("rej") ? "danger" : "", onclick: async () => {
            const r = await api("POST", base + "/stages/" + stage + "/nodes/" + n.name + "/" + op, payload());
            r.ok ? (toast(title(op) + " recorded"), reload()) : fail(r);
          } }, title(op)))));
    }),
    view.can_edit || (view.actions || []).length ? h("section", { class: "card" }, field("Comment (optional)", h("textarea", { id: "comment", rows: 2 })),
      h("div", { class: "row" }, view.can_edit ? h("button", { onclick: () => submit(null) }, "Save draft") : null,
        (view.actions || []).map(a => h("button", { class: a.outcome === "advance" ? "primary" : "", onclick: () => {
          if (a.comment_required && !$("#comment").value) return toast("A comment is required");
          submit(a.name);
        } }, a.label || title(a.name))))) : null);
  const right = h("div", {},
    sla ? h("section", { class: "card sla" }, h("h2", {}, "SLA"), h("div", {}, h("span", {}, "Status"), h("span", { class: "pill " + (sla.status === "breached" ? "bad" : sla.status === "warning" ? "warn" : "ok") }, title(sla.status))),
      h("div", {}, h("span", {}, "Due"), h("span", {}, when(sla.due_at))), sla.pickup_due_at ? h("div", {}, h("span", {}, "Pickup by"), h("span", {}, when(sla.pickup_due_at) + (sla.pickup_breached ? " (missed)" : ""))) : null,
      sla.breached_at ? h("div", {}, h("span", {}, "Breached"), h("span", {}, when(sla.breached_at))) : null, sla.level ? h("div", {}, h("span", {}, "Escalation level"), h("span", {}, sla.level)) : null) : null,
    h("section", { class: "card" }, h("h2", {}, "Work"), h("p", { class: "mute" }, work.assignee ? "Assigned to " + work.assignee : "Unassigned"),
      workBtns.length ? [toIn, h("div", { class: "row", style: "margin-top:8px" }, workBtns)] : h("p", { class: "mute" }, "No actions available to you.")),
    h("section", { class: "card" }, h("h2", {}, "Notes"), notes.map(n => h("p", {}, h("b", {}, n.author || n.by || ""), " ", n.body)),
      (work.operations || []).includes("note") ? [noteBody, h("button", { style: "margin-top:6px", onclick: async () => {
        const r = await api("POST", base + "/stages/" + stage + "/work/note", { body: noteBody.value }); r.ok ? reload() : fail(r);
      } }, "Add note")] : null),
    h("section", { class: "card" }, h("h2", {}, "History"), (c.data.history || []).slice(-12).reverse().map(x => h("div", { class: "help" }, when(x.at) + " · " + title(x.action) + " · " + (x.actor || "").slice(0, 14) + (x.stage ? " · " + x.stage : "")))));
  const cs = view.case || c.data.case || c.data;
  return h("div", {}, h("p", {}, h("a", { href: "#/p/" + name }, "← " + title(name))), h("h1", {}, (cs.title || title(name)) + " · " + cs.number), h("div", { class: "row", style: "margin-bottom:12px" }, statusPill(cs.status), h("span", { class: "mute" }, "Stage: " + (view.stage && view.stage.title || title(stage)))), h("div", { class: "split" }, left, right));
}

// ---- misc ---------------------------------------------------------------
async function notificationsPage() {
  const r = await api("GET", "/api/notifications"); const rows = Array.isArray(r.data) ? r.data : [];
  return h("div", {}, h("h1", {}, "Notifications"), h("div", { class: "card" }, rows.length ? h("table", {}, h("tbody", {}, rows.map(m => h("tr", {}, h("td", {}, when(m.created_at)), h("td", {}, h("b", {}, m.subject)), h("td", { class: "mute" }, m.channel))))) : h("p", { class: "mute" }, "Nothing yet.")));
}
function invitePage(token) {
  const name = h("input"), password = h("input", { type: "password", minlength: 12 });
  return h("form", { class: "card", style: "max-width:420px;margin:40px auto", onsubmit: async e => {
    e.preventDefault();
    const r = await api("POST", "/api/auth/invitations/accept", { token, name: name.value, password: password.value });
    if (!r.ok) return fail(r); toast("Welcome, you can sign in now"); location.hash = "#/login";
  } }, h("h1", {}, "Accept invitation"), field("Your name", name), field("Choose a password (12+ characters)", password), h("button", { class: "primary", type: "submit" }, "Join"));
}
function adminPage() {
  const out = h("pre", { class: "mute" });
  const email = h("input", { type: "email", required: true }), roles = h("input", { placeholder: "comma-separated pipeline roles, e.g. registration_officer" });
  const inv = h("div");
  return h("div", {}, h("h1", {}, "Administration"),
    h("form", { class: "card", onsubmit: async e => {
      e.preventDefault();
      const r = await api("POST", "/api/auth/invite", { email: email.value, roles: roles.value.split(",").map(x => x.trim()).filter(Boolean) });
      if (!r.ok) return fail(r);
      const link = location.origin + "/app/#/invite/" + encodeURIComponent(r.data.token);
      inv.replaceChildren(h("p", {}, "Invitation sent to " + r.data.email + ". Share this link:"), h("p", {}, h("a", { href: link }, link)));
    } }, h("h2", {}, "Invite a user"), field("Email", email), field("Roles", roles), h("button", { class: "primary", type: "submit" }, "Invite"), inv),
    h("div", { class: "card" }, h("h2", {}, "SLA sweep"),
    h("p", { class: "mute" }, "Applies warnings, pickup timeouts, breach actions and escalations to all open cases. It also runs every minute."),
    h("button", { class: "primary", onclick: async () => { const r = await api("POST", "/api/admin/sla/sweep", {}); out.textContent = JSON.stringify(r.data, null, 2); r.ok || fail(r); } }, "Run sweep now"), out));
}

async function route() {
  chrome();
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  const main = $("#main");
  if (!state.token && parts[0] === "invite") { main.replaceChildren(invitePage(parts[1])); return; }
  if (!state.token && parts[0] !== "register") { if (parts[0] !== "login") { location.hash = "#/login"; return; } main.replaceChildren(loginPage()); return; }
  let view;
  try {
    if (!parts.length) view = await pipelinesPage();
    else if (parts[0] === "login") view = loginPage();
    else if (parts[0] === "register") view = registerPage();
    else if (parts[0] === "invite") view = invitePage(parts[1]);
    else if (parts[0] === "p") view = await pipelinePage(parts[1]);
    else if (parts[0] === "c") view = await casePage(parts[1], parts[2], parts[3]);
    else if (parts[0] === "notifications") view = await notificationsPage();
    else if (parts[0] === "admin") view = adminPage();
    else view = h("div", { class: "card" }, "Page not found.");
  } catch (e) { view = h("div", { class: "card err" }, "Something went wrong: " + e.message); }
  main.replaceChildren(view);
}
addEventListener("hashchange", route);
route();
