# chess-puzzles

Generates **mate-in-1 to mate-in-5** chess puzzles using the [Stockfish](https://stockfishchess.org/) engine, and lets you solve them in a local web UI.

Pure Go (standard library only). Talks to Stockfish over UCI as a child process; the UI's HTML is embedded in the binary.

## Requirements

- Go 1.22+
- A Stockfish binary. It is looked up in this order:
  1. `-engine /path/to/stockfish`
  2. `$STOCKFISH`
  3. `bin/stockfish` or `stockfish` next to the binary or in the working directory
  4. `stockfish` on your `PATH` (e.g. `brew install stockfish`, `apt install stockfish`)
  5. a sibling checkout at `../Stockfish/src/stockfish`

To build a pinned version locally instead of installing one:

```bash
make stockfish                          # clones sf_17.1 into third_party/ and builds bin/stockfish
make stockfish STOCKFISH_TAG=sf_17      # another release tag
```

## Build

```bash
make build        # -> bin/mate-puzzles, bin/mate-puzzles-ui
make test
```

## Public website (static, ad-supported)

The public site is plain static files: no server, no accounts and no per-visitor engine cost. Everything runs in the visitor's browser and anyone can use it without signing up.

1. **Puzzles are generated offline** into an append-only bank, `puzzles/bank.jsonl`.
2. **`build-site`** turns the bank into one page per puzzle (`/puzzle/123/`), list pages (`/mate-in-N/`), a home page with a daily puzzle, `sitemap.xml`, `robots.txt` and `ads.txt`.
   It also runs native Stockfish to precompute every position along each solution, so the stored line plays instantly.
3. **Stockfish 19 runs in the visitor's browser** (the [stockfish.js](https://github.com/nmrugg/stockfish.js) "lite, single-threaded" WASM build, 1.8 MB, loaded in the background).
   It checks moves that leave the stored line and gives hints. The single-threaded build needs no cross-origin-isolation headers. Those headers (COOP/COEP) would block AdSense, which rules out the multi-threaded builds such as lichess-org/stockfish.wasm.
4. **Progress** (solved, streak) lives only in the browser's `localStorage`. Nothing is sent to a server.
5. **AdSense** tags, ad slots and `ads.txt` are emitted only when `site.json` has an AdSense client ID. Ads go beside or below the board, never over it.

```bash
make bank N=100          # generate 100 more puzzles of each length into puzzles/bank.jsonl
make serve-site          # build into dist/ and serve it at http://localhost:8080
make site                # just build dist/
```

Keep `puzzles/bank.jsonl` **append-only**. Puzzle IDs (and so URLs) are assigned in bank order, after removing duplicate positions.

### Configure (`site.json`)

| Key | Purpose |
|---|---|
| `title`, `tagline` | Branding |
| `base_url` | e.g. `https://matepuzzles.example`; enables canonical URLs and `sitemap.xml` |
| `source_url` | Linked from the footer and the About page (needed for GPL compliance) |
| `contact_email` | Shown on the About and Privacy pages |
| `adsense.client`, `adsense.slot_sidebar`, `adsense.slot_inline` | AdSense publisher ID (`ca-pub-…`) and ad-unit IDs |

### Deploy

`dist/` is plain static files, so any static host works:

- **Cloudflare Pages or Netlify:** publish `dist/`. The generated `_headers` file sets caching.
- **Firebase Hosting:** `npm i -g firebase-tools && firebase login && firebase use --add <project-id>`, then `make deploy-firebase` (uses `firebase.json`).
- **GitHub Pages** or any web server also works; you just lose the long-lived cache headers.

Then point your domain at it.

### Ads (AdSense)

1. Get the site live on your own domain with a good number of puzzles, then apply at <https://adsense.google.com>.
2. Once approved, put your `ca-pub-…` ID and two **display ad units** into `site.json` and redeploy. `ads.txt` is generated automatically.
3. In AdSense → **Privacy & messaging**, turn on the **GDPR consent message**. EEA/UK/Swiss visitors must get Google-certified consent, and the privacy page assumes this is on.

### Licensing

Stockfish and stockfish.js are GPLv3. The site serves them unmodified and links to the license (`/assets/engine/Copying.txt`) and to both source repositories on `/about/`. Keep `source_url` pointing at this repo.

## Web UI (generator server)

A live server version for generating puzzles on demand, e.g. on your laptop or for a club. For the public site, use the static build above.

There is no sign-in. Nothing is written to disk:

- **Workspaces:** each browser gets an anonymous random ID in a cookie, so two people using the server don't see each other's puzzles.
- **Puzzles** live in server memory per browser, and are forgotten after `-evict-after` of inactivity or on restart. Use the UI's *Export JSONL* button to keep them.
- **Per-browser worker:** each workspace gets its own goroutine that owns a private Stockfish process and serves that browser's board requests. It shuts down after `-idle-timeout` and restarts on the next request. Generating puzzles runs in a second goroutine with its own engine.
- **Limits:** `-max-users` (workspaces in memory), `-max-generators` (generators running at once), `-max-threads` (engine threads per generator).

```bash
./bin/mate-puzzles-ui                                        # http://localhost:8765
./bin/mate-puzzles-ui -load examples/sample_puzzles.jsonl    # every new workspace starts with these puzzles
./bin/mate-puzzles-ui -host 0.0.0.0                          # reachable from your network
```

It listens on `127.0.0.1` by default. With `-host 0.0.0.0` anyone who can reach the port can use your CPU, within the limits above.

- **Generator:** choose mate lengths, puzzles per length, verification depth, blunder rate, duals, plus advanced options (scan depth, winning-side strength, min ply, threads, seed). Live progress and stats.
- **Board:** click or drag to move. Correct moves get an automatic defender reply; wrong moves are taken back. Alternative moves that still force mate in time are accepted.
- **Tools:** hint, show solution, retry, flip, copy FEN, open in Lichess analysis, export JSONL.
- Keyboard: `←` `→` navigate · `H` hint · `S` solution · `R` retry · `F` flip.
- Solved/seen marks and settings are kept in the browser's localStorage.

## Command line

```bash
./bin/mate-puzzles                          # 3 puzzles each for mate in 1..5
./bin/mate-puzzles -mates 2-3 -n 10         # 10 mate-in-2 and 10 mate-in-3
./bin/mate-puzzles -mates 4,5 -n 5 -board -out puzzles.jsonl
```

```
=== Mate in 3 (black to move) [1/1] ===
FEN:      1rbqkbr1/p1p1pp1p/p7/3p4/2P4p/B1n1PQ2/P2P1PPP/1R2KB1R b K - 1 12
Solution: Rxb1+ Bc1 Rxc1+ Qd1 Rxd1#   (b8b1 a3c1 b1c1 f3d1 c1d1)
```

Run `./bin/mate-puzzles -h` for all options.

## Layout

```
cmd/mate-puzzles/      CLI generator
cmd/mate-puzzles-ui/   generator web UI server
cmd/build-site/        static site generator (+ local preview server)
internal/engine/       UCI process wrapper (Stockfish discovery, analyse, perft, d)
internal/puzzle/       game generation, verification, SAN conversion
internal/web/          per-browser sessions/workers, HTTP API, embedded page
internal/site/         static site templates, browser JS (board, WASM engine, progress), CSS
puzzles/bank.jsonl     the puzzle bank (append-only)
```

## How it works

1. **Lopsided self-play.** One side plays engine moves; the other plays a random move some of the time. Forced mates show up quickly.
2. **Scan.** Every position (after a minimum ply) gets a shallow search. A forced mate in 1–5 makes it a candidate.
3. **Verify.** A deeper search builds the solution move by move:
   - each attacking move must be the *only* move that mates fastest (no duals; `-allow-duals` relaxes this to the first move only),
   - the defender always plays the longest resistance,
   - the final position must be checkmate.

## Output format

One JSON object per line:

```json
{"mate_in": 2, "fen": "r3q3/2pn1k1p/5b2/pP1P1P2/P3b3/8/3B1P1p/1RK5 b - - 0 30", "to_move": "black",
 "solution": ["h2h1q", "d2e1", "h1e1"], "san": ["h1=Q+", "Be1", "Qxe1#"]}
```

`examples/sample_puzzles.jsonl` has a few generated puzzles.
