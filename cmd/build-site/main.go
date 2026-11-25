// Command build-site turns a puzzle bank (JSONL) into a static website.
//
//	build-site -bank puzzles/bank.jsonl -config site.json -wasm third_party/stockfish-js -out dist
//
// Puzzles are de-duplicated by position and numbered in bank order, so keep
// the bank append-only to keep puzzle URLs stable. Stockfish (native) is used
// at build time to precompute every position along each solution, so the main
// line works in the browser before the WASM engine has loaded.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/kgunjikar/chess-puzzles/internal/engine"
	"github.com/kgunjikar/chess-puzzles/internal/puzzle"
	"github.com/kgunjikar/chess-puzzles/internal/site"
)

func main() {
	bank := flag.String("bank", "puzzles/bank.jsonl", "puzzle bank file(s), comma-separated or glob")
	cfgPath := flag.String("config", "site.json", "site configuration")
	wasmDir := flag.String("wasm", "third_party/stockfish-js", "directory with the stockfish.js engine files")
	out := flag.String("out", "dist", "output directory (replaced)")
	enginePath := flag.String("engine", engine.Find(), "native stockfish used to precompute positions")
	serve := flag.String("serve", "", "after building, serve the site on this address (e.g. localhost:8080) for local testing")
	flag.Parse()

	cfg, err := site.LoadConfig(*cfgPath)
	if err != nil {
		fatal(err)
	}
	var paths []string
	for _, pat := range strings.Split(*bank, ",") {
		m, err := filepath.Glob(strings.TrimSpace(pat))
		if err != nil || len(m) == 0 {
			fatal(fmt.Errorf("no bank file matches %q", pat))
		}
		paths = append(paths, m...)
	}
	puzzles, err := readBank(paths)
	if err != nil {
		fatal(err)
	}

	eng, err := engine.Start(*enginePath, 1, 16)
	if err != nil {
		fatal(fmt.Errorf("start stockfish (%s): %w", *enginePath, err))
	}
	defer eng.Quit()

	start := time.Now()
	entries := make([]site.Entry, 0, len(puzzles))
	skipped := 0
	for _, p := range puzzles {
		e, err := prepare(eng, p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", p.FEN, err)
			skipped++
			continue
		}
		e.ID = len(entries) + 1
		entries = append(entries, e)
	}
	if err := site.Build(cfg, entries, *wasmDir, *out); err != nil {
		fatal(err)
	}
	counts := map[int]int{}
	for _, e := range entries {
		counts[e.MateIn]++
	}
	fmt.Printf("built %s: %d puzzles (M1 %d, M2 %d, M3 %d, M4 %d, M5 %d), %d skipped, in %.1fs\n",
		*out, len(entries), counts[1], counts[2], counts[3], counts[4], counts[5], skipped, time.Since(start).Seconds())

	if *serve != "" {
		eng.Quit()
		fmt.Printf("serving %s at http://%s/  (Ctrl+C to stop)\n", *out, *serve)
		fatal(http.ListenAndServe(*serve, staticHandler(*out)))
	}
}

// staticHandler serves dir like a static host: directory index.html and a 404 page.
func staticHandler(dir string) http.Handler {
	fsrv := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.FromSlash(path.Clean("/"+r.URL.Path)))
		if st, err := os.Stat(p); err != nil || (st.IsDir() && !exists(filepath.Join(p, "index.html"))) {
			page, _ := os.ReadFile(filepath.Join(dir, "404.html"))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(page)
			return
		}
		fsrv.ServeHTTP(w, r)
	})
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func readBank(paths []string) ([]puzzle.Puzzle, error) {
	seen := map[string]bool{}
	var out []puzzle.Puzzle
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		line := 0
		for sc.Scan() {
			line++
			text := strings.TrimSpace(sc.Text())
			if text == "" {
				continue
			}
			var p puzzle.Puzzle
			if err := json.Unmarshal([]byte(text), &p); err != nil {
				f.Close()
				return nil, fmt.Errorf("%s:%d: %w", path, line, err)
			}
			// Same placement + side + castling + en passant = same puzzle.
			key := strings.Join(strings.Fields(p.FEN)[:4], " ")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, p)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// prepare validates a puzzle against the engine and precomputes each position
// along the solution.
func prepare(eng *engine.Engine, p puzzle.Puzzle) (site.Entry, error) {
	if p.MateIn < 1 || p.MateIn > 5 || len(p.Solution) != 2*p.MateIn-1 {
		return site.Entry{}, fmt.Errorf("solution length %d does not match mate in %d", len(p.Solution), p.MateIn)
	}
	e := site.Entry{Puzzle: p}
	for k := 0; k <= len(p.Solution); k++ {
		moves := p.Solution[:k]
		pos, err := eng.Describe(p.FEN, moves)
		if err != nil {
			return e, err
		}
		legal, err := eng.LegalMoves(p.FEN, moves)
		if err != nil {
			return e, err
		}
		if k < len(p.Solution) && !contains(legal, p.Solution[k]) {
			return e, fmt.Errorf("move %s is illegal", p.Solution[k])
		}
		e.Positions = append(e.Positions, site.Position{FEN: pos.FEN, Check: pos.InCheck, Legal: legal})
	}
	last := e.Positions[len(e.Positions)-1]
	if !last.Check || len(last.Legal) != 0 {
		return e, fmt.Errorf("solution does not end in checkmate")
	}
	if len(p.SAN) != len(p.Solution) {
		san, err := puzzle.SANLine(eng, p.FEN, p.Solution)
		if err != nil {
			return e, err
		}
		e.SAN = san
	}
	if p.ToMove == "" {
		e.ToMove = "white"
		if strings.Fields(p.FEN)[1] == "b" {
			e.ToMove = "black"
		}
	}
	return e, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "build-site:", err)
	os.Exit(1)
}
