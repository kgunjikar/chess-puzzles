#!/usr/bin/env python3
"""
Mate-in-N puzzle generator driven by the Stockfish engine (UCI).

How it works
  1. Plays lopsided games: a "strong" side (engine, small node budget) against a
     "weak" side that blunders randomly some of the time. This quickly produces
     positions with forced mates.
  2. Every position is scanned with a shallow search. If the side to move has a
     forced mate in 1..5, the position becomes a candidate.
  3. Candidates are verified with deeper searches, building the solution move by
     move:
       - the attacker's move at every step must be the *only* move that mates in
         the shortest number of moves (no "duals"),
       - the defender always plays the longest resistance,
       - the final position must be checkmate.

No third-party Python packages are needed.

Examples
  python3 mate_puzzles.py                      # 3 puzzles each for mate in 1..5
  python3 mate_puzzles.py --mates 2-3 -n 10    # 10 mate-in-2 and 10 mate-in-3
  python3 mate_puzzles.py --mates 4 -n 5 --board --out puzzles.jsonl
"""

import argparse
import json
import os
import random
import re
import shutil
import subprocess
import sys
import time

MOVE_LINE = re.compile(r"^([a-h][1-8][a-h][1-8][qrbn]?): \d+$")
def find_engine():
    """$STOCKFISH, ./stockfish next to this script, `stockfish` on PATH, or a sibling Stockfish checkout."""
    here = os.path.dirname(os.path.abspath(__file__))
    candidates = [
        os.environ.get("STOCKFISH"),
        os.path.join(here, "stockfish"),
        shutil.which("stockfish"),
        os.path.join(here, "..", "Stockfish", "src", "stockfish"),
        os.path.join(here, "..", "src", "stockfish"),
    ]
    for c in candidates:
        if c and os.path.isfile(c) and os.access(c, os.X_OK):
            return os.path.abspath(c)
    return "stockfish"


DEFAULT_ENGINE = find_engine()


class Engine:
    def __init__(self, path, threads=1, hash_mb=64):
        self.p = subprocess.Popen(
            [path], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True, bufsize=1,
        )
        self.send("uci")
        self.read_until(lambda l: l == "uciok")
        self.send(f"setoption name Threads value {threads}")
        self.send(f"setoption name Hash value {hash_mb}")
        self.multipv = None
        self.sync()

    def send(self, cmd):
        self.p.stdin.write(cmd + "\n")
        self.p.stdin.flush()

    def read_until(self, pred):
        lines = []
        while True:
            line = self.p.stdout.readline()
            if not line:
                raise RuntimeError("engine terminated unexpectedly")
            line = line.strip()
            lines.append(line)
            if pred(line):
                return lines

    def sync(self):
        self.send("isready")
        return self.read_until(lambda l: l == "readyok")[:-1]

    def set_multipv(self, n):
        if n != self.multipv:
            self.send(f"setoption name MultiPV value {n}")
            self.multipv = n

    @staticmethod
    def pos_cmd(fen, moves):
        base = "position startpos" if fen is None else f"position fen {fen}"
        return base + (" moves " + " ".join(moves) if moves else "")

    def legal_moves(self, fen, moves=()):
        self.send(self.pos_cmd(fen, moves))
        self.send("go perft 1")
        out = self.read_until(lambda l: l.startswith("Nodes searched"))
        return [m.group(1) for l in out if (m := MOVE_LINE.match(l))]

    def describe(self, fen, moves=()):
        """Returns (fen, in_check, ascii_board)."""
        self.send(self.pos_cmd(fen, moves))
        self.send("d")
        out = self.sync()
        fen_out = next(l[5:] for l in out if l.startswith("Fen: "))
        checkers = next((l[len("Checkers:"):].strip() for l in out if l.startswith("Checkers:")), "")
        board = "\n".join(l for l in out if l.startswith(("+", "|", "a ")))
        return fen_out, bool(checkers), board

    def analyse(self, fen, moves, limit, multipv=1):
        """Returns list of dicts {kind: 'mate'|'cp', value, pv} ordered by multipv."""
        self.set_multipv(multipv)
        self.send(self.pos_cmd(fen, moves))
        self.send(f"go {limit}")
        lines = {}
        for l in self.read_until(lambda l: l.startswith("bestmove")):
            if not l.startswith("info") or " pv " not in l or "bound" in l:
                continue
            t = l.split()
            idx = int(t[t.index("multipv") + 1]) if "multipv" in t else 1
            si = t.index("score")
            lines[idx] = {"kind": t[si + 1], "value": int(t[si + 2]),
                          "pv": t[t.index("pv") + 1:]}
        return [lines[i] for i in sorted(lines)]

    def bestmove(self, fen, moves, limit):
        self.send(self.pos_cmd(fen, moves))
        self.send(f"go {limit}")
        last = self.read_until(lambda l: l.startswith("bestmove"))[-1].split()
        return last[1] if len(last) > 1 and last[1] != "(none)" else None

    def new_game(self):
        self.send("ucinewgame")
        self.sync()

    def quit(self):
        try:
            self.send("quit")
            self.p.wait(timeout=2)
        except Exception:
            self.p.kill()


def mate_in(line):
    """Positive mate distance for side to move, else None."""
    if line and line["kind"] == "mate" and line["value"] > 0:
        return line["value"]
    return None


def verify(eng, fen, depth, allow_duals):
    """Builds and checks the full solution. Returns (n, solution) or None."""
    limit = f"depth {depth}"
    lines = eng.analyse(fen, [], limit, multipv=2)
    n = mate_in(lines[0] if lines else None)
    if n is None:
        return None

    sol = []
    for remaining in range(n, 0, -1):
        if sol:
            lines = eng.analyse(fen, sol, limit, multipv=2)
        if mate_in(lines[0]) != remaining:
            return None  # engine line inconsistent; reject
        # Another attacker move that also mates in <= remaining -> not unique.
        if len(lines) > 1 and (m2 := mate_in(lines[1])) is not None and m2 <= remaining:
            if not sol or not allow_duals:
                return None
        sol.append(lines[0]["pv"][0])
        if remaining == 1:
            break
        # Defender: longest resistance.
        reply = eng.analyse(fen, sol, limit, multipv=1)
        if not reply or not reply[0]["pv"]:
            return None
        sol.append(reply[0]["pv"][0])

    # Final position must be checkmate.
    if eng.legal_moves(fen, sol):
        return None
    if not eng.describe(fen, sol)[1]:
        return None
    return n, sol


def board_from_fen(fen):
    """{square: piece} from the placement field of a FEN."""
    board = {}
    for r, row in enumerate(fen.split()[0].split("/")):
        f = 0
        for ch in row:
            if ch.isdigit():
                f += int(ch)
            else:
                board["abcdefgh"[f] + str(8 - r)] = ch
                f += 1
    return board


def san_line(eng, fen, moves):
    """Converts a UCI move list (from fen) into SAN, using the engine for legality/check."""
    out = []
    for i, mv in enumerate(moves):
        cur = eng.describe(fen, moves[:i])[0]
        board = board_from_fen(cur)
        legal = eng.legal_moves(fen, moves[:i])
        frm, to, promo = mv[:2], mv[2:4], mv[4:]
        piece = board.get(frm, "?")
        kind = piece.upper()
        if kind == "K" and abs(ord(frm[0]) - ord(to[0])) == 2:
            san = "O-O" if to[0] == "g" else "O-O-O"
        elif kind == "P":
            capture = frm[0] != to[0]
            san = (frm[0] + "x" if capture else "") + to + ("=" + promo.upper() if promo else "")
        else:
            others = [m[:2] for m in legal if m[2:4] == to and m[:2] != frm and board.get(m[:2]) == piece]
            dis = ""
            if others:
                if all(o[0] != frm[0] for o in others):
                    dis = frm[0]
                elif all(o[1] != frm[1] for o in others):
                    dis = frm[1]
                else:
                    dis = frm
            san = kind + dis + ("x" if to in board else "") + to
        if eng.describe(fen, moves[:i + 1])[1]:
            san += "+" if eng.legal_moves(fen, moves[:i + 1]) else "#"
        out.append(san)
    return out


def generate(eng, rng, mates, wanted, scan_depth=10, verify_depth=22, strong_nodes=3000,
             blunder=0.35, min_ply=16, allow_duals=False, stop=lambda: False, stats=None):
    """
    Yields verified puzzles forever (until stop() is true).
    wanted(n) -> bool says whether a mate-in-n puzzle is still needed.
    stats, if given, is a dict updated with games/positions/candidates/rejected counts.
    """
    stats = stats if stats is not None else {}
    for k in ("games", "positions", "candidates", "rejected"):
        stats.setdefault(k, 0)
    seen = set()
    max_n = max(mates)

    while not stop():
        stats["games"] += 1
        eng.new_game()
        moves = []
        strong = rng.choice("wb")
        # A few random opening moves for variety.
        for _ in range(rng.randint(2, 6)):
            legal = eng.legal_moves(None, moves)
            if not legal:
                break
            moves.append(rng.choice(legal))

        while len(moves) < 300 and not stop():
            legal = eng.legal_moves(None, moves)
            if not legal:
                break
            stm = "w" if len(moves) % 2 == 0 else "b"

            if len(moves) >= min_ply:
                stats["positions"] += 1
                scan = eng.analyse(None, moves, f"depth {scan_depth}")
                n = mate_in(scan[0] if scan else None)
                if n is not None and n <= max_n:
                    fen, _, board = eng.describe(None, moves)
                    key = " ".join(fen.split()[:4])
                    if key not in seen and wanted(n):
                        seen.add(key)
                        stats["candidates"] += 1
                        res = verify(eng, fen, verify_depth, allow_duals)
                        if res and res[0] in mates and wanted(res[0]):
                            n, sol = res
                            yield {"mate_in": n, "fen": fen,
                                   "to_move": "white" if stm == "w" else "black",
                                   "solution": sol, "san": san_line(eng, fen, sol),
                                   "board": board}
                        else:
                            stats["rejected"] += 1
                    # Game is effectively decided; start a fresh one.
                    break

            if stm == strong:
                mv = eng.bestmove(None, moves, f"nodes {strong_nodes}")
            elif rng.random() < blunder:
                mv = rng.choice(legal)
            else:
                mv = eng.bestmove(None, moves, "depth 2")
            if mv is None:
                break
            moves.append(mv)


def parse_range(s):
    out = set()
    for part in s.split(","):
        a, _, b = part.partition("-")
        out.update(range(int(a), int(b or a) + 1))
    if not out or min(out) < 1 or max(out) > 5:
        raise argparse.ArgumentTypeError("mate depths must be within 1..5")
    return sorted(out)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--engine", default=DEFAULT_ENGINE, help="path to stockfish binary")
    ap.add_argument("--mates", type=parse_range, default=[1, 2, 3, 4, 5], help="mate lengths, e.g. '1-5', '2,4' (default 1-5)")
    ap.add_argument("-n", "--count", type=int, default=3, help="puzzles per mate length (default 3)")
    ap.add_argument("--scan-depth", type=int, default=10, help="shallow search depth used to spot mates (default 10)")
    ap.add_argument("--verify-depth", type=int, default=22, help="search depth used to verify puzzles (default 22)")
    ap.add_argument("--strong-nodes", type=int, default=3000, help="node budget for the strong side's moves")
    ap.add_argument("--blunder", type=float, default=0.35, help="probability the weak side plays a random move")
    ap.add_argument("--min-ply", type=int, default=16, help="don't take puzzles from the first N plies")
    ap.add_argument("--allow-duals", action="store_true", help="only require the FIRST move to be unique")
    ap.add_argument("--threads", type=int, default=1)
    ap.add_argument("--hash", type=int, default=64)
    ap.add_argument("--seed", type=int, help="random seed")
    ap.add_argument("--out", help="append puzzles to this JSONL file")
    ap.add_argument("--board", action="store_true", help="print an ASCII board for each puzzle")
    ap.add_argument("--max-games", type=int, default=100000)
    args = ap.parse_args()

    if not os.path.isfile(args.engine):
        sys.exit(f"stockfish binary not found at {args.engine} — install Stockfish, set $STOCKFISH, or pass --engine")

    rng = random.Random(args.seed)
    eng = Engine(args.engine, args.threads, args.hash)
    found = {n: 0 for n in args.mates}
    stats = {}
    out = open(args.out, "a") if args.out else None
    start = time.time()

    def done():
        return all(found[n] >= args.count for n in args.mates) or stats.get("games", 0) >= args.max_games

    try:
        for p in generate(eng, rng, args.mates, lambda n: n in found and found[n] < args.count,
                          args.scan_depth, args.verify_depth, args.strong_nodes, args.blunder,
                          args.min_ply, args.allow_duals, stop=done, stats=stats):
            n = p["mate_in"]
            found[n] += 1
            print(f"\n=== Mate in {n} ({p['to_move']} to move) [{found[n]}/{args.count}] ===")
            if args.board:
                print(p["board"])
            print(f"FEN:      {p['fen']}")
            print(f"Solution: {' '.join(p['san'])}   ({' '.join(p['solution'])})", flush=True)
            if out:
                out.write(json.dumps({k: v for k, v in p.items() if k != "board"}) + "\n")
                out.flush()
    except KeyboardInterrupt:
        print("\ninterrupted", file=sys.stderr)
    finally:
        eng.quit()
        if out:
            out.close()

    summary = ", ".join(f"M{n}: {found[n]}" for n in args.mates)
    print(f"\nDone in {time.time() - start:.1f}s over {stats.get('games', 0)} games — {summary}", file=sys.stderr)


if __name__ == "__main__":
    main()
