"use strict";

// selfcheck.js -- runs the front end without a browser.
//
// The leading underscore in the filename is load-bearing: main.go embeds this
// whole directory with `//go:embed web`, and that directive skips entries
// beginning with "_" or ".". Without it this harness ships inside every release
// binary and is served alongside the real assets.
//
// Why this exists: the web assets are embedded into the Go binary, so a typo
// in app.js ships in a release with nothing catching it. `node --check` only
// parses; it cannot see a call to a helper that was renamed, or a t() key that
// was never added to the locale table. Both of those have happened, and both
// render as the word "undefined" sitting in the middle of a diagnosis.
//
// Run it with plain node, no dependencies, no package.json:
//
//     node web/_selfcheck.js
//
// Three checks:
//   1. every locale carries exactly the keys `en` carries, with the same arity
//   2. every t("literal") and data-i18n in the source resolves to a real key
//   3. the render functions actually run over fixture payloads, and their HTML
//      contains no "undefined", "NaN", or "[object Object]"
//
// It deliberately does not assert on wording or layout -- those change often
// and a test that breaks on every copy edit gets deleted. It asserts that the
// page renders at all and says nothing that is obviously broken.

const fs = require("fs");
const path = require("path");

const ROOT = path.join(__dirname, "..");
const rd = (p) => fs.readFileSync(path.join(ROOT, p), "utf8");

let failures = 0;
function fail(msg) {
  console.log("FAIL  " + msg);
  failures++;
}
function pass(msg) {
  console.log("ok    " + msg);
}

// A DOM thin enough to fit in a screen and thick enough to render a report.
// Every element is the same shape, so a render function that reaches for a
// node the fixture does not describe gets a usable blank rather than a crash
// -- the point is to exercise the string building, not to model a browser.
function fakeEl() {
  const el = {
    textContent: "",
    innerHTML: "",
    value: "",
    checked: false,
    open: false,
    disabled: false,
    style: {},
    dataset: {},
    classList: {
      _s: new Set(),
      add(c) { this._s.add(c); },
      remove(c) { this._s.delete(c); },
      contains(c) { return this._s.has(c); },
      toggle(c, on) { on ? this._s.add(c) : this._s.delete(c); },
    },
    addEventListener() {},
    querySelector: () => fakeEl(),
    querySelectorAll: () => [],
    scrollIntoView() {},
    appendChild() {},
    remove() {},
  };
  return el;
}

// page is read from document.body.dataset.page at load time, and app.js only
// bootstraps when it is "login" or "main". Anything else loads the definitions
// and starts nothing, which is exactly what a harness wants.
global.document = {
  body: { dataset: { page: "selfcheck" } },
  addEventListener() {},
  querySelector: () => fakeEl(),
  querySelectorAll: () => [],
  createElement: () => fakeEl(),
};
global.window = { addEventListener() {}, scrollTo() {}, matchMedia: () => ({ matches: false, addEventListener() {} }) };
// Newer node ships a real navigator as a getter-only global, so it has to be
// redefined rather than assigned.
Object.defineProperty(global, "navigator", { value: { language: "en" }, configurable: true });
global.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
global.fetch = () => Promise.reject(new Error("selfcheck makes no requests"));

// Exported by appending assignments rather than editing the sources: the
// browser has no module system here and neither file should grow a
// harness-shaped seam just to be testable.
const EXPORTS = [
  "I18N", "t",
];
// currentLang is a let inside the evaluated scope, so it cannot be reassigned
// from out here -- a closure that lives in that scope is the way in. Setting it
// directly rather than through setLang() keeps this off localStorage and the
// document, which the harness does not have.
eval(rd("web/static/i18n.js") + ";" + EXPORTS.map((n) => `global.${n}=${n};`).join("") +
  "global.__setLang=(l)=>{currentLang=l;};");

const APP_EXPORTS = [
  "renderUnexplained", "renderReasoningProcs", "renderDiagnosis",
  "escapeHtml", "SEV_STYLE", "PV", "wireTabJumps", "CAPS",
];
eval(rd("web/static/app.js") + ";" + APP_EXPORTS.map((n) => `global.${n}=${n};`).join(""));
pass("both scripts load and evaluate");

// ---- 1. locale parity -------------------------------------------------------
//
// t() falls back to I18N.en for a key a locale is missing, so a gap here does
// not throw -- it silently prints English in the middle of a Japanese page.
// Arity is checked too: a key that is a function in en and a string in ko
// renders as the literal source of the arrow function.
{
  const locales = Object.keys(I18N);
  const enKeys = Object.keys(I18N.en);
  let bad = 0;
  for (const loc of locales) {
    for (const k of enKeys) {
      if (!(k in I18N[loc])) { fail(`${loc} is missing key ${k}`); bad++; continue; }
      if (typeof I18N[loc][k] !== typeof I18N.en[k]) {
        fail(`${loc}.${k} is a ${typeof I18N[loc][k]}, en.${k} is a ${typeof I18N.en[k]}`);
        bad++;
      } else if (typeof I18N.en[k] === "function" && I18N[loc][k].length !== I18N.en[k].length) {
        fail(`${loc}.${k} takes ${I18N[loc][k].length} args, en takes ${I18N.en[k].length}`);
        bad++;
      }
    }
    for (const k of Object.keys(I18N[loc])) {
      if (!(k in I18N.en)) { fail(`${loc} has key ${k} that en does not`); bad++; }
    }
  }
  if (!bad) pass(`${locales.length} locales carry the same ${enKeys.length} keys`);
}

// ---- 2. every referenced key exists -----------------------------------------
//
// Only literal references can be found this way; the verdict and severity
// words are looked up through a table (t(v.key)), so they are exercised by
// check 3 instead. A missing key does not throw either -- t() returns the key
// itself, so the screen shows "rs_procs_head" where a heading should be.
{
  const app = rd("web/static/app.js");
  const markup = rd("web/index.html") + rd("web/login.html");
  const used = new Set();
  for (const m of app.matchAll(/\bt\(\s*"([a-z0-9_]+)"/g)) used.add(m[1]);
  for (const m of markup.matchAll(/data-i18n(?:-title|-html)?="([a-z0-9_]+)"/g)) used.add(m[1]);
  const missing = [...used].filter((k) => !(k in I18N.en));
  for (const k of missing) fail(`referenced key ${k} is in no locale`);
  if (!missing.length) pass(`${used.size} literal key references all resolve`);

  // The reverse direction is a failure, not a note: an unused key costs ten
  // lines across ten locales, and the table had accumulated twelve of them
  // before this check existed. `used` only sees literal t("...") calls, so a
  // key reached through a lookup table is matched by the looser test that it
  // appears in the source at all -- enough to tell a live key from a dead one.
  const orphans = Object.keys(I18N.en).filter((k) => !used.has(k) && !app.includes(k));
  for (const k of orphans) fail(`key ${k} is referenced nowhere -- delete it from all locales`);
  if (!orphans.length) pass("no dead keys");
}

// ---- 3. the render functions run --------------------------------------------

// The failure this catches: a helper that was renamed, a field the server
// stopped sending, a t() called with the wrong number of arguments. All three
// surface as one of these words in the output rather than as an exception.
const LEAKS = [/undefined/, /\bNaN\b/, /\[object Object\]/, /\$\{/];

function scan(what, html) {
  if (typeof html !== "string") { fail(`${what} returned ${typeof html}, not a string`); return; }
  for (const re of LEAKS) {
    const m = html.match(re);
    if (m) {
      const at = Math.max(0, m.index - 60);
      fail(`${what} rendered ${m[0]}: ...${html.slice(at, m.index + 40).replace(/\s+/g, " ")}...`);
      return;
    }
  }
}

// The leak scan alone is not enough, and it is worth being precise about why:
// pctVal, memVal and friends render an em dash for null, which is correct --
// a process born inside the window genuinely has no baseline figure. But that
// makes a renamed field (r.cpu_pct_b becoming r.cpu_pct_bb) indistinguishable
// from missing data: the column just goes blank, no "undefined" anywhere. So
// the figures the fixture does supply have to be asserted present.
function must(what, html, needles) {
  for (const n of needles) {
    if (!String(html).includes(n)) fail(`${what} does not contain ${JSON.stringify(n)}`);
  }
}

// The case that started this: the metric engine calls the CPU degraded and no
// diagnosis fires. Two states in the domain were checked and stayed quiet, one
// could not be measured at all -- the panel has to distinguish those.
const UNEXPLAINED_FIXTURE = {
  unexplained: [
    { key: "cpu", label: "CPU", status: "bad", headline: "System load[15 minute] +spike", worst_pct: 1200, domains: ["cpu"] },
    { key: "disk", label: "Disk I/O", status: "warn", headline: "await +38%", domains: ["io", "filesystem"] },
  ],
  states: [
    { id: "state.cpu.load_high", domain: "cpu", active: false, reason: "" },
    { id: "state.cpu.core_pegged", domain: "cpu", active: false, reason: "" },
    { id: "state.cpu.system_high", domain: "cpu", active: false, reason: "kernel.all.cpu.sys not in this source" },
    { id: "state.io.queue_deep", domain: "io", active: false, reason: "" },
    { id: "state.mem.pressure", domain: "memory", active: true, reason: "" },
  ],
};

// Nulls everywhere they are possible: a process that did not exist in the
// baseline half has no A figures, and from_zero changes how the delta prints.
const PROC_FIXTURE = [
  { name: "nodedata-linux-", verdict: "worse", cpu_pct_a: 3.1, cpu_pct_b: 124.3, cpu_approx_b: false,
    cpu_delta_pct: 3909, rss_kb_a: 41000, rss_kb_b: 512000, rss_delta_pct: 1148, restarted: false, instances: 1, from_zero: false },
  { name: "gunicorn", verdict: "appeared", cpu_pct_a: null, cpu_pct_b: 18.4, cpu_approx_b: true,
    cpu_delta_pct: null, rss_kb_a: null, rss_kb_b: 88000, rss_delta_pct: null, restarted: true, instances: 4, from_zero: true },
  { name: "cron", verdict: "flat", cpu_pct_a: null, cpu_pct_b: null, cpu_approx_b: false,
    cpu_delta_pct: null, rss_kb_a: 2100, rss_kb_b: 2100, rss_delta_pct: 0, restarted: false, instances: 1, from_zero: false },
];

// A full one-click payload, and then the shape that reads as "nothing to say".
// Both matter: the second is when the hand-off to the reasoning tab is the only
// useful thing on the page, so it has to render there too.
const DIAG_FULL = {
  severity: "crit",
  headline: "CPU is saturated and one process accounts for all of it",
  culprit: "nodedata-linux- at 124% of a core",
  changed: "net.core.somaxconn 128 -> 4096",
  evidence: ["load 4.9 vs 0.4", "user 118% of a core"],
  next: ["top -H -p 4127", "perf top"],
  notes: ["No snapshots cover the baseline half."],
  window: { label: "last hour vs the same hour yesterday", a_start: "2026-09-09T14:00:00Z", b_start: "2026-09-10T14:00:00Z" },
  triage: [
    { key: "cpu", label: "CPU", status: "bad", headline: "System load[15 minute] +spike",
      improved: "Steal time -80%", improved_pct: -80 },
    // A block that is green AND carries an improvement, plus a red block that
    // also improved on a second metric: the improvement line has to render in
    // both, since suppressing it under a red light would hide half the window.
    { key: "mem", label: "Memory", status: "ok", headline: "flat",
      improved: "Available memory +140%", improved_pct: 140 },
  ],
  reasoning: [
    { id: "diag.cpu.runaway", severity: "crit", conclusion: "A single process is consuming a core",
      states: ["state.cpu.user_high"], evidence: ["user 118%"], next: ["top -H"], is_root: true, root_id: "diag.cpu.runaway" },
    { id: "diag.io.starved", severity: "warn", conclusion: "I/O waits grew behind the CPU",
      states: ["state.io.await_high"], evidence: [], next: [], is_root: false, root_id: "diag.cpu.runaway", downstream_of: "diag.cpu.runaway" },
  ],
  processes: PROC_FIXTURE,
  changes: [
    { kind: "modified", key: "sysctl.net.core.somaxconn", title: "socket backlog", old: "128", new: "4096" },
    { kind: "added", key: "unit.foo.service", title: "new service", new: "enabled" },
    { kind: "removed", key: "pkg.tcpdump", title: "package", old: "4.99.1" },
  ],
};

const DIAG_BARE = {
  severity: "unknown", headline: "", window: { label: "" },
  triage: [], reasoning: [], processes: [], changes: [], notes: [],
};

{
  for (const loc of Object.keys(I18N)) {
    global.__setLang(loc);
    scan(`renderUnexplained[${loc}]`, renderUnexplained(UNEXPLAINED_FIXTURE));
    scan(`renderReasoningProcs[${loc}]`, renderReasoningProcs(PROC_FIXTURE));
    // Values, not wording: every figure below comes from the fixture, so it is
    // language-independent and safe to assert in every locale.
    must(`renderUnexplained[${loc}]`, renderUnexplained(UNEXPLAINED_FIXTURE),
      ["CPU", "Disk I/O", "System load[15 minute] +spike", "await +38%", "dv-unexplained"]);
    must(`renderReasoningProcs[${loc}]`, renderReasoningProcs(PROC_FIXTURE),
      ["nodedata-linux-", "124.3%", "3.1%", "500M", "gunicorn", "86M", "cron",
       'class="approx"', "4×", "from-idle", "restart-tag"]);
    // renderDiagnosis writes into the DOM instead of returning, so the fake
    // element it writes to is where the output has to be read from.
    for (const [name, payload] of [["full", DIAG_FULL], ["bare", DIAG_BARE]]) {
      const sink = fakeEl();
      const realQS = document.querySelector;
      document.querySelector = (sel) => (sel === "#diagResult" ? sink : fakeEl());
      renderDiagnosis(payload);
      document.querySelector = realQS;
      scan(`renderDiagnosis(${name})[${loc}]`, sink.innerHTML);
      if (name === "full") {
        must(`renderDiagnosis(full)[${loc}]`, sink.innerHTML, [
          DIAG_FULL.headline, DIAG_FULL.culprit, "somaxconn", "top -H -p 4127",
          "A single process is consuming a core", "I/O waits grew behind the CPU",
          "rc-child", "No snapshots cover the baseline half.",
          'data-tab-jump="reasoning"',
          "Available memory +140%", "Steal time -80%", "tc-improved",
        ]);
      }
    }
  }
  pass(`render functions produce clean HTML in all ${Object.keys(I18N).length} locales`);
}

// ---- 4. capability gating ----------------------------------------------------
//
// A host with no reasoning capability has that tab dimmed, and a button that
// jumps to a dimmed tab is a dead click. Checked here rather than in check 3
// because it needs the flag flipped, and CAPS is shared mutable state.
{
  __setLang("en");
  CAPS.reasoning = false;
  const sink = fakeEl();
  const realQS = document.querySelector;
  document.querySelector = (sel) => (sel === "#diagResult" ? sink : fakeEl());
  renderDiagnosis(DIAG_FULL);
  document.querySelector = realQS;
  CAPS.reasoning = true;
  if (sink.innerHTML.includes('data-tab-jump="reasoning"')) {
    fail("the reasoning hand-off is rendered even when the tab is unavailable");
  } else {
    pass("the reasoning hand-off is withheld when the host cannot reason");
  }
}

console.log(failures ? `\nSELFCHECK FAILED (${failures})` : "\nSELFCHECK OK");
process.exit(failures ? 1 : 0);
