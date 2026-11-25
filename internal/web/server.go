// Package web serves the puzzle generator UI and its JSON API. It is open to
// anyone who can reach it: there is no sign-in.
//
// Nothing is written to disk. Each browser gets an anonymous cookie ID, its
// puzzles live in memory, and it gets its own worker goroutine with a private
// Stockfish process (see session).
package web

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kgunjikar/chess-puzzles/internal/engine"
	"github.com/kgunjikar/chess-puzzles/internal/puzzle"
)

//go:embed index.html
var indexHTML []byte

// Options configures a Server.
type Options struct {
	EnginePath string
	// StarterPath optionally names a JSONL file whose puzzles every new visitor starts with.
	StarterPath string
	// MaxUsers caps sessions held in memory at once.
	MaxUsers int
	// MaxGenerators caps generators running at once across all users.
	MaxGenerators int
	// MaxThreads caps the engine threads one generator may use.
	MaxThreads int
	// IdleTimeout stops a user's board engine after this long without requests.
	IdleTimeout time.Duration
	// EvictAfter drops a user's in-memory puzzles after this long without requests.
	EvictAfter time.Duration
}

// Server routes requests to per-user sessions.
type Server struct {
	enginePath    string
	starter       []puzzle.Puzzle
	maxUsers      int
	maxGenerators int
	maxThreads    int
	idleTimeout   time.Duration
	evictAfter    time.Duration

	mu    sync.Mutex
	users map[string]*session

	genSlots chan struct{}
	closing  atomic.Bool
	done     chan struct{}
	once     sync.Once
}

// New validates the configuration, loads starter puzzles, and starts the reaper.
func New(opt Options) (*Server, error) {
	var err error
	s := &Server{
		enginePath:    opt.EnginePath,
		maxUsers:      orDefault(opt.MaxUsers, 50),
		maxGenerators: orDefault(opt.MaxGenerators, 4),
		maxThreads:    orDefault(opt.MaxThreads, 2),
		idleTimeout:   orDefaultDur(opt.IdleTimeout, 10*time.Minute),
		evictAfter:    orDefaultDur(opt.EvictAfter, 12*time.Hour),
		users:         map[string]*session{},
		done:          make(chan struct{}),
	}
	s.genSlots = make(chan struct{}, s.maxGenerators)
	if opt.StarterPath != "" {
		if s.starter, err = loadStarter(opt.EnginePath, opt.StarterPath); err != nil {
			return nil, fmt.Errorf("load %s: %w", opt.StarterPath, err)
		}
	}
	go s.reap()
	return s, nil
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func orDefaultDur(v, d time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return d
}

// StarterCount reports how many starter puzzles were loaded.
func (s *Server) StarterCount() int { return len(s.starter) }

// Close stops all generators and per-user engines.
func (s *Server) Close() {
	s.once.Do(func() {
		s.closing.Store(true)
		close(s.done)
	})
}

func loadStarter(enginePath, path string) ([]puzzle.Puzzle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var e *engine.Engine
	defer func() {
		if e != nil {
			e.Quit()
		}
	}()
	var out []puzzle.Puzzle
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var p puzzle.Puzzle
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			return nil, err
		}
		if len(p.SAN) == 0 {
			if e == nil {
				if e, err = engine.Start(enginePath, 1, 16); err != nil {
					return nil, err
				}
			}
			if p.SAN, err = puzzle.SANLine(e, p.FEN, p.Solution); err != nil {
				return nil, err
			}
		}
		p.ID = 0
		out = append(out, p)
	}
	return out, sc.Err()
}

func (s *Server) acquireGenerator() bool {
	select {
	case s.genSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releaseGenerator() { <-s.genSlots }

var errFull = errors.New("server is busy, try again later")

// session returns (creating if needed) the workspace for visitor id.
func (s *Server) session(id string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.users[id]; ok {
		sess.touch()
		return sess, nil
	}
	if len(s.users) >= s.maxUsers {
		// Make room by evicting the longest-idle user who isn't generating.
		var victim *session
		for _, c := range s.users {
			if !c.generating() && (victim == nil || c.idleFor() > victim.idleFor()) {
				victim = c
			}
		}
		if victim == nil || victim.idleFor() < s.idleTimeout {
			return nil, errFull
		}
		victim.stop()
		delete(s.users, victim.id)
	}
	sess := newSession(s, id)
	s.users[id] = sess
	return sess, nil
}

// reap evicts sessions that have been idle for longer than evictAfter.
func (s *Server) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		s.mu.Lock()
		for id, sess := range s.users {
			if sess.idleFor() > s.evictAfter && !sess.generating() {
				delete(s.users, id)
			}
		}
		s.mu.Unlock()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// withSession resolves the visitor's session for API calls. POSTs must be
// JSON, which (with SameSite=Lax cookies) blocks cross-site form posts.
func (s *Server) withSession(fn func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			jsonError(w, http.StatusUnsupportedMediaType, errors.New("content type must be application/json"))
			return
		}
		sess, err := s.session(visitorID(w, r))
		if err != nil {
			jsonError(w, http.StatusServiceUnavailable, err)
			return
		}
		fn(w, r, sess)
	}
}

type posRequest struct {
	FEN   string   `json:"fen"`
	Moves []string `json:"moves"`
	Depth int      `json:"depth"`
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		visitorID(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(indexHTML)
	})

	mux.HandleFunc("GET /api/state", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		since, _ := strconv.Atoi(r.URL.Query().Get("since"))
		writeJSON(w, http.StatusOK, sess.snapshot(since))
	}))

	mux.HandleFunc("POST /api/start", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		cfg := defaultStartConfig()
		if err := decode(r, &cfg); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		if err := sess.start(cfg); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /api/stop", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		sess.stop()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /api/clear", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		if err := sess.clear(); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /api/position", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		var req posRequest
		if err := decode(r, &req); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		if req.FEN == "" {
			jsonError(w, http.StatusBadRequest, errors.New("fen is required"))
			return
		}
		res, err := sess.position(r.Context(), req.FEN, req.Moves)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}))

	mux.HandleFunc("POST /api/evaluate", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		req := posRequest{Depth: 18}
		if err := decode(r, &req); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		if req.FEN == "" {
			jsonError(w, http.StatusBadRequest, errors.New("fen is required"))
			return
		}
		line, err := sess.evaluate(r.Context(), req.FEN, req.Moves, max(4, min(30, req.Depth)))
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"line": line})
	}))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		jsonError(w, http.StatusNotFound, errors.New("not found"))
	})
	return securityHeaders(mux)
}
