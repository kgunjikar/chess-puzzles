// Package puzzle finds and verifies mate-in-N puzzles using a UCI engine.
//
// How it works
//  1. Plays lopsided games: a "strong" side (engine, small node budget) against a
//     "weak" side that blunders randomly some of the time. This quickly produces
//     positions with forced mates.
//  2. Every position is scanned with a shallow search. If the side to move has a
//     forced mate in 1..5, the position becomes a candidate.
//  3. Candidates are verified with deeper searches, building the solution move by
//     move:
//     - the attacker's move at every step must be the only move that mates in
//     the shortest number of moves (no "duals"),
//     - the defender always plays the longest resistance,
//     - the final position must be checkmate.
package puzzle

import (
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"

	"github.com/kgunjikar/chess-puzzles/internal/engine"
)

// Puzzle is one verified mate-in-N puzzle.
type Puzzle struct {
	ID       int      `json:"id,omitempty"`
	MateIn   int      `json:"mate_in"`
	FEN      string   `json:"fen"`
	ToMove   string   `json:"to_move"`
	Solution []string `json:"solution"`
	SAN      []string `json:"san"`
	Board    string   `json:"-"`
}

// Options control generation. Use DefaultOptions and override fields.
type Options struct {
	Mates       []int
	ScanDepth   int
	VerifyDepth int
	StrongNodes int
	Blunder     float64
	MinPly      int
	AllowDuals  bool
}

// DefaultOptions returns the standard generator settings.
func DefaultOptions() Options {
	return Options{
		Mates:       []int{1, 2, 3, 4, 5},
		ScanDepth:   10,
		VerifyDepth: 22,
		StrongNodes: 3000,
		Blunder:     0.35,
		MinPly:      16,
	}
}

// Stats counts generator work. Fields are safe to read concurrently.
type Stats struct {
	Games, Positions, Candidates, Rejected atomic.Int64
}

// Snapshot returns the counters as a JSON-friendly map.
func (s *Stats) Snapshot() map[string]int64 {
	return map[string]int64{
		"games":      s.Games.Load(),
		"positions":  s.Positions.Load(),
		"candidates": s.Candidates.Load(),
		"rejected":   s.Rejected.Load(),
	}
}

func first(lines []engine.Line) *engine.Line {
	if len(lines) == 0 {
		return nil
	}
	return &lines[0]
}

// Verify builds and checks the full solution from fen. It returns the mate
// length and the solution, or n == 0 if the position is not a valid puzzle.
func Verify(e *engine.Engine, fen string, depth int, allowDuals bool) (int, []string, error) {
	limit := fmt.Sprintf("depth %d", depth)
	lines, err := e.Analyse(fen, nil, limit, 2)
	if err != nil {
		return 0, nil, err
	}
	n := first(lines).MateIn()
	if n == 0 {
		return 0, nil, nil
	}

	var sol []string
	for remaining := n; remaining > 0; remaining-- {
		if len(sol) > 0 {
			if lines, err = e.Analyse(fen, sol, limit, 2); err != nil {
				return 0, nil, err
			}
		}
		if first(lines).MateIn() != remaining || len(lines[0].PV) == 0 {
			return 0, nil, nil // engine line inconsistent; reject
		}
		// Another attacker move that also mates in <= remaining -> not unique.
		if len(lines) > 1 {
			if m2 := lines[1].MateIn(); m2 != 0 && m2 <= remaining && (len(sol) == 0 || !allowDuals) {
				return 0, nil, nil
			}
		}
		sol = append(sol, lines[0].PV[0])
		if remaining == 1 {
			break
		}
		// Defender: longest resistance.
		reply, err := e.Analyse(fen, sol, limit, 1)
		if err != nil {
			return 0, nil, err
		}
		if len(reply) == 0 || len(reply[0].PV) == 0 {
			return 0, nil, nil
		}
		sol = append(sol, reply[0].PV[0])
	}

	// Final position must be checkmate.
	legal, err := e.LegalMoves(fen, sol)
	if err != nil || len(legal) > 0 {
		return 0, nil, err
	}
	pos, err := e.Describe(fen, sol)
	if err != nil || !pos.InCheck {
		return 0, nil, err
	}
	return n, sol, nil
}

// BoardFromFEN maps squares ("e4") to piece letters from a FEN's placement field.
func BoardFromFEN(fen string) map[string]byte {
	board := map[string]byte{}
	placement, _, _ := strings.Cut(fen, " ")
	for r, row := range strings.Split(placement, "/") {
		f := 0
		for i := 0; i < len(row); i++ {
			ch := row[i]
			if ch >= '0' && ch <= '9' {
				f += int(ch - '0')
				continue
			}
			if f < 8 {
				board[string(rune('a'+f))+string(rune('8'-r))] = ch
			}
			f++
		}
	}
	return board
}

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 'a' + 'A'
	}
	return b
}

// SANLine converts a UCI move list (from fen) to SAN, using the engine for
// legality and check detection.
func SANLine(e *engine.Engine, fen string, moves []string) ([]string, error) {
	out := make([]string, 0, len(moves))
	for i, mv := range moves {
		if len(mv) < 4 {
			return nil, fmt.Errorf("bad move %q", mv)
		}
		cur, err := e.Describe(fen, moves[:i])
		if err != nil {
			return nil, err
		}
		board := BoardFromFEN(cur.FEN)
		legal, err := e.LegalMoves(fen, moves[:i])
		if err != nil {
			return nil, err
		}
		frm, to, promo := mv[:2], mv[2:4], mv[4:]
		piece, ok := board[frm]
		if !ok {
			piece = '?'
		}
		kind := upper(piece)
		var san string
		switch {
		case kind == 'K' && (int(frm[0])-int(to[0]) == 2 || int(to[0])-int(frm[0]) == 2):
			san = "O-O"
			if to[0] != 'g' {
				san = "O-O-O"
			}
		case kind == 'P':
			if frm[0] != to[0] {
				san = frm[:1] + "x"
			}
			san += to
			if promo != "" {
				san += "=" + strings.ToUpper(promo)
			}
		default:
			var others []string
			for _, m := range legal {
				if m[2:4] == to && m[:2] != frm && board[m[:2]] == piece {
					others = append(others, m[:2])
				}
			}
			dis := ""
			if len(others) > 0 {
				fileUnique, rankUnique := true, true
				for _, o := range others {
					fileUnique = fileUnique && o[0] != frm[0]
					rankUnique = rankUnique && o[1] != frm[1]
				}
				switch {
				case fileUnique:
					dis = frm[:1]
				case rankUnique:
					dis = frm[1:2]
				default:
					dis = frm
				}
			}
			san = string(kind) + dis
			if _, capture := board[to]; capture {
				san += "x"
			}
			san += to
		}
		after, err := e.Describe(fen, moves[:i+1])
		if err != nil {
			return nil, err
		}
		if after.InCheck {
			next, err := e.LegalMoves(fen, moves[:i+1])
			if err != nil {
				return nil, err
			}
			if len(next) > 0 {
				san += "+"
			} else {
				san += "#"
			}
		}
		out = append(out, san)
	}
	return out, nil
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Generate plays games and calls emit for each verified puzzle until stop()
// returns true. wanted(n) reports whether a mate-in-n puzzle is still needed.
func Generate(e *engine.Engine, rng *rand.Rand, opt Options, wanted func(int) bool,
	stop func() bool, stats *Stats, emit func(Puzzle)) error {
	if stats == nil {
		stats = &Stats{}
	}
	maxN := 0
	for _, n := range opt.Mates {
		maxN = max(maxN, n)
	}
	seen := map[string]bool{}
	scanLimit := fmt.Sprintf("depth %d", opt.ScanDepth)
	strongLimit := fmt.Sprintf("nodes %d", opt.StrongNodes)

	for !stop() {
		stats.Games.Add(1)
		if err := e.NewGame(); err != nil {
			return err
		}
		var moves []string
		strong := "wb"[rng.Intn(2)]
		// A few random opening moves for variety.
		for k := 2 + rng.Intn(5); k > 0; k-- {
			legal, err := e.LegalMoves("", moves)
			if err != nil {
				return err
			}
			if len(legal) == 0 {
				break
			}
			moves = append(moves, legal[rng.Intn(len(legal))])
		}

		for len(moves) < 300 && !stop() {
			legal, err := e.LegalMoves("", moves)
			if err != nil {
				return err
			}
			if len(legal) == 0 {
				break
			}
			stm := byte('w')
			if len(moves)%2 == 1 {
				stm = 'b'
			}

			if len(moves) >= opt.MinPly {
				stats.Positions.Add(1)
				scan, err := e.Analyse("", moves, scanLimit, 1)
				if err != nil {
					return err
				}
				if n := first(scan).MateIn(); n != 0 && n <= maxN {
					pos, err := e.Describe("", moves)
					if err != nil {
						return err
					}
					key := strings.Join(strings.Fields(pos.FEN)[:4], " ")
					if !seen[key] && wanted(n) {
						seen[key] = true
						stats.Candidates.Add(1)
						vn, sol, err := Verify(e, pos.FEN, opt.VerifyDepth, opt.AllowDuals)
						if err != nil {
							return err
						}
						if vn != 0 && contains(opt.Mates, vn) && wanted(vn) {
							san, err := SANLine(e, pos.FEN, sol)
							if err != nil {
								return err
							}
							toMove := "white"
							if stm == 'b' {
								toMove = "black"
							}
							emit(Puzzle{MateIn: vn, FEN: pos.FEN, ToMove: toMove,
								Solution: sol, SAN: san, Board: pos.Board})
						} else {
							stats.Rejected.Add(1)
						}
					}
					// Game is effectively decided; start a fresh one.
					break
				}
			}

			var mv string
			switch {
			case stm == strong:
				mv, err = e.BestMove("", moves, strongLimit)
			case rng.Float64() < opt.Blunder:
				mv = legal[rng.Intn(len(legal))]
			default:
				mv, err = e.BestMove("", moves, "depth 2")
			}
			if err != nil {
				return err
			}
			if mv == "" {
				break
			}
			moves = append(moves, mv)
		}
	}
	return nil
}

// ParseRange parses mate lengths like "1-5" or "2,4" into a sorted list within 1..5.
func ParseRange(s string) ([]int, error) {
	set := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		a, b, found := strings.Cut(strings.TrimSpace(part), "-")
		var lo, hi int
		if _, err := fmt.Sscanf(a, "%d", &lo); err != nil {
			return nil, fmt.Errorf("bad mate range %q", s)
		}
		hi = lo
		if found {
			if _, err := fmt.Sscanf(b, "%d", &hi); err != nil {
				return nil, fmt.Errorf("bad mate range %q", s)
			}
		}
		for n := lo; n <= hi; n++ {
			set[n] = true
		}
	}
	var out []int
	for n := 1; n <= 5; n++ {
		if set[n] {
			out = append(out, n)
			delete(set, n)
		}
	}
	if len(out) == 0 || len(set) > 0 {
		return nil, fmt.Errorf("mate depths must be within 1..5")
	}
	return out, nil
}
