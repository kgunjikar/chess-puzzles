// Package engine drives a UCI chess engine (Stockfish) as a child process.
package engine

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var moveLine = regexp.MustCompile(`^([a-h][1-8][a-h][1-8][qrbn]?): \d+$`)

// Find locates a Stockfish binary: $STOCKFISH, ./bin/stockfish or ./stockfish
// (next to the executable or in the working directory), `stockfish` on PATH, or
// a sibling Stockfish checkout. Falls back to "stockfish".
func Find() string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}

	candidates := []string{os.Getenv("STOCKFISH")}
	for _, d := range dirs {
		candidates = append(candidates, filepath.Join(d, "bin", "stockfish"), filepath.Join(d, "stockfish"))
	}
	if p, err := exec.LookPath("stockfish"); err == nil {
		candidates = append(candidates, p)
	}
	for _, d := range dirs {
		candidates = append(candidates,
			filepath.Join(d, "..", "Stockfish", "src", "stockfish"),
			filepath.Join(d, "..", "src", "stockfish"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return "stockfish"
}

// Line is one principal variation from a search.
type Line struct {
	Kind  string   `json:"kind"` // "mate" or "cp"
	Value int      `json:"value"`
	PV    []string `json:"pv"`
}

// MateIn returns the positive mate distance for the side to move, or 0.
func (l *Line) MateIn() int {
	if l != nil && l.Kind == "mate" && l.Value > 0 {
		return l.Value
	}
	return 0
}

// Position is what the engine's "d" command reports.
type Position struct {
	FEN     string
	InCheck bool
	Board   string
}

// Engine is a running UCI engine. It is not safe for concurrent use.
type Engine struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	out     *bufio.Reader
	multiPV int
}

// Start launches the engine at path and configures threads and hash size.
func Start(path string, threads, hashMB int) (*Engine, error) {
	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", path, err)
	}
	e := &Engine{cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout)}
	if err := e.send("uci"); err != nil {
		return nil, err
	}
	if _, err := e.readUntil(func(l string) bool { return l == "uciok" }); err != nil {
		e.Quit()
		return nil, err
	}
	if err := e.send(fmt.Sprintf("setoption name Threads value %d", threads)); err != nil {
		return nil, err
	}
	if err := e.send(fmt.Sprintf("setoption name Hash value %d", hashMB)); err != nil {
		return nil, err
	}
	if _, err := e.Sync(); err != nil {
		e.Quit()
		return nil, err
	}
	return e, nil
}

func (e *Engine) send(cmd string) error {
	_, err := io.WriteString(e.stdin, cmd+"\n")
	return err
}

func (e *Engine) readUntil(pred func(string) bool) ([]string, error) {
	var lines []string
	for {
		line, err := e.out.ReadString('\n')
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("engine terminated unexpectedly")
			}
			return nil, err
		}
		line = strings.TrimSpace(line)
		lines = append(lines, line)
		if pred(line) {
			return lines, nil
		}
	}
}

// Sync sends isready and returns everything printed before readyok.
func (e *Engine) Sync() ([]string, error) {
	if err := e.send("isready"); err != nil {
		return nil, err
	}
	lines, err := e.readUntil(func(l string) bool { return l == "readyok" })
	if err != nil {
		return nil, err
	}
	return lines[:len(lines)-1], nil
}

func (e *Engine) setMultiPV(n int) error {
	if n == e.multiPV {
		return nil
	}
	e.multiPV = n
	return e.send(fmt.Sprintf("setoption name MultiPV value %d", n))
}

// PosCmd builds a UCI position command. An empty fen means the start position.
func PosCmd(fen string, moves []string) string {
	s := "position startpos"
	if fen != "" {
		s = "position fen " + fen
	}
	if len(moves) > 0 {
		s += " moves " + strings.Join(moves, " ")
	}
	return s
}

func (e *Engine) setPosition(fen string, moves []string) error {
	return e.send(PosCmd(fen, moves))
}

// LegalMoves lists legal moves in UCI notation (via "go perft 1").
func (e *Engine) LegalMoves(fen string, moves []string) ([]string, error) {
	if err := e.setPosition(fen, moves); err != nil {
		return nil, err
	}
	if err := e.send("go perft 1"); err != nil {
		return nil, err
	}
	out, err := e.readUntil(func(l string) bool { return strings.HasPrefix(l, "Nodes searched") })
	if err != nil {
		return nil, err
	}
	legal := []string{}
	for _, l := range out {
		if m := moveLine.FindStringSubmatch(l); m != nil {
			legal = append(legal, m[1])
		}
	}
	return legal, nil
}

// Describe returns the FEN, whether the side to move is in check, and an ASCII board.
func (e *Engine) Describe(fen string, moves []string) (Position, error) {
	if err := e.setPosition(fen, moves); err != nil {
		return Position{}, err
	}
	if err := e.send("d"); err != nil {
		return Position{}, err
	}
	out, err := e.Sync()
	if err != nil {
		return Position{}, err
	}
	var p Position
	var board []string
	for _, l := range out {
		switch {
		case strings.HasPrefix(l, "Fen: "):
			p.FEN = l[len("Fen: "):]
		case strings.HasPrefix(l, "Checkers:"):
			p.InCheck = strings.TrimSpace(l[len("Checkers:"):]) != ""
		case strings.HasPrefix(l, "+"), strings.HasPrefix(l, "|"), strings.HasPrefix(l, "a "):
			board = append(board, l)
		}
	}
	if p.FEN == "" {
		return p, errors.New("engine did not report a FEN (invalid position?)")
	}
	p.Board = strings.Join(board, "\n")
	return p, nil
}

// Analyse searches with the given limit (e.g. "depth 18") and returns the
// final line for each MultiPV index, best first.
func (e *Engine) Analyse(fen string, moves []string, limit string, multiPV int) ([]Line, error) {
	if err := e.setMultiPV(multiPV); err != nil {
		return nil, err
	}
	if err := e.setPosition(fen, moves); err != nil {
		return nil, err
	}
	if err := e.send("go " + limit); err != nil {
		return nil, err
	}
	out, err := e.readUntil(func(l string) bool { return strings.HasPrefix(l, "bestmove") })
	if err != nil {
		return nil, err
	}
	byIdx := map[int]Line{}
	maxIdx := 0
	for _, l := range out {
		if !strings.HasPrefix(l, "info") || !strings.Contains(l, " pv ") || strings.Contains(l, "bound") {
			continue
		}
		t := strings.Fields(l)
		idx := 1
		var line Line
		for i := 0; i < len(t); i++ {
			switch t[i] {
			case "multipv":
				if i+1 < len(t) {
					idx, _ = strconv.Atoi(t[i+1])
				}
			case "score":
				if i+2 < len(t) {
					line.Kind = t[i+1]
					line.Value, _ = strconv.Atoi(t[i+2])
				}
			case "pv":
				line.PV = append([]string(nil), t[i+1:]...)
				i = len(t)
			}
		}
		byIdx[idx] = line
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	lines := make([]Line, 0, len(byIdx))
	for i := 1; i <= maxIdx; i++ {
		if l, ok := byIdx[i]; ok {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// BestMove returns the engine's chosen move, or "" if there is none.
func (e *Engine) BestMove(fen string, moves []string, limit string) (string, error) {
	if err := e.setPosition(fen, moves); err != nil {
		return "", err
	}
	if err := e.send("go " + limit); err != nil {
		return "", err
	}
	out, err := e.readUntil(func(l string) bool { return strings.HasPrefix(l, "bestmove") })
	if err != nil {
		return "", err
	}
	f := strings.Fields(out[len(out)-1])
	if len(f) < 2 || f[1] == "(none)" {
		return "", nil
	}
	return f[1], nil
}

// NewGame resets the engine between games.
func (e *Engine) NewGame() error {
	if err := e.send("ucinewgame"); err != nil {
		return err
	}
	_, err := e.Sync()
	return err
}

// Quit stops the engine process.
func (e *Engine) Quit() {
	_ = e.send("quit")
	done := make(chan struct{})
	go func() { _ = e.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = e.cmd.Process.Kill()
		<-done
	}
}
