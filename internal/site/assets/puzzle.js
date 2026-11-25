// Puzzle page: board, move input, checking, hints and solution.
// Positions along the stored solution are precomputed at build time, so the
// main line works instantly; Stockfish (WASM) loads in the background and is
// only consulted when the solver plays something else.
(function () {
  "use strict";
  const { $, CONFIG, progress, randomUnsolved, getMode, setMode, puzzleURL } = window.MPL;
  const { Engine, sanOf } = window.MPLChess;
  const GLYPH = { k: "♚", q: "♛", r: "♜", b: "♝", n: "♞", p: "♟︎" };
  const root = $("puzzleApp");
  const P = JSON.parse(root.dataset.puzzle);
  const engine = new Engine(CONFIG.engine);

  // ---------- Position cache ----------
  // key: moves.join(" ") -> {fen, check, legal, san[], mate}
  const cache = new Map();
  P.positions.forEach((p, k) => {
    cache.set(P.solution.slice(0, k).join(" "), {
      ...p, san: P.san.slice(0, k), mate: p.check && p.legal.length === 0,
    });
  });

  async function positionAt(moves) {
    const key = moves.join(" ");
    if (cache.has(key)) return cache.get(key);
    const before = await positionAt(moves.slice(0, -1));
    const after = await engine.position(P.fen, moves);
    const res = {
      ...after, mate: after.check && after.legal.length === 0,
      san: before.san.concat(sanOf(before, moves[moves.length - 1], after)),
    };
    cache.set(key, res);
    return res;
  }

  // ---------- State ----------
  let moves = [], pos = cache.get("");
  let onLine = true, busy = false, finished = false, sawSolution = false;
  let mistakes = 0, usedHint = false;
  let flipped = P.to_move === "black", selected = null, hintSq = null;
  let altFlags = [];

  const remaining = () => P.mate_in - Math.floor(moves.length / 2);
  const attackerToMove = () => moves.length % 2 === 0;
  const sleep = ms => new Promise(r => setTimeout(r, ms));

  function feedback(html, kind = "") {
    $("feedback").className = "feedback " + kind;
    $("feedback").innerHTML = html;
  }

  // ---------- Rendering ----------
  function parseBoard(fen) { return window.MPLChess.parseFen(fen); }

  function squares() {
    const out = [];
    for (let r = 0; r < 8; r++) for (let f = 0; f < 8; f++) {
      const rank = flipped ? r + 1 : 8 - r, file = flipped ? 7 - f : f;
      out.push("abcdefgh"[file] + rank);
    }
    return out;
  }

  function renderBoard() {
    const el = $("board"); el.innerHTML = ""; el.classList.remove("board-static");
    const { board, stm } = parseBoard(pos.fen);
    const last = moves.length ? moves[moves.length - 1] : null;
    const targets = selected ? pos.legal.filter(m => m.startsWith(selected)).map(m => m.slice(2, 4)) : [];
    squares().forEach((sq, i) => {
      const fi = "abcdefgh".indexOf(sq[0]), rk = +sq[1];
      const d = document.createElement("div");
      d.className = "sq " + ((fi + rk) % 2 ? "l" : "d");
      d.dataset.sq = sq;
      if (last && (sq === last.slice(0, 2) || sq === last.slice(2, 4))) d.classList.add("last");
      if (sq === selected) d.classList.add("sel");
      if (sq === hintSq) d.classList.add("hint");
      const pc = board[sq];
      if (pc && pc.toLowerCase() === "k" && pos.check && (pc === "K") === (stm === "w")) d.classList.add("check");
      if (i % 8 === 0) d.insertAdjacentHTML("beforeend", `<span class="coord r">${sq[1]}</span>`);
      if (i >= 56) d.insertAdjacentHTML("beforeend", `<span class="coord f">${sq[0]}</span>`);
      if (pc) {
        const s = document.createElement("span");
        s.className = "pc " + (pc === pc.toUpperCase() ? "w" : "b");
        s.textContent = GLYPH[pc.toLowerCase()];
        d.appendChild(s);
      }
      if (targets.includes(sq)) d.insertAdjacentHTML("beforeend", pc ? `<span class="ring"></span>` : `<span class="dot"></span>`);
      el.appendChild(d);
    });
  }

  function renderMoves() {
    const white = P.to_move === "white";
    const fullStart = +P.fen.split(" ")[5] || 1;
    let html = "";
    pos.san.forEach((m, i) => {
      const ply = (white ? 0 : 1) + i;
      const no = fullStart + Math.floor(ply / 2);
      if (ply % 2 === 0) html += `<span><span class="no">${no}.</span> `;
      else if (i === 0) html += `<span><span class="no">${no}…</span> `;
      html += `<span class="${altFlags[i] ? "alt" : ""}">${m}</span>`;
      html += (ply % 2 === 1 || i === pos.san.length - 1) ? `</span>` : " ";
    });
    $("moves").innerHTML = html;
  }

  async function setMoves(ms) {
    const next = await positionAt(ms);
    moves = ms; pos = next;
    selected = null; hintSq = null;
    renderBoard(); renderMoves();
  }

  function resetPuzzle() {
    moves = []; pos = cache.get(""); onLine = true; finished = false; sawSolution = false;
    mistakes = 0; usedHint = false; altFlags = []; selected = null; hintSq = null;
    renderBoard(); renderMoves();
    const s = progress.status(P.id);
    feedback(`${P.to_move === "white" ? "White" : "Black"} to move — find the mate in ${P.mate_in}.` +
      (s === "ok" ? ` <span style="opacity:.75">(already solved ✓)</span>` : ""));
  }

  // ---------- Solving ----------
  async function playUser(mv) {
    if (busy || finished || !attackerToMove()) return;
    busy = true; selected = null;
    const ply = moves.length, rem = remaining();
    const next = [...moves, mv];
    const onMain = onLine && mv === P.solution[ply];
    try {
      if (!onMain && engine.state !== "ready") feedback("Loading engine to check your move…");
      const after = await positionAt(next);
      if (after.mate) {
        moves = next; pos = after; altFlags[ply] = !onMain;
        renderBoard(); renderMoves();
        return solvedNow();
      }
      let reply = null, ok = false;
      if (rem > 1 && onMain) { ok = true; reply = P.solution[ply + 1]; }
      else if (rem > 1) {
        feedback("Checking…");
        // Defender to move: is it mated within rem-1? Re-check deeper before saying no.
        const forced = l => l && l.kind === "mate" && l.value < 0 && -l.value <= rem - 1;
        let line = await engine.evaluate(P.fen, next, { depth: 18 });
        if (!forced(line)) line = await engine.evaluate(P.fen, next, { depth: 26, movetime: 6000 });
        if (forced(line)) { ok = true; reply = line.pv[0]; onLine = false; altFlags[ply] = true; }
      }
      moves = next; pos = after; renderBoard(); renderMoves();
      if (!ok) {
        mistakes++;
        progress.breakStreak();
        feedback(rem === 1 ? "That's not mate. Try again." : `That doesn't force mate in ${rem}. Try again.`, "bad");
        await sleep(800); await setMoves(moves.slice(0, -1));
        return;
      }
      feedback(altFlags[ply] ? "Good — an alternative that also works!" : "Correct! Keep going…", "good");
      await sleep(450);
      if (!reply) { const line = await engine.evaluate(P.fen, moves, { depth: 18 }); reply = line && line.pv[0]; }
      if (!reply) throw new Error("engine found no reply");
      if (!onLine) altFlags[ply + 1] = true;
      await setMoves([...moves, reply]);
      feedback(`Mate in ${remaining()} — your move.`, "good");
    } catch (e) {
      feedback("Error: " + e.message, "bad");
    } finally { busy = false; }
  }

  function solvedNow() {
    finished = true;
    if (!sawSolution) progress.record(P.id, mistakes === 0 && !usedHint ? "clean" : "solved");
    const st = progress.get().streak;
    feedback(sawSolution ? "Checkmate." :
      `Checkmate! Solved.${mistakes === 0 && !usedHint ? ` Streak: ${st.cur}` : ""}
       <span style="flex:1"></span><span style="opacity:.85;font-weight:500">Press → for the next one</span>`, "done");
  }

  // ---------- Buttons ----------
  $("hintBtn").onclick = async () => {
    if (finished || busy || !attackerToMove()) return;
    let mv = onLine ? P.solution[moves.length] : null;
    if (!mv) {
      feedback("Thinking…");
      const line = await engine.evaluate(P.fen, moves, { mate: remaining() });
      mv = line && line.kind === "mate" && line.value > 0 ? line.pv[0] : null;
    }
    if (!mv) return;
    usedHint = true;
    hintSq = mv.slice(0, 2); renderBoard();
    feedback(`Hint: move the piece on ${hintSq}.`);
  };

  $("solBtn").onclick = async () => {
    if (busy) return;
    busy = true; sawSolution = true; finished = false; onLine = true; altFlags = [];
    if (progress.status(P.id) !== "ok") progress.record(P.id, "seen");
    try {
      await setMoves([]);
      feedback("Showing solution…");
      for (let i = 0; i < P.solution.length; i++) {
        await sleep(650);
        await setMoves(P.solution.slice(0, i + 1));
      }
      finished = true;
      feedback(`Solution: ${P.san.join(" ")}`, "good");
    } finally { busy = false; }
  };

  $("retryBtn").onclick = () => { if (!busy) resetPuzzle(); };
  $("flipBtn").onclick = () => { flipped = !flipped; renderBoard(); };
  $("copyBtn").onclick = async () => {
    try { await navigator.clipboard.writeText(P.fen); $("copyBtn").textContent = "Copied ✓"; }
    catch { prompt("FEN:", P.fen); }
    setTimeout(() => $("copyBtn").textContent = "Copy FEN", 1200);
  };
  $("lichessBtn").onclick = () =>
    window.open("https://lichess.org/analysis/standard/" + P.fen.replace(/ /g, "_"), "_blank", "noopener");

  async function goRandom() {
    const p = await randomUnsolved(getMode(), P.id);
    if (p) location.href = puzzleURL(p.id);
  }
  $("randomBtn").onclick = goRandom;

  // Mode chips (which mate lengths "Next" draws from).
  function renderModes() {
    const box = $("modeChips");
    box.innerHTML = "";
    [0, 1, 2, 3, 4, 5].forEach(n => {
      const b = document.createElement("button");
      b.className = "chip small";
      b.setAttribute("aria-pressed", getMode() === n);
      b.textContent = n ? `M${n}` : "All";
      b.onclick = () => { setMode(n); renderModes(); };
      box.appendChild(b);
    });
  }
  renderModes();

  function renderStats() {
    const c = progress.counts(), st = progress.get().streak;
    $("sSolved").textContent = c.ok.toLocaleString();
    $("sStreak").textContent = st.cur;
    $("sBest").textContent = st.best;
    $("sSeen").textContent = c.seen.toLocaleString();
  }
  progress.on(renderStats);
  renderStats();

  document.addEventListener("keydown", e => {
    if (e.target.matches("input, select, textarea") || e.metaKey || e.ctrlKey || e.altKey) return;
    const k = e.key.toLowerCase();
    if (k === "arrowright" || k === "n") goRandom();
    else if (k === "arrowleft" && P.prev) location.href = puzzleURL(P.prev);
    else if (k === "h") $("hintBtn").click();
    else if (k === "s") $("solBtn").click();
    else if (k === "r") $("retryBtn").click();
    else if (k === "f") $("flipBtn").click();
    else if (k === "escape") { selected = null; renderBoard(); }
  });

  // ---------- Input: click & drag ----------
  const ownPiece = sq => pos.legal.some(m => m.startsWith(sq));

  function tryMove(from, to) {
    const cands = pos.legal.filter(m => m.slice(0, 4) === from + to);
    if (!cands.length) return false;
    if (cands.length === 1) { playUser(cands[0]); return true; }
    const white = parseBoard(pos.fen).stm === "w";
    $("promoOpts").innerHTML = "";
    for (const p of ["q", "r", "b", "n"]) {
      const b = document.createElement("button");
      b.innerHTML = `<span class="pc ${white ? "w" : "b"}">${GLYPH[p]}</span>`;
      b.onclick = () => { $("promo").classList.remove("show"); playUser(from + to + p); };
      $("promoOpts").appendChild(b);
    }
    $("promo").classList.add("show");
    return true;
  }
  $("promo").onclick = e => { if (e.target === $("promo")) { $("promo").classList.remove("show"); selected = null; renderBoard(); } };

  let drag = null;
  $("board").addEventListener("pointerdown", e => {
    const sqEl = e.target.closest(".sq"); if (!sqEl || busy || finished || !attackerToMove()) return;
    const sq = sqEl.dataset.sq;
    if (selected && selected !== sq && tryMove(selected, sq)) return;
    if (!ownPiece(sq)) { selected = null; renderBoard(); return; }
    selected = sq; hintSq = null; renderBoard();
    const pcEl = $("board").querySelector(`[data-sq="${sq}"] .pc`);
    const g = $("ghost");
    g.className = pcEl.className; g.textContent = pcEl.textContent;
    g.style.fontSize = getComputedStyle(pcEl).fontSize;
    drag = { from: sq, pcEl, moved: false, x: e.clientX, y: e.clientY };
    try { $("board").setPointerCapture(e.pointerId); } catch {}
  });
  $("board").addEventListener("pointermove", e => {
    if (!drag) return;
    if (!drag.moved && Math.hypot(e.clientX - drag.x, e.clientY - drag.y) < 4) return;
    drag.moved = true;
    drag.pcEl.classList.add("lifted");
    const g = $("ghost"); g.style.display = "block"; g.style.left = e.clientX + "px"; g.style.top = e.clientY + "px";
  });
  $("board").addEventListener("pointerup", e => {
    if (!drag) return;
    const d = drag; drag = null;
    $("ghost").style.display = "none";
    d.pcEl.classList.remove("lifted");
    if (!d.moved) return;
    const t = document.elementFromPoint(e.clientX, e.clientY);
    const sqEl = t && t.closest && t.closest(".sq");
    if (!sqEl || sqEl.dataset.sq === d.from || !tryMove(d.from, sqEl.dataset.sq)) renderBoard();
  });

  // ---------- Engine status ----------
  engine.onState(s => {
    $("engineStatus").textContent = {
      idle: "", loading: "Loading Stockfish…", ready: "Stockfish 19 (in your browser) ready",
      error: "Stockfish couldn't load — only the main line can be checked.",
    }[s];
  });
  const warm = () => engine.load().catch(() => {});
  ("requestIdleCallback" in window) ? requestIdleCallback(warm, { timeout: 3000 }) : setTimeout(warm, 1500);

  resetPuzzle();
})();
