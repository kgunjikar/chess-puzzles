# chess-puzzles

Generates **mate-in-1 to mate-in-5** chess puzzles using the [Stockfish](https://stockfishchess.org/) engine, and lets you solve them in a local web UI.

Pure Python 3 (standard library only). Talks to Stockfish over UCI.

## Requirements

- Python 3.8+
- A Stockfish binary. It is looked up in this order:
  1. `--engine /path/to/stockfish`
  2. `$STOCKFISH`
  3. `./stockfish` next to the scripts
  4. `stockfish` on your `PATH` (e.g. `brew install stockfish`, `apt install stockfish`)
  5. a sibling checkout at `../Stockfish/src/stockfish`

## Web UI

```bash
python3 mate_puzzles_ui.py                       # opens http://127.0.0.1:8765
python3 mate_puzzles_ui.py --out puzzles.jsonl --load puzzles.jsonl   # persist between runs
```

- **Generator:** choose mate lengths, puzzles per length, verification depth, blunder rate, duals, plus advanced options (scan depth, winning-side strength, min ply, threads, seed). Live progress and stats.
- **Board:** click or drag to move. Correct moves get an automatic defender reply; wrong moves are taken back. Alternative moves that still force mate in time are accepted.
- **Tools:** hint, show solution, retry, flip, copy FEN, open in Lichess analysis, export JSONL.
- Keyboard: `←` `→` navigate · `H` hint · `S` solution · `R` retry · `F` flip.

## Command line

```bash
python3 mate_puzzles.py                          # 3 puzzles each for mate in 1..5
python3 mate_puzzles.py --mates 2-3 -n 10        # 10 mate-in-2 and 10 mate-in-3
python3 mate_puzzles.py --mates 4,5 -n 5 --board --out puzzles.jsonl
```

```
=== Mate in 3 (white to move) [1/1] ===
FEN:      2b1kq2/1pp1p2p/2n5/pB1p1Q2/3P3N/1NP5/PP4PP/R1B1K2R w - - 1 21
Solution: Qxc8+ Kf7 Rf1+ Kg7 Qxf8#   (f5c8 e8f7 h1f1 f7g7 c8f8)
```

Run `python3 mate_puzzles.py --help` for all options.

## How it works

1. **Lopsided self-play.** One side plays engine moves; the other plays a random move some of the time. Forced mates show up quickly.
2. **Scan.** Every position (after a minimum ply) gets a shallow search. A forced mate in 1–5 makes it a candidate.
3. **Verify.** A deeper search builds the solution move by move:
   - each attacking move must be the *only* move that mates fastest (no duals; `--allow-duals` relaxes this to the first move only),
   - the defender always plays the longest resistance,
   - the final position must be checkmate.

## Output format

One JSON object per line:

```json
{"mate_in": 2, "fen": "r3q3/2pn1k1p/5b2/pP1P1P2/P3b3/8/3B1P1p/1RK5 b - - 0 30", "to_move": "black",
 "solution": ["h2h1q", "d2e1", "h1e1"], "san": ["h1=Q+", "Be1", "Qxe1#"]}
```

`examples/sample_puzzles.jsonl` has a few generated puzzles.
