"use strict";

const $ = (sel) => document.querySelector(sel);
const page = document.body.dataset.page;


async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  let body = null;
  try { body = await res.json(); } catch { /* 非 JSON */ }
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

async function main() {
  const me = await api("/api/me");
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
      if (t.dataset.tab === "trend") trendInit();
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

const VERDICT = {
  worse:  { icon: "🔴", text: "恶化", cls: "v-worse" },
  better: { icon: "🟢", text: "改善", cls: "v-better" },
  watch:  { icon: "🟡", text: "关注", cls: "v-watch" },
  flat:   { icon: "·",  text: "平稳", cls: "v-flat" },
};

function renderReport(rep) {
  $("#diffEmpty").classList.add("hidden");
  $("#diffResult").classList.remove("hidden");

  const counts = { worse: 0, better: 0, watch: 0, flat: 0 };
  rep.rows.forEach((r) => counts[r.verdict]++);

  const w = rep.window;
  const fmtW = (s, e) => `${s.slice(5, 16).replace("T", " ")} → ${e.slice(11, 16)}`;
  $("#verdictStrip").innerHTML = `
    <span class="verdict-pill pill-bad">🔴 恶化 <b>${counts.worse}</b></span>
    <span class="verdict-pill pill-good">🟢 改善 <b>${counts.better}</b></span>
    <span class="verdict-pill pill-warn">🟡 关注 <b>${counts.watch}</b></span>
    <span class="verdict-pill pill-flat">平稳 <b>${counts.flat}</b></span>
    <span class="verdict-window">
      <span class="wa">[A ${fmtW(w.a_start, w.a_end)}]</span> vs
      <span class="wb">[B ${fmtW(w.b_start, w.b_end)}]</span> · 阈值 ${w.threshold_pct}%
    </span>`;

  const onlyExceeded = $("#onlyExceeded").checked;
  const byCat = new Map();
  rep.rows.forEach((r) => {
    if (onlyExceeded && !r.exceeded) return;
    if (!byCat.has(r.category)) byCat.set(r.category, []);
    byCat.get(r.category).push(r);
  });

  const maxAbs = Math.max(100, ...rep.rows.filter((r) => r.delta_pct !== null).map((r) => Math.abs(r.delta_pct)));

  const blocks = [];
  for (const [cat, rows] of byCat) {
    const trs = rows.map((r) => {
      const v = VERDICT[r.verdict];
      let deltaTxt, barHtml = "";
      if (r.delta_pct === null) {
        deltaTxt = r.a === null ? "新出现" : (r.b === null ? "消失" : "∞");
      } else {
        deltaTxt = (r.delta_pct > 0 ? "+" : "") + r.delta_pct.toFixed(1) + "%";
        const pct = Math.min(50, Math.abs(r.delta_pct) / maxAbs * 50);
        barHtml = `<span class="delta-bar-wrap"><span class="delta-bar ${r.delta_pct >= 0 ? "up" : "down"}" data-w="${pct.toFixed(2)}"></span></span>`;
      }
      const inst = r.instance ? ` <code>[${escapeHtml(r.instance)}]</code>` : "";
      return `<tr class="${v.cls}">
        <td class="metric-cell">
          <span class="m-label">${v.icon} ${escapeHtml(r.label)}${inst}</span>
          <span class="m-name">${escapeHtml(r.metric)}</span>
        </td>
        <td class="col-a">${fmtNum(r.a)}</td>
        <td class="col-b">${fmtNum(r.b)}</td>
        <td class="delta-cell">${deltaTxt}${barHtml}</td>
        <td>${v.text}</td>
        <td class="units-cell">${escapeHtml(r.units || "")}</td>
      </tr>`;
    }).join("");
    blocks.push(`<div class="cat-block">
      <div class="cat-head"><span>${escapeHtml(cat)}</span><span>${rows.length} 项</span></div>
      <table class="report">
        <thead><tr><th>指标</th><th>A 均值</th><th>B 均值</th><th>Δ</th><th>结论</th><th>单位</th></tr></thead>
        <tbody>${trs}</tbody>
      </table>
    </div>`);
  }
  $("#reportTables").innerHTML = blocks.length
    ? blocks.join("")
    : `<div class="empty-hint">没有指标超过 ${rep.window.threshold_pct}% 阈值 —— 两个窗口表现基本一致 👍<br>可取消勾选「只看显著变化」查看全量数据。</div>`;
  document.querySelectorAll(".delta-bar[data-w]").forEach((el) => {
    el.style.width = el.dataset.w + "%";
  });

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

  const cat = await api("/api/catalog");
  const seg = $("#presetSeg");
  const order = ["cpu", "load", "mem", "disk", "net", "tcp"];
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
