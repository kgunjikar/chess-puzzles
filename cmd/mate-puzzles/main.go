// Command mate-puzzles generates mate-in-N chess puzzles with Stockfish.
//
//	mate-puzzles                          # 3 puzzles each for mate in 1..5
//	mate-puzzles -mates 2-3 -n 10         # 10 mate-in-2 and 10 mate-in-3
//	mate-puzzles -mates 4,5 -n 5 -board -out puzzles.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kgunjikar/chess-puzzles/internal/engine"
	"github.com/kgunjikar/chess-puzzles/internal/puzzle"
)

func main() {
	d := puzzle.DefaultOptions()
	opt := d
	enginePath := flag.String("engine", engine.Find(), "path to stockfish binary")
	matesStr := flag.String("mates", "1-5", "mate lengths, e.g. '1-5', '2,4'")
	var count int
	flag.IntVar(&count, "n", 3, "puzzles per mate length")
	flag.IntVar(&count, "count", 3, "puzzles per mate length (same as -n)")
	flag.IntVar(&opt.ScanDepth, "scan-depth", d.ScanDepth, "shallow search depth used to spot mates")
	flag.IntVar(&opt.VerifyDepth, "verify-depth", d.VerifyDepth, "search depth used to verify puzzles")
	flag.IntVar(&opt.StrongNodes, "strong-nodes", d.StrongNodes, "node budget for the strong side's moves")
	flag.Float64Var(&opt.Blunder, "blunder", d.Blunder, "probability the weak side plays a random move")
	flag.IntVar(&opt.MinPly, "min-ply", d.MinPly, "don't take puzzles from the first N plies")
	flag.BoolVar(&opt.AllowDuals, "allow-duals", false, "only require the FIRST move to be unique")
	threads := flag.Int("threads", 1, "engine threads")
	hash := flag.Int("hash", 64, "engine hash size in MB")
	seed := flag.Int64("seed", 0, "random seed (0 = time-based)")
	outPath := flag.String("out", "", "append puzzles to this JSONL file")
	showBoard := flag.Bool("board", false, "print an ASCII board for each puzzle")
	maxGames := flag.Int64("max-games", 100000, "give up after this many games")
	flag.Parse()

	mates, err := puzzle.ParseRange(*matesStr)
	if err != nil {
		fatal(err)
	}
	opt.Mates = mates
	if st, err := os.Stat(*enginePath); err != nil || st.IsDir() {
		fatal(fmt.Errorf("stockfish binary not found at %s — install Stockfish, set $STOCKFISH, run `make stockfish`, or pass -engine", *enginePath))
	}

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))
	eng, err := engine.Start(*enginePath, *threads, *hash)
	if err != nil {
		fatal(err)
	}
	defer eng.Quit()

	var out *os.File
	if *outPath != "" {
		if out, err = os.OpenFile(*outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
			fatal(err)
		}
		defer out.Close()
	}

	var interrupted atomic.Bool
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() { <-sig; interrupted.Store(true); fmt.Fprintln(os.Stderr, "\ninterrupted") }()

	found := map[int]int{}
	for _, n := range mates {
		found[n] = 0
	}
	stats := &puzzle.Stats{}
	wanted := func(n int) bool { f, ok := found[n]; return ok && f < count }
	done := func() bool {
		if interrupted.Load() || stats.Games.Load() >= *maxGames {
			return true
		}
		for _, n := range mates {
			if found[n] < count {
				return false
			}
		}
		return true
	}

	start := time.Now()
	err = puzzle.Generate(eng, rng, opt, wanted, done, stats, func(p puzzle.Puzzle) {
		found[p.MateIn]++
		fmt.Printf("\n=== Mate in %d (%s to move) [%d/%d] ===\n", p.MateIn, p.ToMove, found[p.MateIn], count)
		if *showBoard {
			fmt.Println(p.Board)
		}
		fmt.Printf("FEN:      %s\n", p.FEN)
		fmt.Printf("Solution: %s   (%s)\n", strings.Join(p.SAN, " "), strings.Join(p.Solution, " "))
		if out != nil {
			b, _ := json.Marshal(p)
			_, _ = out.Write(append(b, '\n'))
		}
	})
	if err != nil && !interrupted.Load() {
		fmt.Fprintln(os.Stderr, "error:", err)
	}

	parts := make([]string, 0, len(mates))
	for _, n := range mates {
		parts = append(parts, fmt.Sprintf("M%d: %d", n, found[n]))
	}
	fmt.Fprintf(os.Stderr, "\nDone in %.1fs over %d games — %s\n",
		time.Since(start).Seconds(), stats.Games.Load(), strings.Join(parts, ", "))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
