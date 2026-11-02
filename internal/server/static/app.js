"use strict";
// Photo Sorter UI: one small single-page app shared by Windows and Docker.

// ---------- helpers ----------
const $main = document.getElementById("main");
let INFO = { native: false, canOpen: false, canQuit: false, roots: [] };
let pollTimer = null;

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "style") el.style.cssText = v;
    else if (k === "html") el.innerHTML = v;
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

async function api(method, url, body) {
  const opt = { method, headers: { "X-PhotoSorter": "1" } };
  if (body !== undefined) { opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
  const r = await fetch(url, opt);
  let data = null;
  try { data = await r.json(); } catch { /* empty */ }
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
}

const num = n => Number(n || 0).toLocaleString();
function bytes(n) {
  n = Number(n || 0);
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 1 : 0)) + " " + u[i];
}
const plural = (n, one, many) => num(n) + " " + (n === 1 ? one : (many || one + "s"));
// Destination paths arrive '/'-separated; show them the local way.
const disp = p => (INFO.platform === "windows" ? String(p || "").replace(/\//g, "\\") : String(p || ""));
const pathBase = p => String(p || "").split("/").pop();

function toast(msg, ms = 3500) {
  const t = document.getElementById("toast");
  t.textContent = msg; t.hidden = false;
  clearTimeout(t._t); t._t = setTimeout(() => (t.hidden = true), ms);
}

// Pages that load data first check stillHere() before drawing, so a slow
// page can't draw over the one you've moved on to.
let navSeq = 0;
function stillHere() { const s = navSeq; return () => s === navSeq; }

function setPage(nav, ...kids) {
  clearInterval(pollTimer); pollTimer = null;
  document.querySelectorAll(".nav a").forEach(a => a.classList.toggle("on", a.dataset.nav === nav));
  $main.replaceChildren(h("div", { class: "page" }, ...kids));
  $main.scrollTop = 0;
}

function errBox(e) { return h("div", { class: "error" }, e.message || String(e)); }

// ---------- modal ----------
function modal(title, body, buttons) {
  return new Promise(resolve => {
    const root = document.getElementById("modal-root");
    const close = v => { root.replaceChildren(); resolve(v); };
    const box = h("div", { class: "modal", role: "dialog", "aria-modal": "true" },
      h("h3", {}, title), body,
      h("div", { class: "row", style: "justify-content:flex-end" },
        buttons.map(b => h("button", { class: b.cls || "", onclick: () => close(b.value) }, b.label))));
    root.replaceChildren(h("div", { class: "overlay", onclick: e => { if (e.target.classList.contains("overlay")) close(null); } }, box));
  });
}

function confirmBox(title, text, okLabel, danger) {
  return modal(title, h("div", {}, text), [
    { label: "Cancel", value: false },
    { label: okLabel, value: true, cls: danger ? "danger" : "primary" },
  ]);
}

// pickFolder uses the Windows dialog when available, else the built-in browser.
async function pickFolder(start) {
  if (INFO.native) {
    try { const r = await api("POST", "/api/pick", { start: start || "" }); return r.path || null; }
    catch (e) { toast(e.message); return null; }
  }
  return browseFolder(start);
}

function browseFolder(start) {
  return new Promise(resolve => {
    let current = start || (INFO.roots.length === 1 ? INFO.roots[0] : "");
    const list = h("div", { class: "browser" });
    const path = h("input", { type: "text", style: "flex:1", placeholder: "/camera-photo" });
    const err = h("div", { class: "muted", style: "font-size:13px;min-height:18px" });
    async function load(p) {
      try {
        const r = await api("GET", "/api/browse?path=" + encodeURIComponent(p || ""));
        current = r.path; path.value = r.path; err.textContent = "";
        const rows = [];
        if (r.path) rows.push(h("div", { onclick: () => load(r.parent) }, "⬑  Up one level"));
        for (const d of r.dirs) rows.push(h("div", { onclick: () => load(d) }, "📁  " + (r.path ? d.split(/[\\/]/).pop() : d)));
        if (!r.dirs.length) rows.push(h("div", { class: "faint", style: "cursor:default" }, "No subfolders"));
        list.replaceChildren(...rows);
      } catch (e) { err.textContent = e.message; }
    }
    path.addEventListener("keydown", e => { if (e.key === "Enter") load(path.value); });
    const body = h("div", { class: "stack" }, h("div", { class: "row" }, path, h("button", { onclick: () => load(path.value) }, "Go")), list, err);
    modal("Choose a folder", body, [{ label: "Cancel", value: null }, { label: "Use this folder", value: "use", cls: "primary" }])
      .then(v => resolve(v === "use" && current ? current : null));
    load(current);
  });
}

// ---------- virtual table ----------
// Renders only the visible rows, so tens of thousands of files stay smooth.
// Columns start just wide enough for their longest value. Like a spreadsheet,
// drag a header's right edge to resize it (double-click the edge to fit it
// again) and drag a header to move the column. With an id, the order and the
// widths you set are remembered.
const measureCtx = document.createElement("canvas").getContext("2d");
function textWidth(s, font) { measureCtx.font = font; return measureCtx.measureText(s).width; }

function vtable({ id = null, columns, rows = [], height = 460, select = null, onRow = null, empty = "Nothing here", foot = null }) {
  const rowH = 30, headH = 34, gap = 10, padX = 10, selW = 26;
  const key = c => c.key || c.label;
  const byKey = new Map(columns.map(c => [key(c), c]));
  const saved = (() => { try { return (id && JSON.parse(localStorage.getItem("vt:" + id))) || {}; } catch { return {}; } })();
  let order = columns.map(key);
  if (Array.isArray(saved.order)) {
    const known = saved.order.filter(k => byKey.has(k));
    order = known.concat(order.filter(k => !known.includes(k)));
  }
  const manual = Object.assign({}, saved.w); // widths the user dragged
  const widths = {};
  let data = rows, sortKey = null, sortDir = 1, lastTop = -1;
  const head = h("div", { class: "vt-head" });
  const marker = h("div", { class: "vt-marker", hidden: true });
  const inner = h("div", { class: "vt-rows" });
  const canvas = h("div", { class: "vt-canvas" }, inner);
  const body = h("div", { class: "vt-body", style: `height:${height}px` }, head, canvas);
  const footEl = h("div", { class: "vt-foot" });
  const el = h("div", { class: "vt" }, body, footEl);
  let all = null;

  function save() {
    if (!id) return;
    try { localStorage.setItem("vt:" + id, JSON.stringify({ order, w: manual })); } catch { /* not kept */ }
  }
  function cellText(c, r) {
    if (c.text) return c.text(r);
    const v = c.render(r);
    return v == null ? "" : typeof v === "object" ? v.textContent || "" : String(v);
  }
  // fit sizes columns to their longest value (the longest few hundred by
  // length are measured, which is plenty and keeps big lists quick).
  function fit(only) {
    const fam = getComputedStyle(document.body).fontFamily;
    for (const c of columns) {
      const k = key(c);
      if (only != null ? only !== k : manual[k]) { if (manual[k]) widths[k] = manual[k]; continue; }
      let w = textWidth(c.label, `12.5px ${fam}`) + 18; // room for the sort arrow
      const font = `${c.strong ? 600 : 400} 13px ${fam}`;
      const texts = data.map(r => cellText(c, r)).sort((a, b) => b.length - a.length);
      for (const t of texts.slice(0, 300)) w = Math.max(w, textWidth(t, font));
      widths[k] = Math.min(Math.ceil(w) + 28, 1400); // + breathing room
    }
  }
  function layout() {
    const ws = order.map(k => widths[k]);
    const tracks = ws.length + 1 + (select ? 1 : 0); // + a filler that takes the spare room
    const tw = 2 * padX + (select ? selW : 0) + ws.reduce((a, b) => a + b, 0) + gap * (tracks - 1);
    el.style.setProperty("--cols", (select ? selW + "px " : "") + ws.map(w => w + "px").join(" ") + " minmax(0,1fr)");
    el.style.setProperty("--tw", tw + "px");
  }

  function drawHead() {
    const cells = [];
    if (select) {
      all = h("input", { type: "checkbox", "aria-label": "Select all shown", onchange: () => {
        for (const r of data) all.checked ? select.set.add(select.key(r)) : select.set.delete(select.key(r));
        select.onchange && select.onchange(); draw();
      } });
      cells.push(h("div", { class: "sel" }, all));
    }
    for (const k of order) {
      const c = byKey.get(k);
      const cell = h("div", { class: "h" + (c.right ? " r" : "") + (k === order[order.length - 1] ? " last" : ""), title: (c.sort ? "Click to sort · " : "") + "drag to move" },
        h("span", { class: "lbl" }, c.label + (sortKey === k ? (sortDir > 0 ? " ▲" : " ▼") : "")),
        h("span", { class: "grip", title: "Drag to resize · double-click to fit",
          onpointerdown: e => resize(e, k),
          ondblclick: e => { e.stopPropagation(); delete manual[k]; fit(k); layout(); save(); } }));
      cell.addEventListener("pointerdown", e => move(e, k, cell));
      cells.push(cell);
    }
    cells.push(h("div"), marker);
    head.replaceChildren(...cells);
    syncAll();
  }
  function resize(e, k) {
    if (e.button !== 0) return;
    e.stopPropagation(); e.preventDefault();
    const x0 = e.clientX, w0 = widths[k];
    el.classList.add("resizing");
    const onMove = ev => { widths[k] = manual[k] = Math.max(36, Math.round(w0 + ev.clientX - x0)); layout(); };
    const onUp = () => {
      window.removeEventListener("pointermove", onMove); window.removeEventListener("pointerup", onUp);
      el.classList.remove("resizing"); save();
    };
    window.addEventListener("pointermove", onMove); window.addEventListener("pointerup", onUp);
  }
  // move: a click sorts; a drag moves the column to where the marker shows.
  function move(e, k, cell) {
    if (e.button !== 0) return;
    const x0 = e.clientX, hr = head.getBoundingClientRect();
    const rects = [...head.querySelectorAll(".h")].map(x => x.getBoundingClientRect());
    let dragging = false, to = null;
    const onMove = ev => {
      const dx = ev.clientX - x0;
      if (!dragging && Math.abs(dx) < 5) return;
      dragging = true;
      cell.classList.add("dragging"); cell.style.transform = `translateX(${dx}px)`;
      to = rects.findIndex(b => ev.clientX < b.left + b.width / 2);
      if (to < 0) to = rects.length;
      const x = to < rects.length ? rects[to].left - gap / 2 : rects[rects.length - 1].right + gap / 2;
      marker.style.left = x - hr.left - 1 + "px"; marker.hidden = false;
    };
    const onUp = () => {
      window.removeEventListener("pointermove", onMove); window.removeEventListener("pointerup", onUp);
      marker.hidden = true;
      if (!dragging) {
        const c = byKey.get(k);
        if (c.sort) { sortDir = sortKey === k ? -sortDir : 1; sortKey = k; applySort(); draw(); drawHead(); }
        return;
      }
      const from = order.indexOf(k);
      order.splice(from, 1); order.splice(to > from ? to - 1 : to, 0, k);
      save(); layout(); drawHead(); draw();
    };
    window.addEventListener("pointermove", onMove); window.addEventListener("pointerup", onUp);
  }
  function syncAll() {
    if (!all) return;
    const n = data.filter(r => select.set.has(select.key(r))).length;
    all.checked = data.length > 0 && n === data.length;
    all.indeterminate = n > 0 && n < data.length;
  }
  function applySort() {
    if (sortKey == null) return;
    const f = byKey.get(sortKey).sort;
    data = [...data].sort((a, b) => { const x = f(a), y = f(b); return (x < y ? -1 : x > y ? 1 : 0) * sortDir; });
  }
  function draw() {
    lastTop = body.scrollTop;
    canvas.style.height = (data.length ? data.length * rowH : 70) + "px";
    if (!data.length) { inner.replaceChildren(h("div", { class: "vt-empty" }, empty)); inner.style.transform = ""; footEl.textContent = foot ? foot(data) : ""; syncAll(); return; }
    const first = Math.max(0, Math.floor(Math.max(0, body.scrollTop - headH) / rowH) - 8);
    const last = Math.min(data.length, first + Math.ceil(height / rowH) + 16);
    const out = [];
    for (let i = first; i < last; i++) {
      const r = data[i];
      const cells = [];
      if (select) {
        const k = select.key(r);
        cells.push(h("div", {}, h("input", { type: "checkbox", checked: select.set.has(k), "aria-label": "Select",
          onclick: e => e.stopPropagation(),
          onchange: e => { e.target.checked ? select.set.add(k) : select.set.delete(k); syncAll(); select.onchange && select.onchange(); } })));
      }
      for (const k of order) {
        const c = byKey.get(k);
        const v = c.render(r);
        const tip = c.title ? c.title(r) : typeof v === "string" ? v : c.text ? c.text(r) : null;
        // tail: if the column is narrowed, cut the text at the start so the
        // end (e.g. "_1.JPG") stays visible.
        cells.push(h("div", { class: (c.right ? "r" : "") + (c.tail ? " tail" : ""), title: tip }, c.tail ? ["\u200E", v, "\u200E"] : v));
      }
      cells.push(h("div"));
      out.push(h("div", { class: "vt-row" + (onRow ? " click" : ""), onclick: onRow ? () => onRow(r) : null }, cells));
    }
    inner.style.transform = `translateY(${first * rowH}px)`;
    inner.replaceChildren(...out);
    footEl.textContent = foot ? foot(data) : num(data.length) + " rows";
    syncAll();
  }
  body.addEventListener("scroll", () => { if (body.scrollTop !== lastTop) requestAnimationFrame(draw); });
  fit(); layout(); drawHead(); draw();
  return { el, setRows(r) { data = r; applySort(); fit(); layout(); body.scrollTop = 0; draw(); }, redraw: draw };
}

// ---------- shared bits ----------
const modeExplain = {
  copy: h("div", { class: "d" }, "Writes a second, separate copy of each file into the archive and checks it byte-for-byte. The originals stay where they are, so they take up space twice until you delete them. They'll show on the Leftovers page as “Already in archive”."),
  move: () => h("div", { class: "dd" },
    h("b", {}, "Same disk"), h("span", { class: "muted" }, "e.g. a to-sort folder and the archive in the same share. The file is renamed into place: instant, and its data is never rewritten."),
    h("b", {}, "Different disk"), h("span", { class: "muted" }, "e.g. an SD card or another drive. Each file is copied, checked byte-for-byte, and only then deleted from the source. If the check fails, the original stays and shows under Failed.")),
};

// optionsForm edits {move, jpg, raw, mov, rename, sidecars} in place.
function optionsForm(o) {
  const radio = (move, title) => {
    const box = h("div", { class: "opt" + (o.move === move ? " on" : "") },
      h("label", { class: "t" }, h("input", { type: "radio", name: "mode", checked: o.move === move, onchange: () => { o.move = move; redraw(); } }), " ", title),
      move ? modeExplain.move() : modeExplain.copy.cloneNode(true));
    return box;
  };
  const wrap = h("div");
  function redraw() {
    wrap.replaceChildren(
      radio(false, "Copy — keep the originals"),
      radio(true, "Move — files end up only in the archive"),
      h("div", { class: "faint", style: "font-size:12.5px;margin-top:6px" }, "Neither creates hardlinks: a copy is always a real, independent copy."));
  }
  redraw();
  const cb = (key, label) => h("label", {}, h("input", { type: "checkbox", checked: !!o[key], onchange: e => (o[key] = e.target.checked) }), " ", label);
  return [
    h("div", { class: "lbl" }, "When sorting"), wrap,
    h("div", { class: "lbl" }, "Include"), h("div", { class: "row", style: "padding-top:6px;gap:18px" }, cb("jpg", "JPG (JPEG, HEIC)"), cb("raw", "RAW"), cb("mov", "MOV (all videos)")),
    h("div", { class: "lbl" }, "File names"), h("div", { style: "padding-top:6px" }, cb("rename", "Add the capture date-time"), " ", h("span", { class: "mono faint" }, "FUJI0001-20261003-101500.RAF")),
    h("div", { class: "lbl" }, "Sidecars"), h("div", { style: "padding-top:6px" }, cb("sidecars", "Bring .xmp / .pp3 / .dop along, next to their photo (RAW first)")),
    h("div", { class: "lbl" }, "Speed"), h("div", {},
      h("select", { onchange: e => (o.parallel = Number(e.target.value)) },
        [["0", "Auto (recommended)"], ["1", "One file at a time"], ["2", "2 files at once"], ["4", "4 files at once"], ["8", "8 files at once"]]
          .map(([v, l]) => h("option", { value: v, selected: String(o.parallel || 0) === v }, l))),
      h("div", { class: "faint", style: "font-size:12.5px;margin-top:4px" }, "Auto copies several files at once from SSDs, SD cards and network drives, and one at a time when a spinning hard disk is involved, where parallel copies would slow it down.")),
    h("div", { class: "lbl" }, "Check"), h("div", {},
      h("select", { onchange: e => (o.quickCheck = e.target.value === "quick") },
        [["disk", "Read back from the disk (recommended)"], ["quick", "Quick check (faster)"]]
          .map(([v, l]) => h("option", { value: v, selected: (o.quickCheck ? "quick" : "disk") === v }, l))),
      h("div", { class: "faint", style: "font-size:12.5px;margin-top:4px" }, "Each copy is saved to the disk, then read back from the disk itself and compared with the original, byte for byte. This catches a copy the drive stored wrongly, not only a bad read. It adds about a quarter to the time on SSDs and SD cards, and up to double on a spinning hard disk."),
      h("div", { class: "faint", style: "font-size:12.5px;margin-top:4px" }, "Quick check compares the copy as Windows or Linux still holds it in memory: it catches read and copy errors, but not a drive that stores the data wrongly."),
      h("div", { class: "faint", style: "font-size:12.5px;margin-top:4px" }, "A move within the same disk is a rename, so no data is rewritten and there's nothing to check.")),
  ];
}

// settingsLine sums up a profile's or a job's options in one line. A job
// passes the check its summary recorded; jobs from before the Check setting
// recorded none, so it's left out.
function settingsLine(o, check = o.quickCheck ? "quick" : "disk") {
  return [o.move ? "Move" : "Copy", [o.jpg && "JPG", o.raw && "RAW", o.mov && "MOV"].filter(Boolean).join(", "),
    o.rename ? "date-time added to names" : "names kept", o.sidecars ? "sidecars included" : "no sidecars",
    o.parallel ? o.parallel + " at once" : "auto speed", check && (check === "quick" ? "quick check" : "disk check")].filter(Boolean).join(" · ");
}

function folderField(value, onChange, placeholder) {
  const input = h("input", { type: "text", value: value || "", placeholder: placeholder || "", oninput: e => onChange(e.target.value) });
  return h("div", { class: "field" }, input, h("button", { onclick: async () => {
    const p = await pickFolder(input.value);
    if (p) { input.value = p; onChange(p); }
  } }, "Browse…"));
}

function sourcesEditor(list, onChange) {
  const box = h("div");
  function draw() {
    const rows = list.map((s, i) => h("div", { class: "src" },
      h("span", { class: "faint" }, i + 1 + "."), h("span", { class: "p mono", title: s }, s),
      h("button", { class: "ghost small", "aria-label": "Move up", onclick: () => { if (i) { [list[i - 1], list[i]] = [list[i], list[i - 1]]; draw(); onChange(); } } }, "↑"),
      h("button", { class: "ghost small", "aria-label": "Move down", onclick: () => { if (i < list.length - 1) { [list[i + 1], list[i]] = [list[i], list[i + 1]]; draw(); onChange(); } } }, "↓"),
      h("button", { class: "ghost small", onclick: () => { list.splice(i, 1); draw(); onChange(); } }, "Remove")));
    box.replaceChildren(
      h("div", { class: "sources" }, rows.length ? rows : h("div", { class: "empty" }, "Add an SD card, drive or folder. Every subfolder is included.")),
      h("div", { class: "row", style: "margin-top:8px" },
        h("button", { onclick: async () => { const p = await pickFolder(list[list.length - 1]); if (p && !list.includes(p)) { list.push(p); draw(); onChange(); } } }, "Add folder…"),
        list.length > 1 ? h("span", { class: "faint" }, "Sources are sorted one after another, in this order.") : null));
  }
  draw();
  return box;
}

async function startScan(body) {
  try {
    const j = await api("POST", "/api/scan", body);
    location.hash = (j.purpose === "import" ? "#/jobs/" : "#/scan/") + j.id;
  } catch (e) { toast(e.message, 6000); }
}

// ---------- Active jobs ----------
// Every import is a job: its scan, then its preview while it waits for
// approval, then its sort. Several can run at once, and they carry on in the
// background while you use other tabs.
const stageInfo = {
  scanning: ["Scanning", "accent"], review: ["Waiting for approval", "warn"], sorting: ["Sorting", "accent"], stopped: ["Stopped", ""],
  done: ["Completed", "ok"], problems: ["Completed with errors", "warn"], failed: ["Failed", "danger"], cancelled: ["Cancelled", ""],
};
const stagePill = (k, extra) => h("span", { class: "pill " + stageInfo[k][1] }, stageInfo[k][0] + (extra ? " " + extra : ""));
// sortStatus is what became of a finished sort, from its summary.
function sortStatus(s) {
  if (s.cancelled) return "cancelled";
  if (s.failed && !(s.copied + s.moved)) return "failed";
  return s.failed ? "problems" : "done";
}
const jobPct = p => p.bytesTotal ? Math.floor(100 * p.bytesDone / p.bytesTotal) : p.total ? Math.floor(100 * p.done / p.total) : 0;

// Active jobs lists only jobs that are running or waiting on you; finished
// ones are in Job history. Each row has Open plus the one action its stage needs.
function pageJobs() {
  const list = h("div");
  setPage("jobs", h("h1", {}, "Active jobs"),
    h("p", { class: "sub" }, "Scans, previews waiting for your approval, sorts in progress, and stopped sorts. Several can run at once."),
    list);
  const act = (label, fn) => h("button", { onclick: async e => { e.stopPropagation(); await fn(); draw(); } }, label);
  async function draw() {
    let js;
    try { js = await api("GET", "/api/jobs"); } catch (e) { list.replaceChildren(errBox(e)); return; }
    if (!js.length) {
      list.replaceChildren(h("div", { class: "note" }, "Nothing running. ", h("a", { href: "#/" }, "Start a new job"), "."));
      return;
    }
    list.replaceChildren(h("div", { class: "card jobs" }, js.slice().reverse().map(j => {
      const p = j.progress, c = p.counts || {};
      const sorted = (c.copied || 0) + (c.moved || 0), verb = j.move ? "moved" : "copied";
      let detail = "", pct = null, action = null;
      if (j.stage === "scanning") {
        detail = (p.phase || "Starting…") + (p.total ? ` · ${num(p.done)} of ${num(p.total)}` : "");
        pct = p.total ? 100 * p.done / p.total : 5;
        action = act("Cancel", () => api("POST", `/api/jobs/${j.id}/cancel`).catch(() => {}));
      } else if (j.stage === "review") {
        detail = `${plural(j.toCopy, "file")} to ${j.move ? "move" : "copy"} · ${bytes(j.toCopyBytes)}`;
        action = act("Discard", () => discardPlan(j.planId));
      } else if (j.stage === "sorting") {
        pct = jobPct(p);
        detail = `${num(p.done)} of ${num(p.total)} files · ${bytes(p.bytesDone)} of ${bytes(p.bytesTotal)}`;
        action = act("Stop", () => api("POST", `/api/jobs/${j.id}/cancel`).catch(() => {}));
      } else if (j.stage === "stopped") {
        detail = `${num(sorted)} of ${num(p.total)} ${verb} · ${num(p.total - p.done)} remaining`;
        action = act("Cancel", async () => {
          if (await confirmBox("Cancel this job?", `The ${num(p.total - p.done)} files that weren't ${verb} stay in the source. The job goes to Job history as cancelled.`, "Cancel job", true))
            await api("POST", `/api/jobs/${j.id}/end`).catch(e => toast(e.message));
        });
      }
      return h("div", { class: "jobrow", onclick: () => (location.hash = "#/jobs/" + j.id) },
        h("div", { style: "min-width:0" },
          h("div", { class: "row" }, h("span", { class: "name" }, j.profile), stagePill(j.stage, j.stage === "sorting" ? pct + "%" : ""),
            h("span", { class: "faint", style: "font-size:12.5px" }, "started " + new Date(j.started).toLocaleTimeString())),
          h("div", { class: "mono faint", style: "overflow:hidden;text-overflow:ellipsis;white-space:nowrap;margin-top:2px" }, j.sources.join(", ") + "  →  " + j.dest),
          pct == null ? null : h("div", { class: "bar thin", style: "margin-top:8px" }, h("div", { style: `width:${pct}%` })),
          h("div", { class: "muted", style: "font-size:13px;margin-top:4px" }, detail)),
        h("div", { class: "row" }, action, h("button", {}, "Open")));
    })));
  }
  draw(); pollTimer = setInterval(draw, 1000);
}

// pageJob opens one job at the stage it's at.
async function pageJob(id) {
  const here = stillHere();
  let j;
  try { j = await api("GET", "/api/jobs/" + id); }
  catch (e) { if (here()) setPage("jobs", h("h1", {}, "Active jobs"), errBox(e.message === "no such job" ? "This job is gone (the app was restarted). Finished sorts are in Job history." : e)); return; }
  if (!here()) return;
  if (j.kind === "run") return pageRun(id);
  if (j.runId) return location.replace("#/jobs/" + j.runId);
  if (j.stage === "review" || j.stage === "empty") return pagePreview(j.planId);
  if (j.stage === "scanning" || j.error) return pageScan(id);
  setPage("jobs", h("h1", {}, "Active jobs"),
    h("div", { class: "note" }, "This preview was discarded. ", h("a", { href: "#/jobs" }, "All active jobs")));
}

// The nav badge says what's going on, from any page: the one active job's
// stage, or how many there are.
async function updateJobBadge() {
  const b = document.getElementById("job-badge");
  const js = await api("GET", "/api/jobs").catch(() => []);
  let text = "", cls = "nav-badge";
  if (js.length > 1) text = String(js.length);
  else if (js.length === 1) {
    const j = js[0];
    text = { sorting: jobPct(j.progress) + "%", scanning: "Scanning", review: "Review", stopped: "Stopped" }[j.stage];
  }
  if (js.length && js.every(j => j.stage === "review" || j.stage === "stopped")) cls += " wait";
  b.textContent = text; b.className = cls; b.hidden = !text;
}

// ---------- New job ----------
async function pageNewJob() {
  const list = h("div");
  setPage("new",
    h("h1", {}, "New job"),
    h("p", { class: "sub" }, "Scan, check the preview, then approve. Nothing is copied or moved before you approve."),
    h("div", { class: "card prow feature" },
      h("div", {}, h("div", { class: "name" }, "Manual import"),
        h("div", { class: "muted" }, "Pick any source folders and a destination, choose options, preview, approve. No profile needed — you can save it as one at the end.")),
      h("button", { class: "primary", onclick: () => (location.hash = "#/import") }, "Start")),
    h("h2", {}, "Profiles"), list);
  let ps;
  try { ps = await api("GET", "/api/profiles"); } catch (e) { list.replaceChildren(errBox(e)); return; }
  if (!ps.length) {
    list.replaceChildren(h("div", { class: "note" }, "A profile remembers a destination and its settings, and on a server, the to-sort folder it empties. ",
      h("a", { href: "#/profiles/new" }, "Create a profile")));
    return;
  }
  list.replaceChildren(...ps.map(p => {
    const status = h("span", { class: "pill" }, p.source ? "Counting…" : "Pick sources when you import");
    const extra = h("span");
    const btn = h("button", { onclick: () => {
      if (p.source) startScan({ profileId: p.id, purpose: "import" });
      else location.hash = "#/import?profile=" + p.id;
    } }, p.source ? "Scan and preview" : "Import…");
    if (p.source) {
      api("GET", "/api/count?path=" + encodeURIComponent(p.source)).then(c => {
        if (!c.exists) { status.className = "pill danger"; status.textContent = "To-sort folder not found"; return; }
        status.className = "pill " + (c.media ? "accent" : "");
        status.textContent = c.media ? `${num(c.media)}${c.partial ? "+" : ""} photos and videos · ${bytes(c.bytes)}` : "Empty";
        if (c.media) btn.className = "primary";
        if (c.other) extra.replaceChildren(h("a", { href: "#/leftovers?profile=" + p.id, class: "pill warn", style: "text-decoration:none" }, plural(c.other, "other file")));
      }).catch(() => { status.textContent = "Couldn't count"; });
    }
    return h("div", { class: "card prow" },
      h("div", { style: "min-width:0" }, h("div", { class: "name" }, p.name),
        h("div", { class: "mono muted", style: "overflow:hidden;text-overflow:ellipsis;white-space:nowrap" }, (p.source ? p.source + "  →  " : "→  ") + p.dest)),
      h("div", { class: "row" }, status, extra, btn));
  }));
}

// ---------- Import setup ----------
async function pageImport(q) {
  const here = stillHere();
  let profile = null;
  const st = await api("GET", "/api/settings").catch(() => ({ last: {} }));
  if (q.get("profile")) {
    const ps = await api("GET", "/api/profiles");
    profile = ps.find(p => p.id === q.get("profile"));
  }
  if (!here()) return;
  const o = profile
    ? { sources: profile.source ? [profile.source] : [], dest: profile.dest }
    : Object.assign({ jpg: true, raw: true, mov: true, rename: true, sidecars: true, move: false }, st.last, { sources: [...(st.last.sources || [])] });
  const err = h("div");
  const form = h("div", { class: "form" },
    h("div", { class: "lbl" }, "Sources"), sourcesEditor(o.sources, () => {}),
    h("div", { class: "lbl" }, "Destination"),
    profile ? h("div", { class: "mono", style: "padding-top:6px" }, profile.dest) : folderField(o.dest, v => (o.dest = v), "Archive folder"),
    h("div", { class: "lbl" }, "Layout"), h("div", { class: "mono muted", style: "padding-top:6px" }, "<destination>\\yyyymm\\JPG | RAW | MOV"),
    ...(profile ? [h("div", { class: "lbl" }, "Settings"), h("div", { style: "padding-top:6px" }, settingsLine(profile) + "  ",
      h("a", { href: "#/profiles/" + profile.id }, "Edit profile"))] : optionsForm(o)));
  setPage("new",
    h("h1", {}, profile ? profile.name : "Manual import"),
    h("p", { class: "sub" }, "Choose what to sort. The next step is a dry run: you'll see every folder and file before anything is written."),
    h("div", { class: "card" }, form),
    err,
    h("div", { class: "row", style: "justify-content:flex-end;margin-top:14px" },
      h("button", { onclick: () => (location.hash = "#/") }, "Back"),
      h("button", { class: "primary", onclick: () => {
        err.replaceChildren();
        if (!o.sources.length) return err.replaceChildren(h("div", { class: "error", style: "margin-top:12px" }, "Add at least one source folder."));
        if (!o.dest) return err.replaceChildren(h("div", { class: "error", style: "margin-top:12px" }, "Choose a destination folder."));
        if (profile) startScan({ profileId: profile.id, purpose: "import", options: { sources: o.sources } });
        else startScan({ options: o, purpose: "import" });
      } }, "Scan and preview")));
}

// ---------- Scan progress ----------
function pageScan(id) {
  const phase = h("div", { style: "font-weight:600" }, "Starting…");
  const count = h("div", { class: "muted" });
  const bar = h("div", { style: "width:0%" });
  const cur = h("div", { class: "mono faint", style: "overflow:hidden;text-overflow:ellipsis;white-space:nowrap" });
  const cancel = h("button", { onclick: async () => {
    cancel.disabled = true; cancel.textContent = "Cancelling…";
    await api("POST", `/api/jobs/${id}/cancel`).catch(() => {});
  } }, "Cancel");
  const nav = location.hash.startsWith("#/scan") ? "leftovers" : "jobs";
  setPage(nav, h("h1", {}, "Scanning"),
    h("p", { class: "sub" }, "Reading capture dates and checking the destination. Nothing is written."),
    h("div", { class: "card stack" }, phase, h("div", { class: "bar" }, bar), count, cur,
      h("div", { class: "row", style: "justify-content:flex-end" }, cancel)));
  // A scan that stops without a preview gets its own simple page.
  const stopped = (cancelled, msg) => setPage(nav,
    h("h1", {}, cancelled ? "Scan cancelled" : "Scan failed"),
    h("p", { class: "sub" }, "Nothing was copied, moved or written."),
    cancelled ? null : h("div", { class: "error", style: "margin-bottom:14px" }, msg),
    h("div", { class: "row" }, h("button", { onclick: () => history.back() }, "Back"), h("a", { class: "btn", href: "#/" }, "New job")));
  const tick = async () => {
    let j;
    try { j = await api("GET", "/api/jobs/" + id); } catch (e) { stopped(false, e.message); return; }
    const p = j.progress;
    phase.textContent = p.phase || "Starting…";
    count.textContent = p.total ? `${num(p.done)} of ${num(p.total)}` : p.done ? `${num(p.done)} files found` : "";
    bar.style.width = (p.total ? (100 * p.done / p.total) : 5) + "%";
    cur.textContent = p.current || "";
    if (j.done) {
      clearInterval(pollTimer);
      if (j.error) { stopped(j.error === "Scan cancelled", j.error); return; }
      if (j.purpose === "leftovers") location.replace("#/leftovers?plan=" + j.planId);
      else route(); // the job's page moves on to its preview
    }
  };
  tick(); pollTimer = setInterval(tick, 500);
}

// ---------- Preview ----------
const reasonLabel = {
  duplicate: "Already in archive", system: "Camera system files", excluded: "Left out by settings",
  unknown: "Unknown files", sidecar: "Sidecars not moved", failed: "Failed",
};

async function pagePreview(id) {
  const here = stillHere();
  let p;
  try { p = await api("GET", "/api/plans/" + id); } catch (e) { if (here()) setPage("jobs", h("h1", {}, "Preview"), errBox(e)); return; }
  if (!here()) return;
  const items = p.items, o = p.options;
  const toSort = items.filter(i => i.action === "copy");
  const dups = items.filter(i => i.action === "duplicate");
  const clash = toSort.filter(i => i.clash);
  const fallback = toSort.filter(i => i.fallback);
  const skipped = items.filter(i => i.action === "skip");
  const totalBytes = toSort.reduce((a, i) => a + i.size, 0);
  const verb = o.move ? "move" : "copy";
  const multi = o.sources.length > 1;

  let filter = "all", treeSel = null;
  const sets = {
    all: { label: "Files to " + verb, rows: toSort },
    clash: { label: "Need to rename", rows: clash, title: "A different file already has this name, so _1, _2… is added" },
    fallback: { label: "No date (from metadata)", rows: fallback, title: "No capture date inside the file; dated from its file name or modified time" },
    dup: { label: "Duplicates", rows: dups, title: "An identical file is already in the destination; not copied" },
  };
  // Files this sort won't touch; the Reason column says why for each one.
  sets.skip = { label: "Ignored files", rows: skipped, title: "Not " + (o.move ? "moved" : "copied") + ": types turned off, camera system files, unknown files, sidecars without a photo" };

  // Each group gets the columns that matter for it.
  // A renamed file's wanted name is its new name without the _N suffix; the
  // file already holding that name sits in the same folder.
  const suffix = /^(.*)(_\d+)(\.[^.]+)?$/;
  const wanted = i => { const m = pathBase(i.dest).match(suffix); return m ? m[1] + (m[3] || "") : pathBase(i.dest); };
  const C = {
    src: { label: "Src", render: i => String(i.source + 1), sort: i => i.source, title: i => o.sources[i.source] },
    file: { label: "File", tail: true, render: i => i.rel, sort: i => i.rel.toLowerCase() },
    dest: { label: "Destination", tail: true, render: i => disp(i.dest), sort: i => i.dest || "" },
    taken: { label: "Time taken", render: i => i.date || "", sort: i => i.date || "" },
    size: { label: "Size", render: i => bytes(i.size), sort: i => i.size },
    newName: { label: "New name", tail: true, strong: true, text: i => pathBase(i.dest), sort: i => pathBase(i.dest).toLowerCase(),
      render: i => { const m = pathBase(i.dest).match(suffix); return h("span", { class: "hl" }, m ? [m[1], h("mark", {}, m[2]), m[3] || ""] : pathBase(i.dest)); } },
    existing: { label: "Existing file", tail: true, render: i => disp(String(i.dest).split("/").slice(0, -1).concat(wanted(i)).join("/")), sort: i => i.dest },
    same: { label: "Identical to", tail: true, render: i => disp(i.match), sort: i => i.match || "" },
    modified: { label: "Modified", render: i => i.modified || "", sort: i => i.modified || "" },
    reason: { label: "Reason", render: i => `${reasonLabel[i.reason] || ""}${i.note ? ": " + i.note : ""}`, sort: i => (i.reason || "") + (i.note || "") },
  };
  const layouts = {
    all: [C.file, C.size, C.taken, C.dest],
    clash: [C.file, C.size, C.taken, C.newName, C.existing],
    fallback: [C.file, C.size, C.modified],
    dup: [C.file, C.size, C.taken, C.same],
    skip: [C.file, C.size, C.modified, C.reason],
  };
  const tableBox = h("div");
  let table = null, tableFor = null;

  const chips = h("div", { class: "chips" });
  function refresh() {
    let rows = sets[filter].rows;
    if (treeSel) rows = rows.filter(i => (i.dest || i.match || "").startsWith(treeSel + "/"));
    if (tableFor !== filter) {
      table = vtable({ id: "preview-" + filter, columns: (multi ? [C.src] : []).concat(layouts[filter]), height: 420, empty: "No files in this group",
        foot: r => `${num(r.length)} files · ${bytes(r.reduce((a, i) => a + i.size, 0))}` });
      tableBox.replaceChildren(table.el);
      tableFor = filter;
    }
    table.setRows(rows);
    chips.replaceChildren(...Object.entries(sets).map(([k, s]) => h("span", {
      class: "chip" + (filter === k ? " on" : "") + (s.rows.length ? "" : " zero"), title: s.title || null,
      onclick: () => { filter = k; refresh(); },
    }, s.label, h("span", { class: "n" }, num(s.rows.length)))));
    tree.querySelectorAll("[data-sel]").forEach(e => e.classList.toggle("sel", e.dataset.sel === treeSel));
  }

  const tree = h("div", { class: "tree" });
  const treeRows = [];
  for (const m of p.tree) {
    treeRows.push(h("div", { class: "m", "data-sel": m.month, onclick: () => { treeSel = treeSel === m.month ? null : m.month; filter = "all"; refresh(); } },
      h("span", { class: "name" }, "📁 " + m.month + "\\"),
      h("span", {}, m.exists ? h("span", { class: "pill" }, "exists") : h("span", { class: "pill accent" }, "new")),
      h("span", { class: "muted" }, `${plural(m.files, "file")} · ${bytes(m.bytes)}`)));
    for (const c of m.cats) {
      const sel = m.month + "/" + c.dir;
      treeRows.push(h("div", { class: "c", "data-sel": sel, onclick: () => { treeSel = treeSel === sel ? null : sel; filter = "all"; refresh(); } },
        h("span", {}, c.dir + "\\"),
        h("span", {}, c.exists ? "" : h("span", { class: "pill accent" }, "new")),
        h("span", { class: "muted" }, c.files ? `${plural(c.files, "file")} · ${bytes(c.bytes)}` : (c.exists ? "nothing new" : "empty folder"),
          c.dups ? ` · ${num(c.dups)} already there` : "")));
    }
  }
  tree.replaceChildren(...(treeRows.length ? treeRows : [h("div", { class: "faint", style: "padding:8px" }, "No folders will be created: nothing new to sort.")]));

  const approve = h("button", { class: "primary", onclick: async () => {
    approve.disabled = true;
    try { const j = await api("POST", `/api/plans/${id}/run`); location.hash = "#/jobs/" + j.id; }
    catch (e) { toast(e.message, 6000); approve.disabled = false; }
  } }, `Approve and ${verb} ${plural(toSort.length, "file")}`);

  // Back to settings drops this preview and reopens the settings it came from.
  const back = async () => {
    let to = "#/import";
    if (p.profile !== "Manual import") {
      const pr = (await api("GET", "/api/profiles").catch(() => [])).find(x => x.name === p.profile);
      to = pr ? "#/import?profile=" + pr.id : "#/";
    }
    await api("DELETE", "/api/plans/" + id).catch(() => {});
    location.hash = to;
  };
  const download = h("a", { href: `/api/plans/${id}/csv`, download: "", style: "font-size:13px;font-weight:400" }, "Download list (CSV)");
  const filesHead = h("div", { class: "spread", style: "margin:24px 0 10px" }, h("h2", { style: "margin:0" }, "Files"), download);

  // A scan that found nothing new is a result, not a job waiting for approval.
  if (!toSort.length) {
    delete sets.all; delete sets.clash; delete sets.fallback;
    filter = dups.length ? "dup" : "skip";
    setPage("new",
      h("h1", {}, "Nothing new to copy"),
      h("p", { class: "sub" }, `${o.sources.join(", ")}  →  ${o.dest} · nothing was written`),
      h("div", { class: "metrics" }, metric("Already in archive", num(dups.length)), metric("Ignored files", num(skipped.length))),
      filesHead, chips, h("div", { style: "height:8px" }), tableBox,
      h("div", { class: "sticky-foot row", style: "justify-content:flex-end" },
        h("button", { onclick: back }, "Change settings"),
        h("button", { class: "primary", onclick: async () => { await api("DELETE", "/api/plans/" + id).catch(() => {}); location.hash = "#/"; } }, "Done")));
    refresh();
    return;
  }

  setPage("jobs",
    h("div", { class: "spread" }, h("div", {}, h("h1", {}, "Preview — " + p.profile),
      h("p", { class: "sub" }, "Dry run: nothing has been written. Check it, then approve.")),
      h("span", { class: "pill accent" }, o.move ? "Move" : "Copy")),
    h("div", { class: "card stack" },
      h("div", { class: "form", style: "grid-template-columns:110px minmax(0,1fr);gap:4px 12px" },
        h("div", { class: "muted" }, multi ? "Sources" : "Source"), h("div", { class: "mono" }, o.sources.map((s, i) => h("div", {}, (multi ? i + 1 + ". " : "") + s))),
        h("div", { class: "muted" }, "Destination"), h("div", { class: "mono" }, o.dest),
        h("div", { class: "muted" }, "Speed"), h("div", {}, `${plural(p.workers, "file")} at a time (${p.workersWhy})`)),
      h("div", { class: "metrics" },
        metric("To " + verb, num(toSort.length), bytes(totalBytes)),
        metric("Duplicates", num(dups.length), "already there, not " + (o.move ? "moved" : "copied")),
        metric("Need to rename", num(clash.length), "name taken, _1 added"),
        metric("No date (from metadata)", num(fallback.length), "dated from file name or file time"),
        metric("Ignored files", num(skipped.length), skipped.length ? Object.entries(skipped.reduce((a, i) => (a[i.reason] = (a[i.reason] || 0) + 1, a), {}))
          .map(([r, n]) => `${num(n)} ${reasonLabel[r].toLowerCase()}`).join(" · ") : "nothing left out"))),
    h("h2", {}, "Folders"),
    h("div", { class: "card" }, h("div", { class: "faint", style: "font-size:12.5px;margin-bottom:6px" }, "Every month folder gets JPG, RAW and MOV. Click a folder to list its files."), tree),
    filesHead, chips, h("div", { style: "height:8px" }), tableBox,
    h("div", { class: "sticky-foot spread" },
      h("button", { onclick: back }, "← Back to settings"),
      h("div", { class: "row" },
        h("button", { onclick: () => discardPlan(id) }, "Discard"),
        approve)));
  refresh();
}

async function discardPlan(id) {
  if (!(await confirmBox("Discard this preview?", "Nothing has been copied or moved. Scan again to start over.", "Discard", true))) return false;
  await api("DELETE", "/api/plans/" + id).catch(() => {});
  if (location.hash.startsWith("#/jobs/")) location.hash = "#/jobs";
  return true;
}

// Time spent sorting; a stop and continue counts only the running time.
function seconds(s) { return s.elapsed || (new Date(s.finished) - new Date(s.started)) / 1000; }
function duration(s) {
  const t = Math.round(seconds(s));
  return t >= 3600 ? `${Math.floor(t / 3600)} h ${Math.round(t % 3600 / 60)} min` : t >= 60 ? `${Math.floor(t / 60)} min ${t % 60} s` : `${t} s`;
}
// avgSpeed counts copied bytes only; instant renames would make it meaningless.
function avgSpeed(s) {
  const t = seconds(s);
  return t > 0.5 && s.copiedBytes ? bytes(s.copiedBytes / t) + "/s" : "";
}

function metric(k, v, s) {
  return h("div", { class: "metric" }, h("div", { class: "k" }, k), h("div", { class: "v" }, v), s ? h("div", { class: "s" }, s) : null);
}

// ---------- Sort: running, stopped, finished ----------
// One page for a sort job in every state, with only the buttons that state needs:
// sorting → Stop · stopped → Cancel job, Continue · finished → Open destination, Close.
function pageRun(id) {
  const title = h("h1", {});
  const sub = h("p", { class: "sub" });
  const line = h("div", { class: "spread" });
  const bar = h("div", { style: "width:0%" });
  const srcLine = h("div", { class: "muted", style: "font-size:13px" });
  const cur = h("div", { class: "mono faint", style: "line-height:1.6" });
  const counts = h("div", { class: "metrics" });
  const btns = h("div", { class: "row", style: "justify-content:flex-end" });
  const below = h("div");
  setPage("jobs", title, sub, h("div", { class: "card stack" }, line, h("div", { class: "bar" }, bar), srcLine, cur, counts, btns), below);
  // Speed over the last few seconds, like Explorer's copy dialog.
  let samples = [], shown = null, stopping = false;
  const tick = async () => {
    let j;
    try { j = await api("GET", "/api/jobs/" + id); } catch (e) { below.replaceChildren(errBox(e)); clearInterval(pollTimer); return; }
    const p = j.progress, c = p.counts || {};
    const sorting = j.stage === "sorting";
    const verb = j.move ? "moved" : "copied";
    const sorted = (c.copied || 0) + (c.moved || 0), remaining = Math.max(0, p.total - p.done);
    const now = Date.now();
    if (sorting) samples.push([now, p.bytesDone]); else samples = [];
    while (samples.length > 2 && now - samples[0][0] > 5000) samples.shift();
    const dt = samples.length ? (now - samples[0][0]) / 1000 : 0;
    const speed = dt > 0.8 ? (p.bytesDone - samples[0][1]) / dt : null;
    const pct = p.bytesTotal ? p.bytesDone / p.bytesTotal : (p.total ? p.done / p.total : 0);
    const left = speed > 0 ? Math.round((p.bytesTotal - p.bytesDone) / speed) : null;
    const leftText = left == null ? "" : left >= 3600 ? `${Math.floor(left / 3600)} h ${Math.round(left % 3600 / 60)} min left` : left > 90 ? `${Math.round(left / 60)} min left` : `${left} s left`;
    line.replaceChildren(h("span", {}, `${Math.floor(100 * pct)}% · ${num(p.done)} of ${num(p.total)} files · ${bytes(p.bytesDone)} of ${bytes(p.bytesTotal)}`),
      sorting ? h("span", {}, h("b", {}, speed == null ? "" : bytes(speed) + "/s"), h("span", { class: "muted" }, leftText ? "  ·  " + leftText : "")) : h("span"));
    bar.style.width = 100 * pct + "%";
    bar.parentElement.classList.toggle("paused", !sorting);
    srcLine.textContent = sorting && p.sources > 1 ? `Source ${p.source + 1} of ${p.sources}: ${num(p.sourceDone)} of ${num(p.sourceTotal)} files` : "";
    const active = sorting ? (p.active || []) : [];
    cur.replaceChildren(...(active.length ? [h("div", { class: "faint", style: "font-family:inherit" }, p.workers > 1 ? `${j.move ? "Moving" : "Copying"} ${num(active.length)} of up to ${num(p.workers)} files at once:` : (j.move ? "Moving:" : "Copying:")),
      ...active.map(a => h("div", { style: "overflow:hidden;text-overflow:ellipsis;white-space:nowrap" },
        h("span", { class: "pill" + (a.checking ? " accent" : ""), style: "display:inline-block;min-width:68px;text-align:center;margin-right:8px;font-family:inherit" }, a.checking ? "checking" : j.move ? "moving" : "copying"), a.rel))] : []));
    counts.replaceChildren(metric(j.move ? "Moved" : "Copied", num(sorted)), metric("Remaining", num(remaining)),
      metric("Duplicates", num(c.duplicate || 0)), metric("Failed", num(c.failed || 0)));
    if (sorting) {
      title.textContent = stopping ? "Stopping…" : j.move ? "Moving" : "Copying";
      sub.textContent = stopping ? "Finishing the files in progress, then stopping." : (j.quickCheck ? "Each file is checked byte-for-byte." : "Each file is saved, then read back from the disk and compared.") + " You can leave this page; the job carries on.";
    } else if (j.stage === "stopped") {
      title.textContent = "Stopped";
      sub.textContent = `${num(sorted)} of ${num(p.total)} files ${verb}. The other ${num(remaining)} are still in the source, untouched.`;
    }
    if (shown === j.stage) return;
    shown = j.stage;
    if (sorting) {
      stopping = false;
      const stop = h("button", { onclick: async () => { stopping = true; stop.disabled = true; await api("POST", `/api/jobs/${id}/cancel`).catch(() => {}); tick(); } }, "Stop");
      btns.replaceChildren(stop);
      below.replaceChildren();
    } else if (j.stage === "stopped") {
      btns.replaceChildren(
        h("button", { onclick: async () => {
          if (!(await confirmBox("Cancel this job?", `The ${num(remaining)} files that weren't ${verb} stay in the source. The job goes to Job history as cancelled.`, "Cancel job", true))) return;
          await api("POST", `/api/jobs/${id}/end`).catch(e => toast(e.message)); tick();
        } }, "Cancel job"),
        h("button", { class: "primary", onclick: async () => {
          try { await api("POST", `/api/jobs/${id}/continue`); shown = null; clearInterval(pollTimer); tick(); pollTimer = setInterval(tick, 500); }
          catch (e) { toast(e.message, 6000); }
        } }, "Continue"));
    } else {
      clearInterval(pollTimer);
      showSummary(j, title, sub, btns, below);
    }
  };
  tick(); pollTimer = setInterval(tick, 500);
}

async function showSummary(j, title, sub, btns, below) {
  const s = j.summary || {};
  const st = j.stage === "cancelled" || j.stage === "failed" || j.stage === "problems" ? j.stage : "done";
  title.textContent = { done: "Done", problems: "Finished with problems", failed: "Failed", cancelled: "Cancelled" }[st];
  const n = (s.copied || 0) + (s.moved || 0);
  sub.replaceChildren(`${num(n)} files ${j.move ? "moved" : "copied"} (${bytes(s.bytes)})`,
    avgSpeed(s) ? ` in ${duration(s)} · average ${avgSpeed(s)}` : "",
    s.check === "disk" && s.copiedBytes ? " · all read back from the disk and matched" : "",
    s.notRun ? ` · ${num(s.notRun)} not ${j.move ? "moved" : "copied"}, still in the source` : "",
    j.historyId ? h("span", {}, " · ", h("a", { href: "#/history/" + j.historyId }, "Details")) : "",
    j.profile === "Manual import" ? h("span", {}, " · ", h("a", { href: "#", onclick: async e => {
      e.preventDefault();
      const entry = await api("GET", "/api/history/" + j.historyId).catch(x => toast(x.message));
      if (entry) saveAsProfile(entry.options);
    } }, "Save as profile")) : "");
  btns.replaceChildren(...[
    INFO.canOpen && h("button", { onclick: () => api("POST", "/api/open", { path: j.dest }).catch(e => toast(e.message)) }, "Open destination"),
    h("button", { class: "primary", onclick: () => (location.hash = "#/jobs") }, "Close")].filter(Boolean));
  const kids = [];
  if (j.error) kids.push(errBox(j.error));
  if (s.failed && j.historyId) {
    const entry = await api("GET", "/api/history/" + j.historyId).catch(() => null);
    if (entry) kids.push(h("h2", {}, "Failed"), h("p", { class: "sub" }, "Their originals were kept. They're also listed on the Leftovers page."),
      vtable({ id: "failed", height: 220, rows: entry.results.filter(r => r.status === "failed"), columns: [
        { label: "File", tail: true, render: r => r.src, sort: r => r.src },
        { label: "Problem", render: r => r.error || "", sort: r => r.error || "" }] }).el);
  }
  below.replaceChildren(...kids);
}

async function saveAsProfile(o) {
  const name = h("input", { type: "text", placeholder: "fuji x-t50", style: "width:100%" });
  const v = await modal("Save as a profile", h("div", { class: "stack" },
    h("div", { class: "muted" }, "Keeps the destination and settings. You can add a to-sort folder in Profiles."), name),
    [{ label: "Cancel", value: false }, { label: "Save", value: true, cls: "primary" }]);
  if (!v) return;
  try {
    await api("POST", "/api/profiles", { name: name.value, dest: o.dest, move: o.move, jpg: o.jpg, raw: o.raw, mov: o.mov, rename: o.rename, sidecars: o.sidecars, parallel: o.parallel, quickCheck: o.quickCheck });
    toast("Profile saved");
  } catch (e) { toast(e.message, 6000); }
}

// ---------- Leftovers ----------
const leftoverInfo = [
  ["duplicate", "Already in archive", true, "An identical copy is in the archive. Before anything is deleted, each file is compared byte-for-byte with it."],
  ["system", "Camera system files", true, "Files the camera or computer makes that aren't photos: .CTG .THM .LRV, clip .XML, AVCHD index files, .DS_Store, ._*, Thumbs.db."],
  ["excluded", "Left out by profile", false, "Photos, videos or sidecars this profile is set not to sort. Change the profile or delete them."],
  ["unknown", "Unknown files", false, "Not a photo, video or sidecar the app knows."],
  ["sidecar", "Sidecars not moved", false, "No matching photo here, or a different sidecar already sits next to that photo in the archive (never overwritten)."],
  ["failed", "Failed", false, "Couldn't be read, or the copy didn't match the original. The original was kept. Sort again to retry."],
];

const leftBy = i => i.lastJob ? `${i.lastJob.profile} · ${new Date(i.lastJob.started).toLocaleString()}` : "None yet";

function leftoverCat(i) {
  if (i.failed) return "failed";
  if (i.action === "duplicate") return "duplicate";
  if (i.action === "skip") return i.reason;
  return null;
}

async function pageLeftovers(q) {
  const here = stillHere();
  const planId = q.get("plan");
  let ps = [];
  try { ps = (await api("GET", "/api/profiles")).filter(p => p.source); } catch { /* shown below */ }
  if (!here()) return;
  if (!planId) {
    const pick = h("select", {}, ps.map(p => h("option", { value: p.id }, p.name)));
    setPage("leftovers", h("h1", {}, "Leftovers"),
      h("p", { class: "sub" }, "What's still in a profile's to-sort folder after sorting, grouped by why it stayed. Nothing is deleted until you choose."),
      ps.length ? h("div", { class: "card row" }, h("span", {}, "Profile"), pick,
        h("button", { class: "primary", onclick: () => startScan({ profileId: pick.value, purpose: "leftovers" }) }, "Scan for leftovers"))
        : h("div", { class: "note" }, "Leftovers works on profiles that have a to-sort folder. ", h("a", { href: "#/profiles" }, "Set one in Profiles")));
    const want = q.get("profile");
    if (want && ps.some(p => p.id === want)) { pick.value = want; startScan({ profileId: want, purpose: "leftovers" }); }
    return;
  }
  let p;
  try { p = await api("GET", "/api/plans/" + planId); } catch (e) { if (here()) setPage("leftovers", h("h1", {}, "Leftovers"), errBox(e)); return; }
  if (!here()) return;
  const profile = ps.find(x => x.name === p.profile);
  let rows = p.items.filter(i => !i.deleted && leftoverCat(i));
  const waiting = p.items.filter(i => !i.deleted && i.action === "copy" && !i.failed);
  const sel = new Set(rows.filter(i => ["duplicate", "system"].includes(leftoverCat(i))).map(i => i.id));
  let filter = "all";
  const chips = h("div", { class: "chips" });
  const foot = h("span", { class: "muted" });
  const del = h("button", { class: "danger" });
  const table = vtable({
    id: "leftovers", height: 440, empty: "No leftovers in this group",
    select: { set: sel, key: i => i.id, onchange: () => updateFoot() },
    columns: [
      { label: "File", render: i => i.rel, sort: i => i.rel.toLowerCase(), title: i => i.src },
      { label: "Why it's here", render: i => reasonLabel[leftoverCat(i)], sort: i => leftoverCat(i) },
      { label: "Details", render: i => i.failed ? i.failed : i.action === "duplicate" ? "= " + i.match : i.note || "", sort: i => i.match || i.note || "" },
      { label: "Size", render: i => bytes(i.size), sort: i => i.size },
      // The newest job that saw this file and left it; none if it arrived after the last job.
      { label: "Left by job", text: leftBy, sort: i => i.lastJob ? i.lastJob.started : "",
        title: i => i.lastJob ? "Open this job in Job history" : "No recent job has seen this file: it was added after the last sort",
        render: i => i.lastJob ? h("a", { href: "#/history/" + i.lastJob.historyId, onclick: e => e.stopPropagation() }, leftBy(i)) : h("span", { class: "faint" }, leftBy(i)) },
    ],
    foot: r => `${num(r.length)} shown`,
  });
  function counts() { const c = {}; for (const i of rows) c[leftoverCat(i)] = (c[leftoverCat(i)] || 0) + 1; return c; }
  function refresh() {
    const c = counts();
    chips.replaceChildren(
      h("span", { class: "chip" + (filter === "all" ? " on" : ""), onclick: () => { filter = "all"; refresh(); } }, "All", h("span", { class: "n" }, num(rows.length))),
      ...leftoverInfo.filter(([k]) => c[k]).map(([k, label, safe]) =>
        h("span", { class: "chip" + (filter === k ? " on" : ""), title: safe ? "Safe to delete" : "Your call", onclick: () => { filter = k; refresh(); } },
          label, h("span", { class: "n" }, num(c[k])))));
    table.setRows(filter === "all" ? rows : rows.filter(i => leftoverCat(i) === filter));
    updateFoot();
  }
  function updateFoot() {
    const chosen = rows.filter(i => sel.has(i.id));
    foot.textContent = `${num(chosen.length)} selected · ${bytes(chosen.reduce((a, i) => a + i.size, 0))}`;
    del.textContent = `Delete ${num(chosen.length)} selected…`;
    del.disabled = !chosen.length;
  }
  del.onclick = async () => {
    const chosen = rows.filter(i => sel.has(i.id));
    const byCat = {};
    for (const i of chosen) byCat[leftoverCat(i)] = (byCat[leftoverCat(i)] || 0) + 1;
    const ok = await confirmBox("Delete " + plural(chosen.length, "file") + "?",
      h("div", { class: "stack" },
        h("div", {}, `This permanently deletes ${plural(chosen.length, "file")} (${bytes(chosen.reduce((a, i) => a + i.size, 0))}) from the to-sort folder. It can't be undone.`),
        h("ul", { style: "margin:0" }, Object.entries(byCat).map(([k, n]) => h("li", {}, `${reasonLabel[k]}: ${num(n)}`))),
        byCat.duplicate ? h("div", { class: "muted" }, "Each “Already in archive” file is compared byte-for-byte with its archive copy first, and kept if they differ.") : null),
      "Delete", true);
    if (!ok) return;
    del.disabled = true; del.textContent = "Checking and deleting\u2026";
    try {
      const res = await api("POST", `/api/plans/${planId}/delete`, { ids: chosen.map(i => i.id) });
      const okIds = new Set(res.filter(r => r.ok).map(r => r.id));
      const bad = res.filter(r => !r.ok);
      rows = rows.filter(i => !okIds.has(i.id));
      okIds.forEach(id => sel.delete(id));
      refresh();
      toast(`Deleted ${plural(okIds.size, "file")}` + (bad.length ? ` · ${num(bad.length)} kept: ${bad[0].error}` : ""), 6000);
    } catch (e) { toast(e.message, 6000); updateFoot(); }
  };

  const legend = h("div", { class: "card", style: "font-size:13px" }, leftoverInfo.map(([k, label, safe, text]) =>
    h("div", { style: "display:grid;grid-template-columns:170px minmax(0,1fr) 100px;gap:10px;padding:4px 0" },
      h("b", {}, label), h("span", { class: "muted" }, text), h("span", { class: "pill " + (safe ? "ok" : "warn"), style: "justify-self:end" }, safe ? "safe to delete" : "your call"))));
  setPage("leftovers",
    h("div", { class: "spread" }, h("div", {}, h("h1", {}, "Leftovers — " + p.profile), h("p", { class: "sub mono" }, p.options.sources.join(", "))),
      h("button", { onclick: () => startScan({ profileId: profile ? profile.id : "", purpose: "leftovers" }) }, "Rescan")),
    waiting.length ? h("div", { class: "note", style: "margin-bottom:12px" },
      `${plural(waiting.length, "photo or video", "photos and videos")} (${bytes(waiting.reduce((a, i) => a + i.size, 0))}) are waiting to be sorted. `,
      profile ? h("a", { href: "#", onclick: e => { e.preventDefault(); startScan({ profileId: profile.id, purpose: "import" }); } }, "Scan and preview") : null) : null,
    chips, h("div", { style: "height:8px" }), table.el,
    h("div", { class: "sticky-foot spread" }, foot, h("div", { class: "row" }, del)),
    h("h2", {}, "What the groups mean"), legend);
  refresh();
}

// ---------- Profiles ----------
async function pageProfiles() {
  const here = stillHere();
  let ps;
  try { ps = await api("GET", "/api/profiles"); } catch (e) { if (here()) setPage("profiles", h("h1", {}, "Profiles"), errBox(e)); return; }
  if (!here()) return;
  setPage("profiles",
    h("div", { class: "spread" }, h("h1", {}, "Profiles"), h("a", { class: "btn", href: "#/profiles/new" }, "New profile")),
    h("p", { class: "sub" }, "A profile is a destination with its default settings, plus (on a server) the to-sort folder it empties."),
    ps.length ? h("div", {}, ps.map(p => h("div", { class: "card prow" },
      h("div", { style: "min-width:0" }, h("div", { class: "name" }, p.name),
        h("div", { class: "mono muted" }, (p.source ? p.source + "  →  " : "") + p.dest)),
      h("a", { class: "btn", href: "#/profiles/" + p.id }, "Edit"))))
      : h("div", { class: "note" }, "No profiles yet."));
}

async function pageProfile(id) {
  const here = stillHere();
  const ps = await api("GET", "/api/profiles").catch(() => []);
  if (!here()) return;
  const p = id === "new" ? { name: "", source: "", dest: "", move: true, jpg: true, raw: true, mov: true, rename: true, sidecars: true }
    : Object.assign({}, ps.find(x => x.id === id));
  if (id !== "new" && !p.id) { setPage("profiles", h("h1", {}, "Profile"), errBox("Profile not found")); return; }
  const err = h("div");
  const srcNote = h("div", { class: "faint", style: "font-size:12.5px;margin-top:4px" });
  async function checkSource() {
    srcNote.replaceChildren();
    if (!p.source) { srcNote.textContent = "Optional. On a server, set this to the folder you dump cards into, e.g. /camera-photo/dumps-to-sort/fuji x-t50."; return; }
    const c = await api("GET", "/api/count?path=" + encodeURIComponent(p.source)).catch(() => null);
    if (c && !c.exists) srcNote.replaceChildren("This folder doesn't exist yet. ",
      h("a", { href: "#", onclick: async e => { e.preventDefault(); try { await api("POST", "/api/mkdir", { path: p.source }); toast("Folder created"); checkSource(); } catch (x) { toast(x.message); } } }, "Create it"));
    else if (c) srcNote.textContent = `${num(c.media)} photos and videos waiting, ${num(c.other)} other files.`;
  }
  const form = h("div", { class: "form" },
    h("div", { class: "lbl" }, "Name"), h("input", { type: "text", value: p.name, placeholder: "fuji x-t50", oninput: e => (p.name = e.target.value) }),
    h("div", { class: "lbl" }, "To-sort folder"), h("div", {}, folderField(p.source, v => { p.source = v; clearTimeout(p._t); p._t = setTimeout(checkSource, 400); }, "/camera-photo/dumps-to-sort/fuji x-t50"), srcNote),
    h("div", { class: "lbl" }, "Destination"), h("div", {}, folderField(p.dest, v => (p.dest = v), "/camera-photo/fuji x-t50/Archive"), h("div", { class: "faint", style: "font-size:12.5px;margin-top:4px" }, "Created on the first approved sort if it doesn't exist.")),
    ...optionsForm(p));
  checkSource();
  const buttons = h("div", { class: "row", style: "justify-content:flex-end;margin-top:14px" });
  if (p.id) buttons.append(h("button", { onclick: async () => {
    if (!(await confirmBox("Delete profile?", `Delete the profile “${p.name}”? No files or folders are touched.`, "Delete profile", true))) return;
    await api("DELETE", "/api/profiles/" + p.id); location.hash = "#/profiles";
  } }, "Delete profile"));
  buttons.append(h("a", { class: "btn", href: "#/profiles" }, "Cancel"), h("button", { class: "primary", onclick: async () => {
    err.replaceChildren();
    try { delete p._t; await api("POST", "/api/profiles", p); toast("Profile saved"); location.hash = "#/profiles"; }
    catch (e) { err.replaceChildren(h("div", { class: "error", style: "margin-top:12px" }, e.message)); }
  } }, "Save"));
  setPage("profiles", h("h1", {}, p.id ? "Edit profile" : "New profile"), h("p", { class: "sub" }, "These become the defaults whenever you sort with this profile."),
    h("div", { class: "card" }, form), err, buttons);
}

// ---------- History ----------
async function pageHistory() {
  const here = stillHere();
  let hs;
  try { hs = await api("GET", "/api/history"); } catch (e) { if (here()) setPage("history", h("h1", {}, "Job history"), errBox(e)); return; }
  if (!here()) return;
  const t = vtable({ id: "history", rows: hs, height: 520, empty: "No sorts yet", onRow: e => (location.hash = "#/history/" + e.id), columns: [
    { label: "When", render: e => new Date(e.summary.started).toLocaleString(), sort: e => e.summary.started },
    { label: "Profile", render: e => e.profile, sort: e => e.profile },
    { label: "Status", render: e => stagePill(sortStatus(e.summary)), text: e => stageInfo[sortStatus(e.summary)][0], sort: e => sortStatus(e.summary) },
    { label: "Mode", render: e => (e.options.move ? "Move" : "Copy") },
    { label: "Sorted", render: e => num(e.summary.copied + e.summary.moved), sort: e => e.summary.copied + e.summary.moved },
    { label: "Size", render: e => bytes(e.summary.bytes), sort: e => e.summary.bytes },
    { label: "Speed", render: e => avgSpeed(e.summary) },
    { label: "Duplicates", render: e => num(e.summary.duplicates), sort: e => e.summary.duplicates },
    { label: "Failed", render: e => num(e.summary.failed), sort: e => e.summary.failed },
  ] });
  setPage("history", h("h1", {}, "Job history"), h("p", { class: "sub" }, "Every job you've approved, and how it ended. Click one to see what happened to each file."), t.el);
}

async function pageHistoryEntry(id) {
  const here = stillHere();
  let e;
  try { e = await api("GET", "/api/history/" + id); } catch (x) { if (here()) setPage("history", h("h1", {}, "Job history"), errBox(x)); return; }
  if (!here()) return;
  const res = e.results || [];
  let filter = "all";
  const statuses = ["copied", "moved", "duplicate", "ignored", "failed", "not run"].filter(s => res.some(r => r.status === s));
  const chips = h("div", { class: "chips" });
  const t = vtable({ id: "history-entry", height: 460, columns: [
    { label: "File", tail: true, render: r => r.src, sort: r => r.src },
    { label: "Result", render: r => r.status, sort: r => r.status },
    { label: "Now at / identical to", tail: true, render: r => disp(r.dest), sort: r => r.dest || "" },
    { label: "Note", render: r => r.error || reasonLabel[r.reason] || "", sort: r => r.error || r.reason || "" },
  ] });
  function refresh() {
    chips.replaceChildren(h("span", { class: "chip" + (filter === "all" ? " on" : ""), onclick: () => { filter = "all"; refresh(); } }, "All", h("span", { class: "n" }, num(res.length))),
      ...statuses.map(s => h("span", { class: "chip" + (filter === s ? " on" : ""), onclick: () => { filter = s; refresh(); } }, s, h("span", { class: "n" }, num(res.filter(r => r.status === s).length)))));
    t.setRows(filter === "all" ? res : res.filter(r => r.status === filter));
  }
  const s = e.summary;
  setPage("history", h("div", { class: "row" }, h("h1", {}, e.profile + " — " + new Date(s.started).toLocaleString()), stagePill(sortStatus(s))),
    h("p", { class: "sub mono", style: "margin-bottom:4px" }, e.options.sources.join(", ") + "  →  " + e.options.dest),
    h("p", { class: "sub" }, settingsLine(e.options, s.check || null)),
    h("div", { class: "metrics" }, metric(e.options.move ? "Moved" : "Copied", num(s.copied + s.moved), bytes(s.bytes)), metric("Duplicates", num(s.duplicates), "skipped"),
      metric("Renamed", num(s.renamed), "name taken"), metric("Failed", num(s.failed), "originals kept")),
    h("div", { style: "height:14px" }), chips, h("div", { style: "height:8px" }), t.el,
    h("div", { class: "row", style: "margin-top:12px" }, h("a", { class: "btn", href: "#/history" }, "Back"),
      INFO.canOpen ? h("button", { onclick: () => api("POST", "/api/open", { path: e.options.dest }).catch(x => toast(x.message)) }, "Open destination") : null));
  refresh();
}

// ---------- router ----------
function route() {
  navSeq++;
  window.onbeforeunload = null;
  const [path, qs] = (location.hash.slice(1) || "/").split("?");
  const q = new URLSearchParams(qs || "");
  const parts = path.split("/").filter(Boolean);
  const run = fn => Promise.resolve(fn()).catch(e => setPage("", errBox(e)));
  switch (parts[0]) {
    case "import": return run(() => pageImport(q));
    case "scan": return run(() => pageScan(parts[1]));
    case "preview": return run(() => pagePreview(parts[1]));
    case "run": return run(() => pageRun(parts[1]));
    case "jobs": return run(() => (parts[1] ? pageJob(parts[1]) : pageJobs()));
    case "leftovers": return run(() => pageLeftovers(q));
    case "profiles": return run(() => (parts[1] ? pageProfile(parts[1]) : pageProfiles()));
    case "history": return run(() => (parts[1] ? pageHistoryEntry(parts[1]) : pageHistory()));
    default: return run(pageNewJob);
  }
}

(async () => {
  try { INFO = await api("GET", "/api/info"); } catch { /* defaults */ }
  document.getElementById("version").textContent = "v" + INFO.version;
  const quit = document.getElementById("quit");
  if (INFO.canQuit) { quit.hidden = false; quit.onclick = () => api("POST", "/api/quit").then(() => document.body.replaceChildren(h("p", { style: "padding:40px" }, "Photo Sorter has closed. You can close this tab."))); }
  window.addEventListener("hashchange", route);
  updateJobBadge(); setInterval(updateJobBadge, 1500);
  route();
})();
