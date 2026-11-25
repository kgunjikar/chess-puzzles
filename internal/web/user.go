package web

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kgunjikar/chess-puzzles/internal/engine"
	"github.com/kgunjikar/chess-puzzles/internal/puzzle"
)

// session is one visitor's (browser's) in-memory workspace.
//
// Each session runs its own goroutine (loop) that owns a private Stockfish
// process and serves that user's board requests one at a time. The goroutine
// and its engine are started on demand and shut down after idleTimeout; the
// puzzle list stays in memory until the session is evicted. Generation runs in a
// second goroutine with its own engine while the user has it switched on.
type session struct {
	id  string
	hub *Server

	lastSeen atomic.Int64 // unix nanos

	// Board worker lifecycle.
	wmu     sync.Mutex
	alive   bool
	pending int
	jobs    chan job

	// Generator and puzzle state.
	mu       sync.Mutex
	puzzles  []puzzle.Puzzle
	fens     map[string]bool
	running  bool
	stopFlag atomic.Bool
	stats    *puzzle.Stats
	target   map[int]int
	found    map[int]int
	started  time.Time
	finished time.Time
	errMsg   string
}

type job struct {
	fn   func(*engine.Engine) error
	done chan error
}

func newSession(hub *Server, id string) *session {
	s := &session{
		id: id, hub: hub, fens: map[string]bool{}, stats: &puzzle.Stats{},
		target: map[int]int{}, found: map[int]int{},
	}
	s.touch()
	for _, p := range hub.starter {
		s.add(p)
	}
	return s
}

func (s *session) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

func (s *session) idleFor() time.Duration {
	return time.Since(time.Unix(0, s.lastSeen.Load()))
}

func (s *session) generating() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// do runs fn on this user's worker goroutine, starting it if needed.
func (s *session) do(ctx context.Context, fn func(*engine.Engine) error) error {
	s.wmu.Lock()
	if !s.alive {
		s.alive = true
		s.jobs = make(chan job)
		go s.loop(s.jobs)
	}
	s.pending++
	jobs := s.jobs
	s.wmu.Unlock()
	defer func() {
		s.wmu.Lock()
		s.pending--
		s.wmu.Unlock()
	}()

	j := job{fn: fn, done: make(chan error, 1)}
	select {
	case jobs <- j:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.hub.done:
		return errors.New("server is shutting down")
	}
	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *session) loop(jobs chan job) {
	var e *engine.Engine
	defer func() {
		if e != nil {
			e.Quit()
		}
	}()
	tick := time.NewTicker(s.hub.idleTimeout / 4)
	defer tick.Stop()
	lastJob := time.Now()

	for {
		select {
		case j := <-jobs:
			lastJob = time.Now()
			if e == nil {
				var err error
				if e, err = engine.Start(s.hub.enginePath, 1, 32); err != nil {
					e = nil
					j.done <- fmt.Errorf("start engine: %w", err)
					continue
				}
			}
			err := j.fn(e)
			if err != nil {
				// The engine may be wedged; restart it on the next request.
				e.Quit()
				e = nil
			}
			j.done <- err
		case <-s.hub.done:
			return
		case <-tick.C:
			if time.Since(lastJob) < s.hub.idleTimeout {
				continue
			}
			s.wmu.Lock()
			if s.pending == 0 {
				s.alive = false
				s.wmu.Unlock()
				return
			}
			s.wmu.Unlock()
		}
	}
}

// add stores p unless its FEN is already known.
func (s *session) add(p puzzle.Puzzle) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fens[p.FEN] {
		return false
	}
	p.ID = len(s.puzzles) + 1
	p.Board = ""
	s.puzzles = append(s.puzzles, p)
	s.fens[p.FEN] = true
	if _, ok := s.found[p.MateIn]; ok && s.running {
		s.found[p.MateIn]++
	}
	return true
}

type startConfig struct {
	Mates       []int   `json:"mates"`
	Count       int     `json:"count"`
	ScanDepth   int     `json:"scan_depth"`
	VerifyDepth int     `json:"verify_depth"`
	StrongNodes int     `json:"strong_nodes"`
	Blunder     float64 `json:"blunder"`
	MinPly      int     `json:"min_ply"`
	AllowDuals  bool    `json:"allow_duals"`
	Threads     int     `json:"threads"`
	Hash        int     `json:"hash"`
	Seed        any     `json:"seed"` // number, numeric string, "" or null
}

func defaultStartConfig() startConfig {
	d := puzzle.DefaultOptions()
	return startConfig{
		Mates: d.Mates, Count: 3, ScanDepth: d.ScanDepth, VerifyDepth: d.VerifyDepth,
		StrongNodes: d.StrongNodes, Blunder: d.Blunder, MinPly: d.MinPly, Threads: 1, Hash: 64,
	}
}

func (s *session) start(cfg startConfig) error {
	set := map[int]bool{}
	for _, n := range cfg.Mates {
		if n >= 1 && n <= 5 {
			set[n] = true
		}
	}
	var mates []int
	for n := range set {
		mates = append(mates, n)
	}
	sort.Ints(mates)
	if len(mates) == 0 {
		return errors.New("pick at least one mate length")
	}
	count := max(1, min(500, cfg.Count))

	var seed int64
	switch v := cfg.Seed.(type) {
	case float64:
		seed = int64(v)
	case string:
		if v == "" {
			seed = time.Now().UnixNano()
		} else if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			seed = n
		} else {
			return errors.New("seed must be an integer")
		}
	default:
		seed = time.Now().UnixNano()
	}

	if !s.hub.acquireGenerator() {
		return fmt.Errorf("server is busy: %d generators already running, try again later", s.hub.maxGenerators)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.hub.releaseGenerator()
		return errors.New("generator already running")
	}
	s.target, s.found = map[int]int{}, map[int]int{}
	for _, n := range mates {
		s.target[n], s.found[n] = count, 0
	}
	s.stats = &puzzle.Stats{}
	s.errMsg = ""
	s.started, s.finished = time.Now(), time.Time{}
	s.running = true
	s.stopFlag.Store(false)

	opt := puzzle.Options{
		Mates: mates, ScanDepth: cfg.ScanDepth, VerifyDepth: cfg.VerifyDepth,
		StrongNodes: cfg.StrongNodes, Blunder: cfg.Blunder, MinPly: cfg.MinPly,
		AllowDuals: cfg.AllowDuals,
	}
	threads := max(1, min(s.hub.maxThreads, cfg.Threads))
	hash := max(16, min(256, cfg.Hash))
	go s.generate(opt, threads, hash, rand.New(rand.NewSource(seed)), s.stats)
	return nil
}

func (s *session) wanted(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.found[n]
	return ok && f < s.target[n]
}

func (s *session) generate(opt puzzle.Options, threads, hash int, rng *rand.Rand, stats *puzzle.Stats) {
	defer s.hub.releaseGenerator()
	err := func() error {
		e, err := engine.Start(s.hub.enginePath, threads, hash)
		if err != nil {
			return err
		}
		defer e.Quit()
		stop := func() bool {
			if s.stopFlag.Load() || s.hub.closing.Load() {
				return true
			}
			for _, n := range opt.Mates {
				if s.wanted(n) {
					return false
				}
			}
			return true
		}
		return puzzle.Generate(e, rng, opt, s.wanted, stop, stats, func(p puzzle.Puzzle) { s.add(p) })
	}()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.errMsg = err.Error()
	}
	s.running = false
	s.finished = time.Now()
}

func (s *session) stop() { s.stopFlag.Store(true) }

func (s *session) clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("stop the generator first")
	}
	s.puzzles, s.fens = nil, map[string]bool{}
	return nil
}

func (s *session) snapshot(since int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := 0.0
	if !s.started.IsZero() {
		end := s.finished
		if end.IsZero() {
			end = time.Now()
		}
		elapsed = float64(end.Sub(s.started).Round(100*time.Millisecond)) / float64(time.Second)
	}
	since = max(0, min(since, len(s.puzzles)))
	var errMsg any
	if s.errMsg != "" {
		errMsg = s.errMsg
	}
	return map[string]any{
		"running": s.running,
		"error":   errMsg,
		"stats":   s.stats.Snapshot(),
		"target":  s.target,
		"found":   s.found,
		"elapsed": elapsed,
		"total":   len(s.puzzles),
		"puzzles": append([]puzzle.Puzzle{}, s.puzzles[since:]...),
	}
}

func (s *session) position(ctx context.Context, fen string, moves []string) (map[string]any, error) {
	var res map[string]any
	err := s.do(ctx, func(e *engine.Engine) error {
		cur, err := e.Describe(fen, moves)
		if err != nil {
			return err
		}
		legal, err := e.LegalMoves(fen, moves)
		if err != nil {
			return err
		}
		san := []string{}
		if len(moves) > 0 {
			if san, err = puzzle.SANLine(e, fen, moves); err != nil {
				return err
			}
		}
		res = map[string]any{
			"fen": cur.FEN, "check": cur.InCheck, "legal": legal, "san": san,
			"mate": cur.InCheck && len(legal) == 0,
		}
		return nil
	})
	return res, err
}

func (s *session) evaluate(ctx context.Context, fen string, moves []string, depth int) (*engine.Line, error) {
	var line *engine.Line
	err := s.do(ctx, func(e *engine.Engine) error {
		lines, err := e.Analyse(fen, moves, fmt.Sprintf("depth %d", depth), 1)
		if err == nil && len(lines) > 0 {
			line = &lines[0]
		}
		return err
	})
	return line, err
}
