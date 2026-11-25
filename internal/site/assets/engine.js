// In-browser Stockfish (stockfish.js "lite, single-threaded" WASM build) behind
// a tiny promise API. Same UCI conversation as internal/engine in Go.
// Single-threaded on purpose: it needs no cross-origin isolation headers, which
// would break ads.
(function () {
  "use strict";
  const FILES = "abcdefgh";

  function parseFen(fen) {
    const b = {};
    const [placement, stm, castling, ep] = fen.split(" ");
    placement.split("/").forEach((row, r) => {
      let f = 0;
      for (const ch of row) {
        if (/\d/.test(ch)) f += +ch;
        else { b[FILES[f] + (8 - r)] = ch; f++; }
      }
    });
    return { board: b, stm, castling, ep };
  }

  // SAN for `mv` played in `before` ({fen, legal}); `after` is {check, legal}.
  function sanOf(before, mv, after) {
    const { board } = parseFen(before.fen);
    const frm = mv.slice(0, 2), to = mv.slice(2, 4), promo = mv.slice(4);
    const piece = board[frm] || "?", kind = piece.toUpperCase();
    let san;
    if (kind === "K" && Math.abs(frm.charCodeAt(0) - to.charCodeAt(0)) === 2) {
      san = to[0] === "g" ? "O-O" : "O-O-O";
    } else if (kind === "P") {
      san = (frm[0] !== to[0] ? frm[0] + "x" : "") + to + (promo ? "=" + promo.toUpperCase() : "");
    } else {
      const others = before.legal.filter(m => m.slice(2, 4) === to && m.slice(0, 2) !== frm && board[m.slice(0, 2)] === piece)
        .map(m => m.slice(0, 2));
      let dis = "";
      if (others.length) {
        if (others.every(o => o[0] !== frm[0])) dis = frm[0];
        else if (others.every(o => o[1] !== frm[1])) dis = frm[1];
        else dis = frm;
      }
      san = kind + dis + (board[to] ? "x" : "") + to;
    }
    if (after.check) san += after.legal.length ? "+" : "#";
    return san;
  }

  class Engine {
    constructor(url) {
      this.url = url;
      this.worker = null;
      this.queue = Promise.resolve();
      this.waiter = null;
      this.ready = null;
      this.listeners = new Set();
      this.state = "idle"; // idle | loading | ready | error
    }

    onState(fn) { this.listeners.add(fn); fn(this.state); }
    setState(s) { this.state = s; this.listeners.forEach(fn => fn(s)); }

    // Starts the worker (once). Safe to call early to warm up.
    load() {
      if (this.ready) return this.ready;
      this.setState("loading");
      this.ready = new Promise((resolve, reject) => {
        try {
          this.worker = new Worker(this.url);
        } catch (e) { this.setState("error"); reject(e); return; }
        this.worker.onmessage = e => {
          const line = typeof e.data === "string" ? e.data : String(e.data);
          if (this.waiter) this.waiter.push(line);
        };
        this.worker.onerror = e => { this.setState("error"); reject(new Error(e.message || "engine failed to load")); };
        this.exchange(["uci"], l => l === "uciok")
          .then(() => this.exchange(["setoption name Hash value 16", "isready"], l => l === "readyok"))
          .then(() => { this.setState("ready"); resolve(); }, err => { this.setState("error"); reject(err); });
      });
      // Allow a retry on the next request if loading failed.
      this.ready.catch(() => {
        if (this.worker) this.worker.terminate();
        this.worker = null; this.ready = null;
      });
      return this.ready;
    }

    // Sends commands and collects output lines until done(line) is true.
    exchange(cmds, done, timeoutMs = 20000) {
      return new Promise((resolve, reject) => {
        const lines = [];
        const timer = setTimeout(() => { this.waiter = null; reject(new Error("engine timed out")); }, timeoutMs);
        this.waiter = {
          push: l => {
            lines.push(l);
            if (done(l)) { clearTimeout(timer); this.waiter = null; resolve(lines); }
          },
        };
        cmds.forEach(c => this.worker.postMessage(c));
      });
    }

    // Serializes requests on the single worker.
    run(fn) {
      const p = this.queue.then(() => this.load()).then(fn);
      this.queue = p.catch(() => {});
      return p;
    }

    static posCmd(fen, moves) {
      return "position fen " + fen + (moves.length ? " moves " + moves.join(" ") : "");
    }

    // {fen, check, legal} after playing moves from fen.
    position(fen, moves) {
      return this.run(async () => {
        const pc = Engine.posCmd(fen, moves);
        const d = await this.exchange([pc, "d", "isready"], l => l === "readyok");
        const fenLine = d.find(l => l.startsWith("Fen: "));
        if (!fenLine) throw new Error("engine rejected position");
        const checkers = (d.find(l => l.startsWith("Checkers:")) || "Checkers:").slice(9).trim();
        const perft = await this.exchange([pc, "go perft 1"], l => l.startsWith("Nodes searched"));
        const legal = perft.map(l => /^([a-h][1-8][a-h][1-8][qrbn]?): \d+$/.exec(l.trim())).filter(Boolean).map(m => m[1]);
        return { fen: fenLine.slice(5).trim(), check: checkers !== "", legal };
      });
    }

    // Best line {kind: "mate"|"cp", value, pv} for the side to move.
    // opts.mate = N runs a dedicated mate-in-N search ("go mate N"), which is far
    // more reliable than a depth search when the attacker is already winning big.
    evaluate(fen, moves, opts = {}) {
      const { depth = 18, mate = 0, movetime = 4000 } = typeof opts === "number" ? { depth: opts } : opts;
      const limit = mate ? `go mate ${mate} movetime ${movetime}` : `go depth ${depth} movetime ${movetime}`;
      return this.run(async () => {
        const out = await this.exchange(
          ["setoption name MultiPV value 1", Engine.posCmd(fen, moves), limit],
          l => l.startsWith("bestmove"), movetime + 15000);
        let line = null;
        for (const l of out) {
          if (!l.startsWith("info") || !l.includes(" pv ") || l.includes("bound")) continue;
          const t = l.trim().split(/\s+/);
          const si = t.indexOf("score");
          if (si < 0) continue;
          line = { kind: t[si + 1], value: +t[si + 2], pv: t.slice(t.indexOf("pv") + 1) };
        }
        return line;
      });
    }
  }

  window.MPLChess = { Engine, parseFen, sanOf };
})();
