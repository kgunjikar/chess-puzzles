#!/usr/bin/env python3
"""
Web UI for the Stockfish mate-in-N puzzle generator.

  python3 mate_puzzles_ui.py            # opens http://127.0.0.1:8765
  python3 mate_puzzles_ui.py --port 9000 --out puzzles.jsonl --load puzzles.jsonl

Runs a local HTTP server (Python standard library only). The generator runs on a
background thread with its own Stockfish process; a second Stockfish process
answers the board's requests (legal moves, checking your moves while solving).
"""

import argparse
import json
import os
import random
import sys
import threading
import time
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import mate_puzzles as mp  # noqa: E402

HTML_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mate_puzzles_ui.html")


class State:
    def __init__(self, engine_path, out_path):
        self.engine_path = engine_path
        self.out_path = out_path
        self.lock = threading.Lock()
        self.puzzles = []
        self.fens = set()
        self.running = False
        self.stop_event = threading.Event()
        self.thread = None
        self.stats = {}
        self.target = {}
        self.found = {}
        self.started = None
        self.finished = None
        self.error = None
        # Engine used for interactive board requests.
        self.ui_engine = mp.Engine(engine_path)
        self.ui_lock = threading.Lock()

    def add_puzzle(self, p):
        with self.lock:
            if p["fen"] in self.fens:
                return False
            p = dict(p, id=len(self.puzzles) + 1)
            p.pop("board", None)
            self.puzzles.append(p)
            self.fens.add(p["fen"])
        if self.out_path:
            with open(self.out_path, "a") as f:
                f.write(json.dumps({k: v for k, v in p.items() if k != "id"}) + "\n")
        return True

    def start(self, cfg):
        with self.lock:
            if self.running:
                raise RuntimeError("generator already running")
            mates = sorted({int(n) for n in cfg.get("mates", [1, 2, 3, 4, 5]) if 1 <= int(n) <= 5})
            if not mates:
                raise ValueError("pick at least one mate length")
            count = max(1, min(500, int(cfg.get("count", 3))))
            self.target = {n: count for n in mates}
            self.found = {n: 0 for n in mates}
            self.stats = {}
            self.error = None
            self.started = time.time()
            self.finished = None
            self.running = True
            self.stop_event.clear()
        self.thread = threading.Thread(target=self._run, args=(cfg, mates), daemon=True)
        self.thread.start()

    def _run(self, cfg, mates):
        eng = None
        try:
            eng = mp.Engine(self.engine_path, int(cfg.get("threads", 1)), int(cfg.get("hash", 64)))
            seed = cfg.get("seed")
            rng = random.Random(int(seed) if seed not in (None, "") else None)

            def wanted(n):
                return n in self.found and self.found[n] < self.target[n]

            def stop():
                return self.stop_event.is_set() or not any(wanted(n) for n in mates)

            for p in mp.generate(
                eng, rng, mates, wanted,
                scan_depth=int(cfg.get("scan_depth", 10)),
                verify_depth=int(cfg.get("verify_depth", 22)),
                strong_nodes=int(cfg.get("strong_nodes", 3000)),
                blunder=float(cfg.get("blunder", 0.35)),
                min_ply=int(cfg.get("min_ply", 16)),
                allow_duals=bool(cfg.get("allow_duals", False)),
                stop=stop, stats=self.stats,
            ):
                if self.add_puzzle(p):
                    self.found[p["mate_in"]] += 1
        except Exception as e:  # surfaced to the UI
            self.error = str(e)
        finally:
            if eng:
                eng.quit()
            with self.lock:
                self.running = False
                self.finished = time.time()

    def snapshot(self, since):
        with self.lock:
            end = self.finished or time.time()
            return {
                "running": self.running,
                "error": self.error,
                "stats": dict(self.stats),
                "target": self.target,
                "found": self.found,
                "elapsed": round(end - self.started, 1) if self.started else 0,
                "total": len(self.puzzles),
                "puzzles": self.puzzles[since:],
            }

    def position(self, fen, moves):
        with self.ui_lock:
            e = self.ui_engine
            cur, check, _ = e.describe(fen, moves)
            legal = e.legal_moves(fen, moves)
            san = mp.san_line(e, fen, moves) if moves else []
        return {"fen": cur, "check": check, "legal": legal, "san": san,
                "mate": check and not legal}

    def evaluate(self, fen, moves, depth):
        with self.ui_lock:
            lines = self.ui_engine.analyse(fen, moves, f"depth {depth}", multipv=1)
        return lines[0] if lines else None

    def load(self, path):
        n = 0
        with open(path) as f:
            for line in f:
                line = line.strip()
                if line:
                    p = json.loads(line)
                    if "san" not in p:
                        with self.ui_lock:
                            p["san"] = mp.san_line(self.ui_engine, p["fen"], p["solution"])
                    out, self.out_path = self.out_path, None  # don't re-append loaded puzzles
                    n += self.add_puzzle(p)
                    self.out_path = out
        return n


def make_handler(state):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def send_json(self, obj, code=200):
            body = json.dumps(obj).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def body(self):
            n = int(self.headers.get("Content-Length") or 0)
            return json.loads(self.rfile.read(n) or b"{}")

        def do_GET(self):
            u = urlparse(self.path)
            if u.path == "/":
                with open(HTML_PATH, "rb") as f:
                    html = f.read()
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(html)))
                self.send_header("Cache-Control", "no-store")
                self.end_headers()
                self.wfile.write(html)
            elif u.path == "/api/state":
                since = int(parse_qs(u.query).get("since", ["0"])[0])
                self.send_json(state.snapshot(since))
            else:
                self.send_json({"error": "not found"}, 404)

        def do_POST(self):
            path = urlparse(self.path).path
            try:
                b = self.body()
                if path == "/api/start":
                    state.start(b)
                    self.send_json({"ok": True})
                elif path == "/api/stop":
                    state.stop_event.set()
                    self.send_json({"ok": True})
                elif path == "/api/clear":
                    with state.lock:
                        if state.running:
                            raise RuntimeError("stop the generator first")
                        state.puzzles.clear()
                        state.fens.clear()
                    self.send_json({"ok": True})
                elif path == "/api/position":
                    self.send_json(state.position(b["fen"], b.get("moves", [])))
                elif path == "/api/evaluate":
                    depth = max(4, min(30, int(b.get("depth", 18))))
                    self.send_json({"line": state.evaluate(b["fen"], b.get("moves", []), depth)})
                else:
                    self.send_json({"error": "not found"}, 404)
            except (RuntimeError, ValueError, KeyError) as e:
                self.send_json({"error": str(e)}, 400)

    return Handler


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--engine", default=mp.DEFAULT_ENGINE)
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8765)
    ap.add_argument("--out", help="append generated puzzles to this JSONL file")
    ap.add_argument("--load", help="preload puzzles from a JSONL file")
    ap.add_argument("--no-browser", action="store_true")
    args = ap.parse_args()

    if not os.path.isfile(args.engine):
        sys.exit(f"stockfish binary not found at {args.engine} — install Stockfish, set $STOCKFISH, or pass --engine")

    state = State(args.engine, args.out)
    if args.load and os.path.isfile(args.load):
        print(f"loaded {state.load(args.load)} puzzles from {args.load}")

    server = ThreadingHTTPServer((args.host, args.port), make_handler(state))
    url = f"http://{args.host}:{args.port}/"
    print(f"Mate puzzle UI running at {url}  (Ctrl+C to quit)")
    if not args.no_browser:
        threading.Timer(0.5, lambda: webbrowser.open(url)).start()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        state.stop_event.set()
        state.ui_engine.quit()
        server.server_close()


if __name__ == "__main__":
    main()
