// Shared by every page: theme, local progress, solved marks, ads.
(function () {
  "use strict";
  const $ = id => document.getElementById(id);
  const CONFIG = JSON.parse(document.body.dataset.config || "{}");

  // ---------- Theme ----------
  const applyTheme = t => { if (t) document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme; };
  try { applyTheme(localStorage.getItem("mpl:theme")); } catch {}
  const themeBtn = $("themeBtn");
  if (themeBtn) themeBtn.onclick = () => {
    const dark = matchMedia("(prefers-color-scheme: dark)").matches;
    const cur = document.documentElement.dataset.theme || (dark ? "dark" : "light");
    const next = cur === "dark" ? "light" : "dark";
    applyTheme(next);
    try { localStorage.setItem("mpl:theme", next); } catch {}
  };

  // ---------- Progress (localStorage; optionally synced) ----------
  // { v, solved: {id: "ok"|"seen"}, streak: {cur, best}, updated }
  const KEY = "mpl:progress";
  const listeners = new Set();
  function blank() { return { v: 1, solved: {}, streak: { cur: 0, best: 0 }, updated: 0 }; }
  function load() {
    try {
      const p = JSON.parse(localStorage.getItem(KEY));
      if (p && p.v === 1) return p;
    } catch {}
    return blank();
  }
  let data = load();

  function save() {
    data.updated = Date.now();
    try { localStorage.setItem(KEY, JSON.stringify(data)); } catch {}
    listeners.forEach(fn => fn(data));
  }

  // Another tab changed progress.
  addEventListener("storage", e => { if (e.key === KEY) { data = load(); listeners.forEach(fn => fn(data)); } });

  const progress = {
    get: () => data,
    status: id => data.solved[id] || "",
    on(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    // result: "clean" (first try, no hint), "solved" (with mistakes/hints), "seen" (looked at solution)
    record(id, result) {
      const prev = data.solved[id];
      if (result === "seen") {
        if (prev !== "ok") data.solved[id] = "seen";
        data.streak.cur = 0;
      } else {
        data.solved[id] = "ok";
        if (result === "clean" && prev !== "ok") {
          data.streak.cur += 1;
          data.streak.best = Math.max(data.streak.best, data.streak.cur);
        } else if (result !== "clean") {
          data.streak.cur = 0;
        }
      }
      save();
    },
    breakStreak() { if (data.streak.cur) { data.streak.cur = 0; save(); } },
    counts() {
      let ok = 0, seen = 0;
      for (const s of Object.values(data.solved)) s === "ok" ? ok++ : seen++;
      return { ok, seen };
    },
  };

  // ---------- Solved marks on any page ----------
  function markCards() {
    document.querySelectorAll("[data-pid]").forEach(el => {
      const s = progress.status(el.dataset.pid), st = el.querySelector(".st");
      if (!st) return;
      st.className = "st " + s;
      st.textContent = s === "ok" ? "✓" : s === "seen" ? "◐" : "";
      st.title = s === "ok" ? "Solved" : s === "seen" ? "Solution viewed" : "";
    });
  }
  progress.on(markCards);
  markCards();

  // ---------- Puzzle index (for random / daily / next) ----------
  let indexPromise = null;
  function index() {
    if (!indexPromise) {
      indexPromise = fetch(CONFIG.index).then(r => r.json()).then(j => j.puzzles.map(([id, n, w]) => ({ id, n, white: !!w })));
    }
    return indexPromise;
  }
  function getMode() { try { return +localStorage.getItem("mpl:mode") || 0; } catch { return 0; } }
  function setMode(n) { try { localStorage.setItem("mpl:mode", String(n)); } catch {} }
  async function randomUnsolved(mode, excludeId) {
    const all = await index();
    let pool = all.filter(p => (!mode || p.n === mode) && p.id !== excludeId);
    const unsolved = pool.filter(p => progress.status(p.id) !== "ok");
    if (unsolved.length) pool = unsolved;
    return pool.length ? pool[Math.floor(Math.random() * pool.length)] : null;
  }
  // Same puzzle for everyone on a given (UTC) day.
  async function daily() {
    const all = await index();
    if (!all.length) return null;
    const day = new Date().toISOString().slice(0, 10);
    let h = 2166136261;
    for (const c of day) { h ^= c.charCodeAt(0); h = Math.imul(h, 16777619); }
    return all[(h >>> 0) % all.length];
  }
  const puzzleURL = id => `/puzzle/${id}/`;

  // ---------- Ads ----------
  document.querySelectorAll("ins.adsbygoogle").forEach(() => {
    try { (window.adsbygoogle = window.adsbygoogle || []).push({}); } catch {}
  });

  window.MPL = { $, CONFIG, progress, index, randomUnsolved, daily, getMode, setMode, puzzleURL };
})();
