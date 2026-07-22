"use strict";

const $ = (sel) => document.querySelector(sel);
const page = document.body.dataset.page;


async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  let body = null;
  try { body = await res.json(); } catch {}
  if (res.status === 401 && page === "main") {
    location.href = "/login";
    throw new Error("未登录");
  }
  if (!res.ok) throw new Error((body && body.error) || `HTTP ${res.status}`);
  return body;
}

function toLocalInput(d) {
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
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


if (page === "login") {
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
  $("#logoutBtn").addEventListener("click", async () => {
    await api("/api/logout", { method: "POST" });
    location.href = "/login";
  });

  document.querySelectorAll(".tab").forEach((t) =>
    t.addEventListener("click", () => {
      document.querySelectorAll(".tab").forEach((x) => x.classList.toggle("is-active", x === t));
      $("#view-diff").classList.toggle("hidden", t.dataset.tab !== "diff");
      $("#view-trend").classList.toggle("hidden", t.dataset.tab !== "trend");
      $("#view-proc").classList.toggle("hidden", t.dataset.tab !== "proc");
      if (t.dataset.tab === "trend") trendInit();
      if (t.dataset.tab === "proc") procInit();
    })
  );

  diffInit();
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

async function runDiff() {
  const btn = $("#runDiff");
  const errBox = $("#diffError");
  errBox.classList.add("hidden");
  btn.disabled = true;
  btn.textContent = "比对中…";
  try {
    const q = new URLSearchParams({
      a_start: $("#aStart").value, a_end: $("#aEnd").value,
      b_start: $("#bStart").value, b_end: $("#bEnd").value,
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
    btn.textContent = "开始比对";
  }
}

const KIND = {
  worse:  { icon: "\u{1F534}", text: "恶化", cls: "v-worse", rgb: "255,93,108" },
  better: { icon: "\u{1F7E2}", text: "改善", cls: "v-better", rgb: "61,220,151" },
  watch:  { icon: "\u{1F7E1}", text: "关注", cls: "v-watch", rgb: "232,197,71" },
  flat:   { icon: "\u00B7",   text: "平稳", cls: "v-flat", rgb: null },
  new:    { icon: "\u2295",   text: "新出现", cls: "v-new", rgb: "178,141,255" },
  gone:   { icon: "\u2296",   text: "消失", cls: "v-gone", rgb: "131,145,173" },
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

const SEV = { crit: "严重", warn: "警告", info: "提示" };

function renderFindings(findings) {
  const box = $("#findings");
  if (!findings || !findings.length) {
    box.innerHTML = `<div class="no-finding">未命中已知诊断模式 — 请查看下方明细与趋势曲线。</div>`;
    return;
  }
  const order = { crit: 0, warn: 1, info: 2 };
  const sorted = [...findings].sort((a, b) => order[a.severity] - order[b.severity]);
  box.innerHTML = sorted.map((f) => `
    <div class="finding f-${f.severity}">
      <div class="finding-head">
        <span class="sev sev-${f.severity}">${SEV[f.severity] || f.severity}</span>
        <span class="finding-conclusion">${escapeHtml(f.conclusion)}</span>
      </div>
      <div class="finding-evidence">依据: ${f.evidence.map(escapeHtml).join(" · ")}</div>
      ${f.next && f.next.length ? `<div class="finding-next">下一步: ${f.next.map((c) => `<code>${escapeHtml(c)}</code>`).join("")}</div>` : ""}
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
  return `<tr id="${r._id}" class="${k.cls}${extraCls}"${hiddenAttr}${bg ? ` data-bg="${bg}"` : ""}>
    <td class="metric-cell">
      <span class="m-label">${k.icon} ${escapeHtml(r.label)}${inst}</span>
      <span class="m-name">${escapeHtml(r.metric)}</span>
    </td>
    <td class="col-a">${fmtNum(r.a)}</td>
    <td class="col-b">${fmtNum(r.b)}</td>
    <td class="delta-cell">${deltaTxt}${barHtml}</td>
    <td>${k.text}</td>
    <td class="units-cell">${escapeHtml(r.units || "")}</td>
  </tr>`;
}

let renderScale = 100;

function renderReport(rep) {
  $("#diffEmpty").classList.add("hidden");
  $("#diffResult").classList.remove("hidden");

  rep.rows.forEach((r, i) => { r._id = "row-" + i; });
  renderFindings(rep.findings);

  const counts = { worse: 0, better: 0, watch: 0, flat: 0, new: 0, gone: 0 };
  rep.rows.forEach((r) => counts[rowKind(r)]++);

  const w = rep.window;
  const fmtW = (s, e) => `${s.slice(5, 16).replace("T", " ")} \u2192 ${e.slice(11, 16)}`;
  const extra = (counts.new ? ` <span class="verdict-pill pill-new">\u2295 新出现 <b>${counts.new}</b></span>` : "") +
                (counts.gone ? ` <span class="verdict-pill pill-flat">\u2296 消失 <b>${counts.gone}</b></span>` : "");
  $("#verdictStrip").innerHTML = `
    <span class="verdict-pill pill-bad">\u{1F534} 恶化 <b>${counts.worse}</b></span>
    <span class="verdict-pill pill-good">\u{1F7E2} 改善 <b>${counts.better}</b></span>
    <span class="verdict-pill pill-warn">\u{1F7E1} 关注 <b>${counts.watch}</b></span>
    <span class="verdict-pill pill-flat">平稳 <b>${counts.flat}</b></span>${extra}
    <span class="verdict-window">
      <span class="wa">[A ${fmtW(w.a_start, w.a_end)}]</span> vs
      <span class="wb">[B ${fmtW(w.b_start, w.b_end)}]</span> \u00B7 阈值 ${w.threshold_pct}%
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
            <td class="metric-cell"><span class="m-label">${KIND[wk].icon} ${escapeHtml(r.label)} <code>${grp.length} 实例</code></span>
            <span class="m-name">${escapeHtml(r.metric)} \u00B7 B \u6781\u5DEE ${fmtNum(bMin)} ~ ${fmtNum(bMax)}</span></td>
            <td class="col-a"></td><td class="col-b">${fmtNum(bMax)}</td>
            <td class="delta-cell">\u6700\u5DEE ${wd}</td><td>${KIND[wk].text}</td><td class="units-cell">${escapeHtml(r.units || "")}</td></tr>`);
          grp.forEach((x) => trs.push(rowHTML(x, rowKind(x), " fold-child", " hidden")));
          i = j;
          continue;
        }
      }
      trs.push(rowHTML(r, rowKind(r), "", ""));
      i++;
    }
    blocks.push(`<details class="cat-block" open>
      <summary class="cat-head"><span>${escapeHtml(cat)}</span><span>${rows.length} 项</span></summary>
      <table class="report">
        <thead><tr><th>指标</th><th>A 均值</th><th>B 均值</th><th>\u0394</th><th>结论</th><th>单位</th></tr></thead>
        <tbody>${trs.join("")}</tbody>
      </table>
    </details>`);
  }
  $("#reportTables").innerHTML = blocks.length
    ? blocks.join("")
    : `<div class="empty-hint">聚焦模式下没有可显示的变化行 — 可关闭聚焦查看全量 ${rep.rows.length} 行。</div>`;

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
    $("#warnPre").textContent = rep.warnings.join("\n");
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
  const order = ["cpu", "load", "mem", "disk", "net", "tcp", "sock", "syn", "psi", "fs"];
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
  chart.showLoading({ text: "读取归档…", color: "#4cc9f0", textColor: "#8391ad", maskColor: "rgba(13,19,34,.6)" });
  try {
    const q = new URLSearchParams({
      preset: curPreset,
      start: $("#tStart").value,
      end: $("#tEnd").value,
    });
    const data = await api("/api/trend?" + q.toString());
    drawChart(data.series);
    const note = $("#trendNote");
    if (data.missing && data.missing.length) {
      note.textContent = `${data.missing.length} 项指标未被归档记录,已跳过: ${data.missing.join(", ")}`;
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

function drawChart(series) {
  chart.hideLoading();
  const opt = {
    backgroundColor: "transparent",
    color: PALETTE,
    textStyle: { color: "#8391ad", fontFamily: "ui-monospace, Menlo, Consolas, monospace" },
    tooltip: {
      trigger: "axis",
      backgroundColor: "#1a2440", borderColor: "#263354",
      textStyle: { color: "#dbe4f5", fontSize: 12 },
      valueFormatter: (v) => (v === null || v === undefined ? "—" : fmtNum(v)),
    },
    legend: { top: 0, textStyle: { color: "#8391ad" }, icon: "roundRect" },
    grid: { left: 64, right: 24, top: 40, bottom: 76 },
    xAxis: {
      type: "time",
      axisLine: { lineStyle: { color: "#263354" } },
      splitLine: { show: false },
    },
    yAxis: {
      type: "value",
      axisLabel: { formatter: (v) => fmtNum(v) },
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
      areaStyle: series.length <= 2 ? { opacity: 0.12 } : undefined,
      emphasis: { focus: "series" },
      data: s.points,
    })),
  };
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
  btn.disabled = true; btn.textContent = "对账中…";
  try {
    const q = new URLSearchParams({
      a_start: $("#pcAStart").value, a_end: $("#pcAEnd").value,
      b_start: $("#pcBStart").value, b_end: $("#pcBEnd").value,
    });
    const rep = await api("/api/procdiff?" + q.toString());
    renderProcDiff(rep);
    $("#procHint").classList.add("hidden");
  } catch (e) {
    err.textContent = e.message;
    err.classList.remove("hidden");
    $("#procResult").innerHTML = "";
  } finally {
    btn.disabled = false; btn.textContent = "开始对账";
  }
}

const PV = {
  worse:    { icon: "\u{1F534}", text: "恶化", cls: "v-worse" },
  better:   { icon: "\u{1F7E2}", text: "改善", cls: "v-better" },
  appeared: { icon: "\u2295", text: "新出现", cls: "v-new" },
  gone:     { icon: "\u2296", text: "已消失", cls: "v-gone" },
  flat:     { icon: "\u00B7", text: "平稳", cls: "v-flat" },
};

function procDelta(r) {
  if (r.verdict === "appeared") return "\u2295";
  if (r.verdict === "gone") return "\u2296";
  if (r.delta_pct === null || r.delta_pct === undefined) return "\u221E";
  return (r.delta_pct > 0 ? "+" : "") + r.delta_pct.toFixed(1) + "%";
}

function procTable(rows, unit) {
  const active = rows.filter((r) => r.verdict !== "flat");
  if (!active.length) return `<div class="empty-hint">无显著变化</div>`;
  const trs = active.map((r) => {
    const v = PV[r.verdict] || PV.flat;
    const mark = r.restarted ? ` <span class="restart-tag" title="${escapeHtml(r.restart_text||"")}">\u27F3</span>` : "";
    return `<tr class="${v.cls}">
      <td class="proc-name">${v.icon} ${escapeHtml(r.name)}${mark}</td>
      <td>${fmtNum(r.a)}</td><td>${fmtNum(r.b)}</td>
      <td class="delta-cell">${procDelta(r)}</td>
      <td>${v.text}</td><td class="units-cell">${unit}</td>
    </tr>`;
  }).join("");
  return `<table class="report"><thead><tr>
    <th>进程</th><th>A</th><th>B</th><th>\u0394</th><th>结论</th><th>单位</th>
    </tr></thead><tbody>${trs}</tbody></table>`;
}

function renderProcDiff(rep) {
  let html = "";
  if (rep.restarts && rep.restarts.length) {
    html += `<div class="restart-banner"><b>\u27F3 期间发生重启</b> ` +
      rep.restarts.map((r) => `<span class="restart-chip">${escapeHtml(r.name)} <em>${escapeHtml(r.restart_text||"")}</em></span>`).join("") +
      `</div>`;
  }
  html += `<div class="cat-block"><div class="cat-head"><span>进程 CPU 对账</span><span>占用升高 = 变差</span></div>${procTable(rep.cpu, "ms/s")}</div>`;
  html += `<div class="cat-block"><div class="cat-head"><span>进程内存对账</span><span>RSS</span></div>${procTable(rep.mem, "KB")}</div>`;
  $("#procResult").innerHTML = html;
}
