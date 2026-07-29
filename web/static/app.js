"use strict";

const $ = (sel) => document.querySelector(sel);
const page = document.body.dataset.page;


async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  let body = null;
  let parseFailed = false;
  try { body = await res.json(); } catch { parseFailed = true; }
  if (res.status === 401 && page === "main") {
    location.href = "/login";
    throw new Error("not logged in");
  }
  if (!res.ok) throw new Error((body && body.error) || `HTTP ${res.status}`);
  // A 200 with an unparsable or empty body used to be returned as null,
  // which crashed the first thing that read a property off it (e.g.
  // "Cannot read properties of null (reading 'series')") with no useful
  // message. Fail loudly here instead, once, with the real cause.
  if (body === null) {
    throw new Error(parseFailed
      ? "server returned an unreadable response (possibly truncated)"
      : "server returned an empty response");
  }
  return body;
}

function toLocalInput(d) {
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

// A <input type="datetime-local"> value is bare wall-clock text ("2026-07-29T18:54")
// with no timezone. Sending it as-is forced the server to guess which zone it
// meant, and it guessed its own -- so a browser even a few hours off from the
// server asked for a window in the server's future and got an empty result with
// no error. Attaching the browser's actual UTC offset makes the instant
// unambiguous end to end; the server already accepts RFC3339.
function inputToISO(val) {
  if (!val) return val;
  const d = new Date(val); // parsed in the browser's local zone
  if (isNaN(d)) return val; // let the server surface a parse error
  return d.toISOString();   // absolute UTC instant, e.g. 2026-07-29T09:54:00.000Z
}

// unitLabel gives the Y-axis a short, human header naming what the
// numbers actually mean, so nobody has to infer it from a bare suffix.
function unitLabel(unit) {
  switch (unit) {
    case "Kbyte": case "byte": return t("unit_size");
    case "Kbyte / sec": case "byte / sec": return t("unit_rate");
    case "millisec / second": return t("unit_cores");
    case "count / sec": return t("unit_per_sec");
    case "count": return t("unit_count");
    case "sec": return t("unit_duration");
    default: return "";
  }
}

function fmtNum(v) {
  if (v === null || v === undefined) return "—";
  const abs = Math.abs(v);
  if (abs >= 1e9) return (v / 1e9).toFixed(2) + "G";
  if (abs >= 1e6) return (v / 1e6).toFixed(2) + "M";
  if (abs >= 1e4) return (v / 1e3).toFixed(2) + "k";
  if (abs >= 100) return v.toFixed(1);
  if (abs >= 1) return v.toFixed(2);
  if (abs === 0) return "0";
  return v.toPrecision(3);
}

// fmtByUnit is unit-aware, unlike fmtNum's bare decimal-magnitude suffix.
// This exists because a raw Kbyte value formatted with fmtNum's generic
// K/M/G reads as a BYTE unit even though the suffix means "times a
// thousand", not "kilobytes" -- 13,800,000 (Kbyte) becomes "13.80M",
// which looks like "13.8 megabytes" but actually means 13.8 GB. A memory
// chart mislabelled this way can make a healthy machine look seconds
// from OOM. Every value that reaches the UI carries its PCP unit
// alongside it specifically so this class of misread can't happen.
function fmtByUnit(v, unit) {
  if (v === null || v === undefined) return "—";
  const abs = Math.abs(v);
  switch (unit) {
    case "Kbyte":
    case "Kbyte / sec": {
      const suffix = unit.endsWith("sec") ? "/s" : "";
      if (abs >= 1048576) return (v / 1048576).toFixed(2) + " GB" + suffix;
      if (abs >= 1024) return (v / 1024).toFixed(2) + " MB" + suffix;
      return v.toFixed(0) + " KB" + suffix;
    }
    case "byte":
    case "byte / sec": {
      const suffix = unit.endsWith("sec") ? "/s" : "";
      if (abs >= 1073741824) return (v / 1073741824).toFixed(2) + " GB" + suffix;
      if (abs >= 1048576) return (v / 1048576).toFixed(2) + " MB" + suffix;
      if (abs >= 1024) return (v / 1024).toFixed(2) + " KB" + suffix;
      return v.toFixed(0) + " B" + suffix;
    }
    case "millisec / second":
      // 1000 ms of CPU time per second of wall time == one core fully busy.
      // Expressed as core-equivalents rather than "% of a core": that
      // phrasing implies one specific core, which is wrong for an
      // aggregate metric like kernel.all.cpu.user (summed across every
      // core) -- 250 ms/s could mean one core 25% busy, or four cores
      // each 6% busy; the aggregate number can't distinguish those, so
      // the label shouldn't claim it can. "0.25 cores" is unambiguous
      // whether the metric is per-core or whole-machine.
      return (v / 1000).toFixed(2) + " cores";
    case "count / sec":
      return fmtNum(v) + "/s";
    case "count":
      return fmtNum(v);
    case "sec": {
      if (abs >= 86400) return (v / 86400).toFixed(1) + "d";
      if (abs >= 3600) return (v / 3600).toFixed(1) + "h";
      if (abs >= 60) return (v / 60).toFixed(1) + "m";
      return v.toFixed(0) + "s";
    }
    case "none":
    case "":
    case undefined:
      return fmtNum(v);
    default:
      return fmtNum(v) + " " + unit;
  }
}


if (page === "login") {
  initLang();
  (async () => {
    let needsSetup = false;
    try {
      const status = await api("/api/setup-status");
      needsSetup = !!status.needs_setup;
    } catch (e) { /* status check failed, fall back to the login form */ }
    $("#setupPanel").classList.toggle("hidden", !needsSetup);
    $("#loginForm").classList.toggle("hidden", needsSetup);
    (needsSetup ? $("#setupUsername") : $("#username")).focus();
  })();

  $("#loginForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errBox = $("#loginError");
    errBox.classList.add("hidden");
    try {
      await api("/api/login", {
        method: "POST",
        body: JSON.stringify({
          username: $("#username").value.trim(),
          password: $("#password").value,
        }),
      });
      location.href = "/";
    } catch (err) {
      errBox.textContent = err.message;
      errBox.classList.remove("hidden");
    }
  });

  $("#setupForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errBox = $("#setupError");
    errBox.classList.add("hidden");
    const username = $("#setupUsername").value.trim();
    const password = $("#setupPassword").value;
    if (password.length < 8) {
      errBox.textContent = "password must be at least 8 characters";
      errBox.classList.remove("hidden");
      return;
    }
    try {
      await api("/api/setup", { method: "POST", body: JSON.stringify({ username, password }) });
      // the account now exists; sign in with the same credentials right away
      await api("/api/login", { method: "POST", body: JSON.stringify({ username, password }) });
      location.href = "/";
    } catch (err) {
      errBox.textContent = err.message;
      errBox.classList.remove("hidden");
    }
  });
}



if (page === "main") {
  main().catch((e) => console.error(e));
}

let CAT = null;
let foldSet = new Set();

async function main() {
  const me = await api("/api/me");
  CAT = await api("/api/catalog");
  foldSet = new Set(CAT.metrics.filter((m) => m.fold).map((m) => m.metric));
  $("#userChip").textContent = me.user;
  $("#hostChip").textContent = me.archive;
  if (me.version) $("#verChip").textContent = "v" + me.version;
  initTheme();
  initLang();
  $("#exportLogBtn").addEventListener("click", exportDiagnosticLog);
  $("#logoutBtn").addEventListener("click", async () => {
    await api("/api/logout", { method: "POST" });
    location.href = "/login";
  });

  document.querySelectorAll(".tab").forEach((t) =>
    t.addEventListener("click", () => {
      document.querySelectorAll(".tab").forEach((x) => x.classList.toggle("is-active", x === t));
      $("#view-diag").classList.toggle("hidden", t.dataset.tab !== "diag");
      $("#view-diff").classList.toggle("hidden", t.dataset.tab !== "diff");
      $("#view-trend").classList.toggle("hidden", t.dataset.tab !== "trend");
      $("#view-proc").classList.toggle("hidden", t.dataset.tab !== "proc");
      $("#view-change").classList.toggle("hidden", t.dataset.tab !== "change");
      $("#view-reasoning").classList.toggle("hidden", t.dataset.tab !== "reasoning");
      if (t.dataset.tab === "trend") trendInit();
      if (t.dataset.tab === "proc") procInit();
      if (t.dataset.tab === "change") changeInit();
      if (t.dataset.tab === "diag") diagInit();
      if (t.dataset.tab === "reasoning") reasoningInit();
    })
  );

  diffInit();   // prepare the regression tab's window pickers
  trendInit();  // Performance Metrics (trends) is the default tab: draw it on load
  // Diagnose is no longer the landing tab, so it is not run on load -- its
  // tab handler calls diagInit() the first time it is opened. Running it
  // eagerly here would fire an /api/diagnose the user never asked for.
}


function setWindows(aS, aE, bS, bE) {
  $("#aStart").value = toLocalInput(aS);
  $("#aEnd").value = toLocalInput(aE);
  $("#bStart").value = toLocalInput(bS);
  $("#bEnd").value = toLocalInput(bE);
}

function diffInit() {
  const now = new Date();
  const hourStart = new Date(now); hourStart.setMinutes(0, 0, 0);
  const dayMs = 86400e3, hourMs = 3600e3;

  const presetYesterday = () => {
    const bS = new Date(hourStart - hourMs), bE = hourStart;
    setWindows(new Date(bS - dayMs), new Date(bE - dayMs), bS, bE);
  };
  presetYesterday();

  $("#presetYesterday").addEventListener("click", presetYesterday);
  $("#presetPrevHour").addEventListener("click", () => {
    const bS = new Date(hourStart - hourMs), bE = hourStart;
    setWindows(new Date(bS - hourMs), bS, bS, bE);
  });
  $("#presetPrevDayFull").addEventListener("click", () => {
    const todayStart = new Date(now); todayStart.setHours(0, 0, 0, 0);
    setWindows(new Date(todayStart - dayMs), todayStart, todayStart, now);
  });

  $("#runDiff").addEventListener("click", runDiff);
  $("#onlyExceeded").addEventListener("change", () => {
    if (lastReport) renderReport(lastReport);
  });
}

let lastReport = null;
let lastProcReport = null;
let lastChangeReport = null;
let lastDiagnosis = null;

// exportDiagnosticLog bundles whatever the operator has already run in
// this session -- regression diff, trends, process accounting, change
// accounting, one-click diagnosis -- into a single JSON file. Intended
// for handing raw comparison data to a colleague or an AI assistant for
// analysis, so it's the underlying structured data, not a formatted
// report: every number, verdict, and evidence string the UI itself
// used to render, with nothing summarized away.
function exportDiagnosticLog() {
  const bundle = {
    exported_at: new Date().toISOString(),
    host: $("#hostChip") ? $("#hostChip").textContent : undefined,
    version: $("#verChip") ? $("#verChip").textContent : undefined,
  };
  if (lastDiagnosis) bundle.diagnose = lastDiagnosis;
  if (lastReport) bundle.regression_diff = lastReport;
  if (lastProcReport) bundle.process_accounting = lastProcReport;
  if (lastChangeReport) bundle.change_accounting = lastChangeReport;

  const hasAny = lastDiagnosis || lastReport || lastProcReport || lastChangeReport;
  if (!hasAny) {
    alert(t("export_empty"));
    return;
  }

  const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  a.href = url;
  a.download = `deltascope-diagnostic-${stamp}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// Tab switching only toggles visibility of already-rendered DOM; static
// chrome (labels, buttons) picks up a language change immediately via
// applyStaticI18n(), but a report rendered BEFORE the switch stays in
// whatever language it was rendered in until something re-runs its
// render function. Re-render every tab that has cached data, using the
// cached response rather than a fresh API call, so switching language
// does not also re-run the underlying PCP/snapshot query.
document.addEventListener("dscope-lang-changed", () => {
  if (lastReport) renderReport(lastReport);
  if (lastProcReport) renderProcDiff(lastProcReport);
  if (lastChangeReport) renderStateDiff(lastChangeReport);
  if (lastDiagnosis) renderDiagnosis(lastDiagnosis);
  if (lastReasoning) renderReasoning(lastReasoning);
  if (typeof chart !== "undefined" && chart && typeof curPreset !== "undefined" && curPreset) loadTrend();
});

async function runDiff() {
  const btn = $("#runDiff");
  const errBox = $("#diffError");
  errBox.classList.add("hidden");
  btn.disabled = true;
  btn.textContent = t("comparing");
  try {
    const q = new URLSearchParams({
      a_start: inputToISO($("#aStart").value), a_end: inputToISO($("#aEnd").value),
      b_start: inputToISO($("#bStart").value), b_end: inputToISO($("#bEnd").value),
      threshold: $("#threshold").value || "15",
    });
    const rep = await api("/api/diff?" + q.toString());
    lastReport = rep;
    renderReport(rep);
  } catch (err) {
    errBox.textContent = err.message;
    errBox.classList.remove("hidden");
  } finally {
    btn.disabled = false;
    btn.textContent = t("run_comparison");
  }
}

const KIND = {
  worse:  { icon: "\u{1F534}", key: "verdict_worse", cls: "v-worse", rgb: "255,93,108" },
  better: { icon: "\u{1F7E2}", key: "verdict_better", cls: "v-better", rgb: "61,220,151" },
  watch:  { icon: "\u{1F7E1}", key: "verdict_watch", cls: "v-watch", rgb: "232,197,71" },
  flat:   { icon: "\u00B7",   key: "verdict_flat", cls: "v-flat", rgb: null },
  new:    { icon: "\u2295",   key: "verdict_appeared", cls: "v-new", rgb: "178,141,255" },
  gone:   { icon: "\u2296",   key: "verdict_gone", cls: "v-gone", rgb: "131,145,173" },
};
const KIND_RANK = { worse: 0, new: 1, watch: 2, gone: 3, better: 4, flat: 5 };

function rowKind(r) {
  if (r.a === null && r.b !== null) return "new";
  if (r.b === null && r.a !== null) return "gone";
  return r.verdict;
}

function absD(r) {
  if (r.delta_pct !== null && r.delta_pct !== undefined) return Math.abs(r.delta_pct);
  if (r.a === null || r.b === null) return Infinity;
  return 0;
}

const SEV_KEYS = { crit: "sev_crit_short", warn: "sev_warn_short", info: "sev_info_short" };

const TRIAGE_ICON = {
  cpu: "\u{1F5A5}\uFE0F", mem: "\u{1F9E0}", disk: "\u{1F4BE}", net: "\u{1F310}", ghost: "\u{1F47B}",
};
const TRIAGE_STATUS = {
  bad:  { cls: "t-bad",  dot: "\u{1F534}" },
  warn: { cls: "t-warn", dot: "\u{1F7E1}" },
  ok:   { cls: "t-ok",   dot: "\u{1F7E2}" },
};

function renderTriage(triage, rows) {
  const board = $("#triageBoard");
  if (!triage || !triage.length) { board.innerHTML = ""; return; }

  const cards = triage.map((b) => {
    const st = TRIAGE_STATUS[b.status] || TRIAGE_STATUS.ok;
    const jump = b.status !== "ok"
      ? `<button class="triage-jump" data-res="${b.key}">${t("details_down")}</button>` : "";
    return `<div class="triage-card ${st.cls}" data-res="${b.key}">
      <div class="tc-top"><span class="tc-icon">${TRIAGE_ICON[b.key]||""}</span>
        <span class="tc-label">${escapeHtml(b.label)}</span><span class="tc-dot">${st.dot}</span></div>
      <div class="tc-headline">${escapeHtml(b.headline)}</div>
      ${jump}
    </div>`;
  });

  // Fifth card: "the software gremlin" -- summarizes diagnosis rule hits and links onward
  const findings = window._lastFindings || [];
  const ghostBits = [];
  let ghostStatus = "ok";
  if (findings.length) {
    const crit = findings.filter((f) => f.severity === "crit").length;
    ghostStatus = crit ? "bad" : "warn";
    ghostBits.push(t("diagnosis_hits", findings.length));
  }
  const ghostSt = TRIAGE_STATUS[ghostStatus];
  const ghostHead = ghostBits.length ? ghostBits.join(" · ") : t("no_anomalies");
  cards.push(`<div class="triage-card ${ghostSt.cls}" data-res="ghost">
    <div class="tc-top"><span class="tc-icon">${TRIAGE_ICON.ghost}</span>
      <span class="tc-label">${t("software_gremlin")}</span><span class="tc-dot">${ghostSt.dot}</span></div>
    <div class="tc-headline">${escapeHtml(ghostHead)}</div>
    <div class="tc-ghost-links">
      <button class="triage-jump" data-tab-jump="proc">${t("view_processes")}</button>
    </div>
  </div>`);

  board.innerHTML = cards.join("");

  board.querySelectorAll(".triage-jump[data-res]").forEach((btn) =>
    btn.addEventListener("click", () => {
      const res = btn.dataset.res;
      const cats = { cpu: ["CPU"], mem: ["Memory"], disk: ["Disk I/O", "Filesystem"], net: ["Network"] }[res];
      // Matched by the exact category the block was rendered with, not by
      // scanning summary text: "聚焦模式" (only-exceeded) can filter an
      // entire category down to zero rows, in which case its <details>
      // block was never rendered at all. Scanning text silently did
      // nothing in that case -- the button looked broken with no feedback.
      const el = [...document.querySelectorAll(".cat-block[data-category]")]
        .find((d) => cats.includes(d.dataset.category));
      if (el) {
        el.open = true;
        el.scrollIntoView({ behavior: "smooth", block: "start" });
      } else {
        // The category's rows all got filtered out by focus mode. Turn
        // it off and retry once rather than leaving the click looking
        // like a no-op.
        const onlyExceeded = $("#onlyExceeded");
        if (onlyExceeded && onlyExceeded.checked) {
          onlyExceeded.checked = false;
          renderReport(lastReport);
          const retry = [...document.querySelectorAll(".cat-block[data-category]")]
            .find((d) => cats.includes(d.dataset.category));
          if (retry) { retry.open = true; retry.scrollIntoView({ behavior: "smooth", block: "start" }); }
        }
      }
    }));
  board.querySelectorAll(".triage-jump[data-tab-jump]").forEach((btn) =>
    btn.addEventListener("click", () => {
      const tab = [...document.querySelectorAll(".tab")].find((t) => t.dataset.tab === btn.dataset.tabJump);
      if (tab) tab.click();
    }));
}

function renderFindings(findings) {
  window._lastFindings = findings || [];
  const box = $("#findings");
  if (!findings || !findings.length) {
    box.innerHTML = `<div class="no-finding">${t("no_finding")}</div>`;
    return;
  }
  const order = { crit: 0, warn: 1, info: 2 };
  const sorted = [...findings].sort((a, b) => order[a.severity] - order[b.severity]);
  box.innerHTML = sorted.map((f) => `
    <div class="finding f-${f.severity}">
      <div class="finding-head">
        <span class="sev sev-${f.severity}">${SEV_KEYS[f.severity] ? t(SEV_KEYS[f.severity]) : f.severity}</span>
        <span class="finding-conclusion">${escapeHtml(f.conclusion)}</span>
      </div>
      <div class="finding-evidence">${t("evidence_label")} ${f.evidence.map(escapeHtml).join(" · ")}</div>
      ${f.next && f.next.length ? `<div class="finding-next">${t("next_label")} ${f.next.map((c) => `<code>${escapeHtml(c)}</code>`).join("")}</div>` : ""}
    </div>`).join("");
}

function renderTop5(rows) {
  const box = $("#top5");
  const worst = rows.filter((r) => rowKind(r) === "worse")
    .sort((a, b) => absD(b) - absD(a)).slice(0, 5);
  if (!worst.length) { box.innerHTML = ""; return; }
  box.innerHTML = worst.map((r) => {
    const d = r.delta_pct === null ? "\u221E" : (r.delta_pct > 0 ? "+" : "") + r.delta_pct.toFixed(0) + "%";
    const inst = r.instance ? `[${escapeHtml(r.instance)}]` : "";
    return `<button class="chip" data-target="${r._id}">${escapeHtml(r.label)}${inst} ${d}</button>`;
  }).join("");
  box.querySelectorAll(".chip").forEach((c) =>
    c.addEventListener("click", () => {
      const el = document.getElementById(c.dataset.target);
      if (el) { el.scrollIntoView({ behavior: "smooth", block: "center" }); el.classList.add("row-flash"); setTimeout(() => el.classList.remove("row-flash"), 1600); }
    }));
}

function statsCaption(min, max, count, unit) {
  if (min === null || min === undefined) return "";
  return t("min_max_n", fmtByUnit(min, unit), fmtByUnit(max, unit), count);
}

function rowHTML(r, kind, extraCls, hiddenAttr) {
  const k = KIND[kind];
  let deltaTxt, barHtml = "";
  if (kind === "new") deltaTxt = "\u2295";
  else if (kind === "gone") deltaTxt = "\u2296";
  else if (r.delta_pct === null) deltaTxt = "\u221E";
  else {
    deltaTxt = (r.delta_pct > 0 ? "+" : "") + r.delta_pct.toFixed(1) + "%";
    const pct = Math.min(50, absD(r) / renderScale * 50);
    barHtml = `<span class="delta-bar-wrap"><span class="delta-bar ${r.delta_pct >= 0 ? "up" : "down"}" data-w="${pct.toFixed(2)}"></span></span>`;
  }
  const inst = r.instance ? ` <code>[${escapeHtml(r.instance)}]</code>` : "";
  const bg = k.rgb && isFinite(absD(r)) && absD(r) > 0
    ? `rgba(${k.rgb},${Math.min(0.05 + absD(r) / renderScale * 0.16, 0.22).toFixed(3)})`
    : (k.rgb && !isFinite(absD(r)) ? `rgba(${k.rgb},0.18)` : "");
  const aStats = statsCaption(r.a_min, r.a_max, r.a_count, r.units);
  const bStats = statsCaption(r.b_min, r.b_max, r.b_count, r.units);
  const statsLine = (aStats || bStats)
    ? `<span class="m-stats">${aStats ? "A " + aStats : ""}${aStats && bStats ? " \u00b7 " : ""}${bStats ? "B " + bStats : ""}</span>`
    : "";
  return `<tr id="${r._id}" class="${k.cls}${extraCls}"${hiddenAttr}${bg ? ` data-bg="${bg}"` : ""}>
    <td class="metric-cell">
      <span class="m-label">${k.icon} ${escapeHtml(r.label)}${inst}</span>
      <span class="m-name">${escapeHtml(r.metric)}${statsLine ? " · " + statsLine : ""}</span>
    </td>
    <td class="col-a">${fmtByUnit(r.a, r.units)}</td>
    <td class="col-b">${fmtByUnit(r.b, r.units)}</td>
    <td class="delta-cell">${deltaTxt}${barHtml}</td>
    <td>${t(k.key)}</td>
    <td class="units-cell">${escapeHtml(r.units || "")}</td>
  </tr>`;
}

let renderScale = 100;

function renderReport(rep) {
  $("#diffEmpty").classList.add("hidden");
  $("#diffResult").classList.remove("hidden");

  rep.rows.forEach((r, i) => { r._id = "row-" + i; });
  window._lastFindings = rep.findings || [];
  renderTriage(rep.triage, rep.rows);
  renderFindings(rep.findings);

  const counts = { worse: 0, better: 0, watch: 0, flat: 0, new: 0, gone: 0 };
  rep.rows.forEach((r) => counts[rowKind(r)]++);

  const w = rep.window;
  const fmtW = (s, e) => `${s.slice(5, 16).replace("T", " ")} \u2192 ${e.slice(11, 16)}`;
  const extra = (counts.new ? ` <span class="verdict-pill pill-new">\u2295 appeared <b>${counts.new}</b></span>` : "") +
                (counts.gone ? ` <span class="verdict-pill pill-flat">\u2296 gone <b>${counts.gone}</b></span>` : "");
  $("#verdictStrip").innerHTML = `
    <span class="verdict-pill pill-bad">\u{1F534} worse <b>${counts.worse}</b></span>
    <span class="verdict-pill pill-good">\u{1F7E2} better <b>${counts.better}</b></span>
    <span class="verdict-pill pill-warn">\u{1F7E1} watch <b>${counts.watch}</b></span>
    <span class="verdict-pill pill-flat">flat <b>${counts.flat}</b></span>${extra}
    <span class="verdict-window">
      <span class="wa">[A ${fmtW(w.a_start, w.a_end)}]</span> vs
      <span class="wb">[B ${fmtW(w.b_start, w.b_end)}]</span> \u00B7 threshold ${w.threshold_pct}%
    </span>`;

  renderTop5(rep.rows);

  const focus = $("#onlyExceeded").checked;
  const finiteMax = rep.rows.filter((r) => r.delta_pct !== null).map((r) => Math.abs(r.delta_pct));
  renderScale = Math.max(100, ...finiteMax);

  const byCat = new Map();
  rep.rows.forEach((r) => {
    if (focus && rowKind(r) === "flat") return;
    if (!byCat.has(r.category)) byCat.set(r.category, []);
    byCat.get(r.category).push(r);
  });

  const blocks = [];
  for (const [cat, rows] of byCat) {
    const trs = [];
    let i = 0;
    while (i < rows.length) {
      const r = rows[i];
      if (foldSet.has(r.metric)) {
        let j = i;
        while (j < rows.length && rows[j].metric === r.metric) j++;
        const grp = rows.slice(i, j);
        if (grp.length > 4) {
          const worst = [...grp].sort((a, b) =>
            KIND_RANK[rowKind(a)] - KIND_RANK[rowKind(b)] || absD(b) - absD(a))[0];
          const wk = rowKind(worst);
          const bMax = Math.max(...grp.map((x) => x.b ?? -Infinity));
          const bMin = Math.min(...grp.map((x) => x.b ?? Infinity));
          const wd = worst.delta_pct === null ? "\u221E" : (worst.delta_pct > 0 ? "+" : "") + worst.delta_pct.toFixed(1) + "%";
          trs.push(`<tr class="fold-agg ${KIND[wk].cls}" data-fold="${escapeHtml(r.metric)}">
            <td class="metric-cell"><span class="m-label">${KIND[wk].icon} ${escapeHtml(r.label)} <code>${grp.length} ${t("instances")}</code></span>
            <span class="m-name">${escapeHtml(r.metric)} \u00B7 B \u6781\u5DEE ${fmtByUnit(bMin, r.units)} ~ ${fmtByUnit(bMax, r.units)}</span></td>
            <td class="col-a"></td><td class="col-b">${fmtByUnit(bMax, r.units)}</td>
            <td class="delta-cell">\u6700\u5DEE ${wd}</td><td>${t(KIND[wk].key)}</td><td class="units-cell">${escapeHtml(r.units || "")}</td></tr>`);
          grp.forEach((x) => trs.push(rowHTML(x, rowKind(x), " fold-child", " hidden")));
          i = j;
          continue;
        }
      }
      trs.push(rowHTML(r, rowKind(r), "", ""));
      i++;
    }
    blocks.push(`<details class="cat-block" open data-category="${escapeHtml(cat)}">
      <summary class="cat-head"><span>${escapeHtml(cat)}</span><span>${rows.length} ${t("items_shown")}</span></summary>
      <table class="report">
        <thead><tr><th>${t("th_metric")}</th><th>${t("th_a_mean")}</th><th>${t("th_b_mean")}</th><th>${t("th_delta")}</th><th>${t("th_verdict")}</th><th>${t("th_unit")}</th></tr></thead>
        <tbody>${trs.join("")}</tbody>
      </table>
    </details>`);
  }
  $("#reportTables").innerHTML = blocks.length
    ? blocks.join("")
    : `<div class="empty-hint">No rows to show in focus mode -- turn it off to see all ${rep.rows.length} rows.</div>`;

  document.querySelectorAll(".delta-bar[data-w]").forEach((el) => { el.style.width = el.dataset.w + "%"; });
  document.querySelectorAll("tr[data-bg]").forEach((el) => { el.style.backgroundColor = el.dataset.bg; });
  document.querySelectorAll(".fold-agg").forEach((agg) =>
    agg.addEventListener("click", () => {
      agg.classList.toggle("open");
      let sib = agg.nextElementSibling;
      while (sib && sib.classList.contains("fold-child")) {
        sib.hidden = !sib.hidden;
        sib = sib.nextElementSibling;
      }
    }));

  const warnBox = $("#warnBox");
  if (rep.warnings && rep.warnings.length) {
    warnBox.classList.remove("hidden");
    const names = (rep.absent && rep.absent.length ? rep.absent : [...new Set(rep.warnings.map((w) => {
      const m = w.match(/for ([\w.]+):/);
      return m ? m[1] : w;
    }))]).sort();
    $("#warnSummary").textContent =
      `${names.length} metric(s) absent from this archive \u2014 now skipped automatically`;
    $("#warnPre").innerHTML =
      `These are not recorded by the local pmlogger, so they are excluded from\n` +
      `further queries and do not affect the rest of the report. To collect them,\n` +
      `apply the tiered sampling config from deploy.sh and restart pmlogger.\n\n` +
      names.map(escapeHtml).join("\n");
  } else {
    warnBox.classList.add("hidden");
  }
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}


let chart = null;
let trendReady = false;
let curPreset = "cpu";

async function trendInit() {
  if (trendReady) { chart && chart.resize(); return; }
  trendReady = true;

  const cat = CAT || (CAT = await api("/api/catalog"));
  const seg = $("#presetSeg");
  const order = ["cpu", "percpu", "load", "ctx", "mem", "memdet", "swap", "disk", "diskio", "net", "tcp", "sock", "psi"];
  order.forEach((key) => {
    if (!cat.presets[key]) return;
    const b = document.createElement("button");
    b.className = "seg-btn" + (key === curPreset ? " is-active" : "");
    b.textContent = cat.presets[key].label;
    b.dataset.preset = key;
    b.addEventListener("click", () => {
      curPreset = key;
      seg.querySelectorAll(".seg-btn").forEach((x) => x.classList.toggle("is-active", x === b));
      loadTrend();
    });
    seg.appendChild(b);
  });

  document.querySelectorAll("#rangeSeg .seg-btn").forEach((b) =>
    b.addEventListener("click", () => {
      document.querySelectorAll("#rangeSeg .seg-btn").forEach((x) => x.classList.toggle("is-active", x === b));
      const end = new Date();
      const start = new Date(end - Number(b.dataset.hours) * 3600e3);
      $("#tStart").value = toLocalInput(start);
      $("#tEnd").value = toLocalInput(end);
      loadTrend();
    })
  );
  $("#applyRange").addEventListener("click", loadTrend);
  $("#overlayYesterday").addEventListener("change", loadTrend);

  const end = new Date();
  $("#tStart").value = toLocalInput(new Date(end - 6 * 3600e3));
  $("#tEnd").value = toLocalInput(end);

  chart = echarts.init($("#chart"));
  window.addEventListener("resize", () => chart.resize());
  loadTrend();
}

const PALETTE = ["#4cc9f0", "#e8a33d", "#3ddc97", "#ff5d6c", "#b28dff", "#e8c547"];

async function loadTrend() {
  const errBox = $("#trendError");
  errBox.classList.add("hidden");
  const lt = ["theme-nova-light", "theme-terra-light", "theme-iris-light"]
    .some((c) => document.body.classList.contains(c));
  chart.showLoading({ text: t("reading_archive"), color: lt ? "#0b7fa8" : "#4cc9f0", textColor: lt ? "#6a7489" : "#8391ad", maskColor: lt ? "rgba(246,247,251,.6)" : "rgba(13,19,34,.6)" });
  try {
    const q = new URLSearchParams({
      preset: curPreset,
      start: inputToISO($("#tStart").value),
      end: inputToISO($("#tEnd").value),
    });
    const data = await api("/api/trend?" + q.toString());
    let yesterday = null;
    if ($("#overlayYesterday").checked) {
      const dayMs = 86400e3;
      const ys = new Date(new Date($("#tStart").value) - dayMs);
      const ye = new Date(new Date($("#tEnd").value) - dayMs);
      const qy = new URLSearchParams({ preset: curPreset, start: inputToISO(toLocalInput(ys)), end: inputToISO(toLocalInput(ye)) });
      try {
        const yd = await api("/api/trend?" + qy.toString());
        // shift yesterday's timestamps forward one day to overlay on today's axis
        yesterday = yd.series.map((s) => ({
          name: s.name,
          unit: s.unit,
          points: s.points.map((p) => [p[0] + dayMs, p[1]]),
        }));
      } catch (e) { /* no data for yesterday, ignore */ }
    }
    drawChart(data.series, yesterday);
    const note = $("#trendNote");
    if (data.missing && data.missing.length) {
      note.textContent = `${data.missing.length} metric(s) not found in the archive, skipped: ${data.missing.join(", ")}`;
      note.classList.remove("hidden");
    } else {
      note.classList.add("hidden");
    }
  } catch (err) {
    chart.hideLoading();
    errBox.textContent = err.message;
    errBox.classList.remove("hidden");
  }
}

function drawChart(series, yesterday) {
  chart.hideLoading();

  // Every series in a preset shares one unit by construction (presets are
  // built from same-unit metrics specifically so a shared Y-axis is never
  // mixing Kbyte with count/sec). Look it up once for the axis label and
  // per-series for the tooltip, in case a future preset isn't homogeneous.
  const unitByName = {};
  series.forEach((s) => { unitByName[s.name] = s.unit || "none"; });
  const axisUnit = series.length ? (series[0].unit || "none") : "none";

  const opt = {
    backgroundColor: "transparent",
    color: PALETTE,
    textStyle: { color: "#8391ad", fontFamily: "ui-monospace, Menlo, Consolas, monospace" },
    tooltip: {
      trigger: "axis",
      backgroundColor: "#1a2440", borderColor: "#263354",
      textStyle: { color: "#dbe4f5", fontSize: 12 },
      formatter: (params) => {
        if (!params.length) return "";
        const time = new Date(params[0].axisValue).toLocaleString();
        const rows = params.map((p) => {
          const v = Array.isArray(p.data) ? p.data[1] : p.data;
          const unit = unitByName[p.seriesName] || "none";
          return `<div style="display:flex;justify-content:space-between;gap:16px;">
            <span>${p.marker} ${escapeHtml(p.seriesName)}</span>
            <span>${escapeHtml(fmtByUnit(v, unit))}</span></div>`;
        });
        return `<div style="font-family:var(--mono);font-size:11px;color:#8391ad;margin-bottom:4px;">${time}</div>${rows.join("")}`;
      },
    },
    legend: {
      top: 0, textStyle: { color: "#8391ad" }, icon: "roundRect",
      formatter: (name) => name,
    },
    grid: { left: 68, right: 24, top: 40, bottom: 76 },
    xAxis: {
      type: "time",
      axisLine: { lineStyle: { color: "#263354" } },
      splitLine: { show: false },
    },
    yAxis: {
      type: "value",
      name: unitLabel(axisUnit),
      nameLocation: "end",
      nameTextStyle: { color: "#8391ad", fontSize: 10.5, align: "left" },
      axisLabel: { formatter: (v) => fmtByUnit(v, axisUnit) },
      splitLine: { lineStyle: { color: "rgba(122,152,210,.1)" } },
    },
    dataZoom: [
      { type: "inside", throttle: 60 },
      { type: "slider", height: 26, bottom: 12, borderColor: "#263354",
        backgroundColor: "rgba(20,28,48,.6)", fillerColor: "rgba(76,201,240,.15)",
        handleStyle: { color: "#4cc9f0" }, textStyle: { color: "#8391ad" } },
    ],
    series: series.map((s, i) => ({
      name: s.name,
      type: "line",
      showSymbol: false,
      connectNulls: false,
      lineStyle: { width: 1.6 },
      areaStyle: series.length <= 2 && !yesterday ? { opacity: 0.12 } : undefined,
      emphasis: { focus: "series" },
      data: s.points,
    })),
  };
  if (yesterday && yesterday.length) {
    const idxByName = {};
    series.forEach((s, i) => { idxByName[s.name] = i; });
    yesterday.forEach((y) => {
      const i = idxByName[y.name] ?? 0;
      unitByName[y.name + " (yesterday)"] = y.unit || unitByName[y.name] || "none";
      opt.series.push({
        name: y.name + " (yesterday)",
        type: "line",
        showSymbol: false,
        connectNulls: false,
        lineStyle: { width: 1.4, type: "dashed", opacity: 0.7, color: PALETTE[i % PALETTE.length] },
        itemStyle: { color: PALETTE[i % PALETTE.length] },
        emphasis: { focus: "series" },
        data: y.points,
      });
    });
    opt.legend.data = [...series.map((s) => s.name), ...yesterday.map((y) => y.name + " (yesterday)")];
  }
  chart.setOption(opt, true);
}


let procReady = false;

function procInit() {
  if (procReady) return;
  procReady = true;

  const now = new Date();
  const hourStart = new Date(now); hourStart.setMinutes(0, 0, 0);
  const dayMs = 86400e3, hourMs = 3600e3;
  const fmt = (d) => {
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth()+1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  };
  const setWin = () => {
    const bS = new Date(hourStart - hourMs), bE = hourStart;
    $("#pcAStart").value = fmt(new Date(bS - dayMs));
    $("#pcAEnd").value = fmt(new Date(bE - dayMs));
    $("#pcBStart").value = fmt(bS);
    $("#pcBEnd").value = fmt(bE);
  };
  setWin();
  $("#pcYesterday").addEventListener("click", setWin);
  $("#pcRun").addEventListener("click", runProcDiff);
}

async function runProcDiff() {
  const btn = $("#pcRun");
  const err = $("#procError");
  err.classList.add("hidden");
  btn.disabled = true; btn.textContent = t("accounting");
  try {
    const q = new URLSearchParams({
      a_start: inputToISO($("#pcAStart").value), a_end: inputToISO($("#pcAEnd").value),
      b_start: inputToISO($("#pcBStart").value), b_end: inputToISO($("#pcBEnd").value),
    });
    const rep = await api("/api/procdiff?" + q.toString());
    lastProcReport = rep;
    renderProcDiff(rep);
    $("#procHint").classList.add("hidden");
  } catch (e) {
    err.textContent = e.message;
    err.classList.remove("hidden");
    $("#procResult").innerHTML = "";
  } finally {
    btn.disabled = false; btn.textContent = t("run_accounting");
  }
}

const PV = {
  worse:    { icon: "\u{1F534}", key: "verdict_worse", cls: "v-worse" },
  better:   { icon: "\u{1F7E2}", key: "verdict_better", cls: "v-better" },
  // Status dots, matching the 🔴/🟢 language of the other verdicts. The old
  // ⊕/⊖ glyphs read as expand/collapse controls -- users tried to click them
  // to open a subtree that does not exist.
  appeared: { icon: "\u{1F7E3}", key: "verdict_appeared", cls: "v-new" },
  gone:     { icon: "\u26AA", key: "verdict_gone", cls: "v-gone" },
  flat:     { icon: "\u00B7", key: "verdict_flat", cls: "v-flat" },
};

function pctVal(v) { return v === null || v === undefined ? "\u2014" : v.toFixed(1) + "%"; }
// A process born inside the window has no baseline reading, so its CPU
// figure is a lifetime average rather than a rate measured over the window.
// It is the best number available and must not be presented as if it were
// measured, hence the tilde and the tooltip.
function pctValApprox(v, approx) {
  if (v === null || v === undefined) return "\u2014";
  if (!approx) return pctVal(v);
  return `<span class="approx" title="${escapeHtml(t("approx_hint"))}">~${v.toFixed(1)}%</span>`;
}
function deltaVal(v) {
  if (v === null || v === undefined) return "\u2014";
  return (v > 0 ? "+" : "") + v.toFixed(0) + "%";
}
// A process that rose from an idle baseline has no percentage change to
// show -- the change from zero is infinite. Printing an em dash there makes
// a flagged row look flagged for no reason, so the reason is named instead.
// The two value columns already carry the numbers.
function deltaValFrom(v, fromZero) {
  if ((v === null || v === undefined) && fromZero) {
    return `<span class="from-idle">${t("from_idle")}</span>`;
  }
  return deltaVal(v);
}
function memVal(kb) {
  if (kb === null || kb === undefined) return "\u2014";
  if (kb >= 1048576) return (kb / 1048576).toFixed(1) + "G";
  if (kb >= 1024) return (kb / 1024).toFixed(0) + "M";
  return kb.toFixed(0) + "K";
}

function renderProcDiff(rep) {
  if (rep.no_data) {
    $("#procResult").innerHTML =
      `<div class="no-finding" style="line-height:1.7">${escapeHtml(rep.no_data_hint || t("no_process_data"))}</div>`;
    return;
  }
  const rows = rep.rows || [];
  const active = rows.filter((r) => r.verdict !== "flat");
  let html = "";

  if (rep.restarts && rep.restarts.length) {
    html += `<div class="restart-banner"><b>\u27F3 ${t("restart_banner")}</b> ` +
      rep.restarts.map((r) => `<span class="restart-chip">${escapeHtml(r.name)}</span>`).join("") +
      `</div>`;
  }

  html += `<div class="change-window">A ${new Date(rep.a_start).toLocaleString()} &rarr; ${new Date(rep.a_end).toLocaleTimeString()} ` +
          `vs B ${new Date(rep.b_start).toLocaleString()} &rarr; ${new Date(rep.b_end).toLocaleTimeString()}</div>`;

  if (!active.length) {
    html += `<div class="no-finding" style="margin-top:10px">\u2705 ${t("no_significant_change")}</div>`;
    $("#procResult").innerHTML = html;
    return;
  }

  const trs = active.map((r) => {
    const v = PV[r.verdict] || PV.flat;
    const mark = r.restarted ? ` <span class="restart-tag">\u27F3</span>` : "";
    const inst = r.instances > 1 ? ` <code>${r.instances}\u00d7</code>` : "";
    return `<tr class="${v.cls}">
      <td class="proc-name"><span class="p-dot">${v.icon}</span>${escapeHtml(r.name)}${mark}${inst}</td>
      <td>${pctVal(r.cpu_pct_a)}</td><td>${pctValApprox(r.cpu_pct_b, r.cpu_approx_b)}</td>
      <td class="delta-cell">${deltaValFrom(r.cpu_delta_pct, r.from_zero)}</td>
      <td>${memVal(r.rss_kb_a)}</td><td>${memVal(r.rss_kb_b)}</td>
      <td class="delta-cell">${deltaValFrom(r.rss_delta_pct, r.from_zero)}</td>
      <td>${t(v.key)}</td>
    </tr>`;
  }).join("");

  html += `<div class="cat-block"><div class="cat-head">
      <span>${t("process_cpu_accounting")}</span><span>${active.length} / ${rows.length} ${t("changed_label").toLowerCase()}</span></div>
    <table class="report"><thead><tr>
      <th>${t("th_process")}</th><th>${t("th_cpu_a")}</th><th>${t("th_cpu_b")}</th><th>${t("th_dcpu")}</th>
      <th>${t("th_mem_a")}</th><th>${t("th_mem_b")}</th><th>${t("th_dmem")}</th><th>${t("th_verdict")}</th>
    </tr></thead><tbody>${trs}</tbody></table></div>`;

  $("#procResult").innerHTML = html;
}

let changeReady = false;

function changeInit() {
  if (changeReady) return;
  changeReady = true;
  $("#changeRun").addEventListener("click", runStateDiff);
  runStateDiff();
}

async function runStateDiff() {
  const btn = $("#changeRun");
  const err = $("#changeError");
  err.classList.add("hidden");
  btn.disabled = true; btn.textContent = t("checking");
  try {
    const since = $("#changeSince").value;
    const rep = await api("/api/statediff?since=" + encodeURIComponent(since));
    lastChangeReport = rep;
    renderStateDiff(rep);
  } catch (e) {
    err.textContent = e.message;
    err.classList.remove("hidden");
    $("#changeResult").innerHTML = "";
  } finally {
    btn.disabled = false; btn.textContent = t("check_for_changes");
  }
}

const CHANGE_KIND = {
  added:    { icon: "\u{1F7E2}", cls: "v-new", key: "change_added" },
  modified: { icon: "\u{1F7E1}", cls: "v-watch", key: "change_modified" },
  removed:  { icon: "\u26AA",   cls: "v-gone", key: "change_removed" },
};

function renderStateDiff(rep) {
  const box = $("#changeResult");
  const fmtT = (s) => new Date(s).toLocaleString();
  const header = `<div class="change-window">A ${fmtT(rep.a_time)} &rarr; B ${fmtT(rep.b_time)}</div>`;

  if (!rep.total) {
    box.innerHTML = header + `<div class="no-finding" style="margin-top:10px">${t("no_change_hint")}</div>`;
    return;
  }

  const sections = rep.sections.map((sec) => {
    const rows = sec.changes.map((ch) => {
      const k = CHANGE_KIND[ch.kind] || CHANGE_KIND.modified;
      let detail;
      if (ch.kind === "added") detail = `<code>${escapeHtml(ch.new)}</code>`;
      else if (ch.kind === "removed") detail = `<span class="was">${t("was_label")} <code>${escapeHtml(ch.old)}</code></span>`;
      else detail = `<code>${escapeHtml(ch.old)}</code> &rarr; <code>${escapeHtml(ch.new)}</code>`;
      return `<tr class="${k.cls}"><td class="metric-cell"><span class="m-label">${k.icon} ${escapeHtml(ch.key)}</span></td><td>${detail}</td><td>${t(k.key)}</td></tr>`;
    }).join("");
    return `<details class="cat-block" open>
      <summary class="cat-head"><span>${escapeHtml(sec.title)}</span><span>${sec.changes.length} ${t("items_shown")}</span></summary>
      <table class="report"><tbody>${rows}</tbody></table>
    </details>`;
  }).join("");

  box.innerHTML = header + `<div class="verdict-strip" style="margin-top:10px"><span class="verdict-pill pill-warn">\u26A0\uFE0F ${rep.total} change(s)</span></div>` + sections;
}

let diagReady = false;

function diagInit() {
  if (diagReady) return;
  diagReady = true;
  $("#diagRun").addEventListener("click", runDiagnose);
  runDiagnose();
}

const SEV_STYLE = {
  crit: { cls: "d-crit", icon: "\u{1F534}", key: "sev_crit" },
  warn: { cls: "d-warn", icon: "\u{1F7E1}", key: "sev_warn" },
  info: { cls: "d-info", icon: "\u{1F535}", key: "sev_info" },
  ok:   { cls: "d-ok",   icon: "\u{1F7E2}", key: "sev_ok" },
};

async function runDiagnose() {
  const btn = $("#diagRun");
  const err = $("#diagError");
  err.classList.add("hidden");
  btn.disabled = true; btn.textContent = t("diagnosing");
  try {
    const d = await api("/api/diagnose");
    lastDiagnosis = d;
    renderDiagnosis(d);
    $("#diagEmpty").classList.add("hidden");
  } catch (e) {
    err.textContent = e.message;
    err.classList.remove("hidden");
    $("#diagResult").innerHTML = "";
  } finally {
    btn.disabled = false; btn.textContent = t("diag_run");
  }
}

function renderDiagnosis(d) {
  const sv = SEV_STYLE[d.severity] || SEV_STYLE.info;
  const w = d.window || {};
  const fmt = (s) => s ? new Date(s).toLocaleString([], { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }) : "";
  $("#diagWindow").textContent = w.label
    ? `${w.label} \u00b7 A ${fmt(w.a_start)} vs B ${fmt(w.b_start)}` : "";

  let html = `<div class="diag-verdict ${sv.cls}">
    <div class="dv-top"><span class="dv-badge">${sv.icon} ${t(sv.key)}</span></div>
    <div class="dv-headline">${escapeHtml(d.headline || "")}</div>`;

  const chain = [];
  if (d.culprit) chain.push(`<div class="dv-link"><span class="dv-label">${t("responsible_label")}</span><span>${escapeHtml(d.culprit)}</span></div>`);
  if (d.changed) chain.push(`<div class="dv-link"><span class="dv-label">${t("changed_label")}</span><code>${escapeHtml(d.changed)}</code></div>`);
  if (chain.length) html += `<div class="dv-chain">${chain.join("")}</div>`;

  if (d.evidence && d.evidence.length)
    html += `<div class="dv-evidence">${t("evidence_label")} ${d.evidence.map(escapeHtml).join(" \u00b7 ")}</div>`;
  if (d.next && d.next.length)
    html += `<div class="dv-next">${t("next_label")} ${d.next.map((c) => `<code>${escapeHtml(c)}</code>`).join("")}</div>`;
  html += `</div>`;

  if (d.notes && d.notes.length)
    html += `<div class="diag-notes">${d.notes.map((n) => escapeHtml(n)).join("<br>")}</div>`;

  if (d.triage && d.triage.length) {
    html += `<div class="triage-board">` + d.triage.map((b) => {
      const st = TRIAGE_STATUS[b.status] || TRIAGE_STATUS.ok;
      return `<div class="triage-card ${st.cls}">
        <div class="tc-top"><span class="tc-icon">${TRIAGE_ICON[b.key] || ""}</span>
          <span class="tc-label">${escapeHtml(b.label)}</span><span class="tc-dot">${st.dot}</span></div>
        <div class="tc-headline">${escapeHtml(b.headline)}</div></div>`;
    }).join("") + `</div>`;
  }

  if (d.processes && d.processes.length) {
    const trs = d.processes.map((r) => {
      const v = PV[r.verdict] || PV.flat;
      const mark = r.restarted ? ` <span class="restart-tag">\u27F3</span>` : "";
      return `<tr class="${v.cls}"><td class="proc-name"><span class="p-dot">${v.icon}</span>${escapeHtml(r.name)}${mark}</td>
        <td>${pctVal(r.cpu_pct_a)}</td><td>${pctValApprox(r.cpu_pct_b, r.cpu_approx_b)}</td><td class="delta-cell">${deltaValFrom(r.cpu_delta_pct, r.from_zero)}</td>
        <td>${memVal(r.rss_kb_a)}</td><td>${memVal(r.rss_kb_b)}</td><td class="delta-cell">${deltaValFrom(r.rss_delta_pct, r.from_zero)}</td></tr>`;
    }).join("");
    html += `<details class="cat-block" open><summary class="cat-head">
      <span>${t("per_process")}</span><span>${d.processes.length} ${t("shown")}</span></summary>
      <table class="report"><thead><tr><th>${t("th_process")}</th><th>${t("th_cpu_a")}</th><th>${t("th_cpu_b")}</th><th>${t("th_dcpu")}</th>
      <th>${t("th_mem_a")}</th><th>${t("th_mem_b")}</th><th>${t("th_dmem")}</th></tr></thead><tbody>${trs}</tbody></table></details>`;
  }

  if (d.changes && d.changes.length) {
    const trs = d.changes.map((c) => {
      const k = CHANGE_KIND[c.kind] || CHANGE_KIND.modified;
      let detail;
      if (c.kind === "added") detail = `<code>${escapeHtml(c.new)}</code>`;
      else if (c.kind === "removed") detail = `<span class="was">${t("was_label")} <code>${escapeHtml(c.old)}</code></span>`;
      else detail = `<code>${escapeHtml(c.old)}</code> &rarr; <code>${escapeHtml(c.new)}</code>`;
      return `<tr class="${k.cls}"><td class="metric-cell"><span class="m-label">${k.icon} ${escapeHtml(c.key)}</span>
        <span class="m-name">${escapeHtml(c.title)}</span></td><td>${detail}</td></tr>`;
    }).join("");
    html += `<details class="cat-block" open><summary class="cat-head">
      <span>${t("config_changes")}</span><span>${d.changes.length} ${t("shown")}</span></summary>
      <table class="report"><tbody>${trs}</tbody></table></details>`;
  }

  $("#diagResult").innerHTML = html;
}


// Theme: dark by default, light available for people who find a dark
// dashboard tiring over a long session. Persisted per browser.
const THEME_CYCLE = ["nova-dark", "nova-light", "terra-dark", "terra-light", "iris-dark", "iris-light"];
const THEME_ICON = {
  "nova-dark": "\u25D1", "nova-light": "\u25D5",
  "terra-dark": "\u2600", "terra-light": "\u25D5",
  "iris-dark": "\u25D0", "iris-light": "\u25D5",
};

// loginDefaultLang honours a previously stored choice but otherwise falls
// back to English rather than the browser locale.
function loginDefaultLang() {
  try {
    const stored = localStorage.getItem("dscope-lang");
    if (stored && LANG_NAMES[stored]) return stored;
  } catch (e) { /* private mode */ }
  return "en";
}

function initLang() {
  const sel = $("#langSelect");
  Object.keys(LANG_NAMES).forEach((code) => {
    const opt = document.createElement("option");
    opt.value = code;
    opt.textContent = LANG_NAMES[code];
    sel.appendChild(opt);
  });
  // The login page defaults to English rather than following the browser
  // locale: it is the first thing an operator or a screen-sharing session
  // sees, English is the lingua franca for this kind of tool, and a
  // returning user's explicit choice is still honoured (detectLang reads
  // the stored preference first). Inside the app proper, browser-locale
  // detection stays the default so a first-time user lands in their own
  // language.
  setLang(page === "login" ? loginDefaultLang() : detectLang());
  sel.value = currentLang;
  sel.addEventListener("change", () => {
    setLang(sel.value);
    // dynamically-rendered content (tables, diagnosis results) needs a
    // re-render to pick up the new language; static chrome already
    // updated itself via applyStaticI18n() inside setLang().
    document.dispatchEvent(new CustomEvent("dscope-lang-changed"));
  });
}

function initTheme() {
  const stored = (() => { try { return localStorage.getItem("dscope-theme"); } catch (e) { return null; } })();
  applyTheme(THEME_CYCLE.includes(stored) ? stored : "nova-dark");
  $("#themeToggle").addEventListener("click", () => {
    const cur = THEME_CYCLE.find((t) => t !== "nova-dark" && document.body.classList.contains("theme-" + t)) || "nova-dark";
    const next = THEME_CYCLE[(THEME_CYCLE.indexOf(cur) + 1) % THEME_CYCLE.length];
    applyTheme(next);
    try { localStorage.setItem("dscope-theme", next); } catch (e) { /* private mode */ }
  });
}

function applyTheme(name) {
  // nova-dark is :root's own default palette and has no explicit class;
  // every other theme overrides :root via body.theme-<name>.
  THEME_CYCLE.forEach((t) => document.body.classList.toggle("theme-" + t, t === name && t !== "nova-dark"));
  $("#themeToggle").textContent = THEME_ICON[name] || THEME_ICON["nova-dark"];
  $("#themeToggle").title = "Theme: " + name + " (click to cycle)";
  if (typeof chart !== "undefined" && chart) {
    if (typeof curPreset !== "undefined" && curPreset) loadTrend();
  }
}

let reasoningReady = false;
let lastReasoning = null;

function reasoningInit() {
  if (reasoningReady) return;
  reasoningReady = true;
  $("#reasoningRun").addEventListener("click", runReasoning);
  runReasoning();
}

async function runReasoning() {
  const btn = $("#reasoningRun");
  const err = $("#reasoningError");
  err.classList.add("hidden");
  btn.disabled = true;
  btn.textContent = t("reasoning_running");
  try {
    const d = await api("/api/reasoning");
    lastReasoning = d;
    renderReasoning(d);
    $("#reasoningEmpty").classList.add("hidden");
  } catch (e) {
    err.textContent = e.message;
    err.classList.remove("hidden");
    $("#reasoningResult").innerHTML = "";
  } finally {
    btn.disabled = false;
    btn.textContent = t("reasoning_run");
  }
}

function renderReasoning(d) {
  const w = d.window || {};
  const fmt = (x) => x ? new Date(x).toLocaleString([], { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }) : "";
  // The core count is shown because every scale-relative threshold is
  // derived from it -- a reader checking whether a verdict is reasonable
  // needs to know what "50% of capacity" was measured against.
  const ncpu = d.machine && d.machine.ncpu ? ` \u00b7 ${d.machine.ncpu} CPU` : "";
  $("#reasoningWindow").textContent = w.label
    ? `${w.label} \u00b7 A ${fmt(w.a_start)} vs B ${fmt(w.b_start)}${ncpu}` : "";

  let html = "";
  const diags = d.diagnoses || [];

  if (!diags.length) {
    // "No diagnosis" and "no state is active" are different facts, and
    // conflating them reads as a contradiction: a state row can show
    // "1 active" directly under a message saying nothing matched. If
    // something IS active, name it -- the state itself carries a real,
    // human-readable signal even before the catalog has an opinion about
    // what combination it means.
    const activeStates = (d.states || []).filter((s) => s.active);
    if (activeStates.length) {
      html += `<div class="no-finding" style="margin-bottom:18px">${t("reasoning_active_no_pattern", activeStates.length)}` +
        `<div style="margin-top:8px">` +
        activeStates.map((s) => `<code class="rsn-id">${escapeHtml(s.id)}</code>`).join(" ") +
        `</div></div>`;
    } else {
      html += `<div class="no-finding" style="margin-bottom:18px">${t("reasoning_no_diagnosis")}</div>`;
    }
  } else {
    html += diags.map((r) => {
      const sv = SEV_STYLE[r.severity] || SEV_STYLE.info;
      return `<div class="diag-verdict ${sv.cls}">
        <div class="dv-top"><span class="dv-badge">${sv.icon} ${t(sv.key)}</span>
          <code class="rsn-id">${escapeHtml(r.id)}</code></div>
        <div class="dv-headline">${escapeHtml(r.conclusion)}</div>
        <div class="dv-chain">
          <div class="dv-link"><span class="dv-label">${t("reasoning_triggered_by")}</span>
            <span>${(r.states || []).map((x) => `<code>${escapeHtml(x)}</code>`).join(" ")}</span></div>
        </div>
        ${r.evidence && r.evidence.length ? `<div class="dv-evidence">${t("evidence_label")} ${r.evidence.map(escapeHtml).join(" \u00b7 ")}</div>` : ""}
        ${r.next && r.next.length ? `<div class="dv-next">${t("next_label")} ${r.next.map((c) => `<code>${escapeHtml(c)}</code>`).join("")}</div>` : ""}
      </div>`;
    }).join("");
  }

  // Show every state that was checked, including the ones that did NOT
  // hold: a diagnosis that hinges on something being absent can only be
  // audited if the reader can see that it was actually checked.
  const states = d.states || [];
  if (states.length) {
    const activeCount = states.filter((x) => x.active).length;

    // Grouped by domain, and the active ones first within each group. A flat
    // list was fine at 17 states; at ~60 the reader needs the shape of the
    // machine, and the handful that hold must not be buried among the ones
    // that don't.
    const byDomain = new Map();
    states.forEach((st) => {
      const k = st.domain || "other";
      if (!byDomain.has(k)) byDomain.set(k, []);
      byDomain.get(k).push(st);
    });

    const stateRow = (st) => {
      const cls = st.active ? "v-worse" : "v-flat";
      const mark = st.active ? "\u25CF" : "\u25CB";
      const label = st.active ? t("reasoning_active") : t("reasoning_inactive");
      return `<tr class="${cls}">
        <td class="metric-cell"><span class="m-label">${mark} <code>${escapeHtml(st.id)}</code></span>
          ${st.evidence && st.evidence.length ? `<span class="m-name">${st.evidence.map(escapeHtml).join(" \u00b7 ")}</span>` : ""}</td>
        <td>${label}</td>
      </tr>`;
    };

    const groups = [...byDomain.entries()].map(([domain, list]) => {
      const act = list.filter((x) => x.active);
      const inact = list.filter((x) => !x.active);
      const rows = [...act, ...inact].map(stateRow).join("");
      // Groups with nothing active start collapsed: the negative evidence
      // stays available for auditing without pushing the findings off screen.
      const open = act.length ? " open" : "";
      return `<details class="cat-block"${open}><summary class="cat-head">
        <span>${escapeHtml(domain)}</span><span>${act.length} / ${list.length} ${t("reasoning_active")}</span></summary>
        <table class="report"><tbody>${rows}</tbody></table></details>`;
    }).join("");

    html += `<div class="cat-head" style="margin-top:18px">
        <span>${t("reasoning_states")}</span><span>${activeCount} / ${states.length} ${t("reasoning_active")}</span>
      </div>${groups}`;
  }

  $("#reasoningResult").innerHTML = html;
}
