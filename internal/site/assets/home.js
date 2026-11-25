// Home page: mode picker, start button, daily puzzle, progress summary.
(function () {
  "use strict";
  const { $, progress, randomUnsolved, daily, getMode, setMode, puzzleURL, index } = window.MPL;

  function renderModes() {
    const box = $("homeModes");
    if (!box) return;
    box.innerHTML = "";
    [0, 1, 2, 3, 4, 5].forEach(n => {
      const b = document.createElement("button");
      b.className = "chip";
      b.setAttribute("aria-pressed", getMode() === n);
      b.textContent = n ? `Mate in ${n}` : "Any length";
      b.onclick = () => { setMode(n); renderModes(); };
      box.appendChild(b);
    });
  }
  renderModes();

  $("startBtn").onclick = async e => {
    e.preventDefault();
    const p = await randomUnsolved(getMode());
    if (p) location.href = puzzleURL(p.id);
  };

  daily().then(p => {
    if (!p) return;
    const a = $("dailyLink");
    a.href = puzzleURL(p.id);
    a.textContent = `Puzzle #${p.id} · Mate in ${p.n} · ${p.white ? "White" : "Black"} to move`;
    a.dataset.pid = p.id;
    const st = progress.status(p.id);
    $("dailyState").textContent = st === "ok" ? "Solved today ✓" : "";
  }).catch(() => {});

  async function renderStats() {
    const c = progress.counts(), st = progress.get().streak;
    $("sSolved").textContent = c.ok.toLocaleString();
    $("sStreak").textContent = st.cur;
    $("sBest").textContent = st.best;
    try {
      const all = await index();
      $("sTotal").textContent = all.length.toLocaleString();
    } catch {}
  }
  progress.on(renderStats);
  renderStats();
})();
