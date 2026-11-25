// Command mate-puzzles-ui serves a web UI for generating and solving mate-in-N
// puzzles. There is no sign-in: each browser gets an anonymous workspace with a
// private worker goroutine and Stockfish process, and nothing is written to disk.
//
//	mate-puzzles-ui                                  # http://localhost:8765
//	mate-puzzles-ui -load examples/sample_puzzles.jsonl
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/kgunjikar/chess-puzzles/internal/engine"
	"github.com/kgunjikar/chess-puzzles/internal/web"
)

func main() {
	enginePath := flag.String("engine", engine.Find(), "path to stockfish binary")
	host := flag.String("host", "127.0.0.1", "listen address")
	port := flag.Int("port", 8765, "listen port")
	loadPath := flag.String("load", "", "JSONL file of starter puzzles every new visitor gets")
	maxUsers := flag.Int("max-users", 50, "visitor workspaces held in memory at once")
	maxGen := flag.Int("max-generators", 4, "generators running at once across all visitors")
	maxThreads := flag.Int("max-threads", 2, "engine threads one generator may use")
	idle := flag.Duration("idle-timeout", 10*time.Minute, "stop a visitor's board engine after this much inactivity")
	evict := flag.Duration("evict-after", 12*time.Hour, "forget a visitor's puzzles after this much inactivity")
	noBrowser := flag.Bool("no-browser", false, "don't open a browser")
	flag.Parse()

	if st, err := os.Stat(*enginePath); err != nil || st.IsDir() {
		fatal(fmt.Errorf("stockfish binary not found at %s — install Stockfish, set $STOCKFISH, run `make stockfish`, or pass -engine", *enginePath))
	}
	if !isLoopback(*host) {
		fmt.Fprintln(os.Stderr, "note: listening publicly; anyone who can reach this address can run Stockfish here (limits: -max-users, -max-generators, -max-threads)")
	}

	srv, err := web.New(web.Options{
		EnginePath:    *enginePath,
		StarterPath:   *loadPath,
		MaxUsers:      *maxUsers,
		MaxGenerators: *maxGen,
		MaxThreads:    *maxThreads,
		IdleTimeout:   *idle,
		EvictAfter:    *evict,
	})
	if err != nil {
		fatal(err)
	}
	defer srv.Close()
	if n := srv.StarterCount(); n > 0 {
		fmt.Printf("loaded %d starter puzzles from %s\n", n, *loadPath)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err != nil {
		fatal(err)
	}
	url := "http://" + net.JoinHostPort(*host, strconv.Itoa(*port))
	if isLoopback(*host) {
		url = "http://localhost:" + strconv.Itoa(*port)
	}
	fmt.Printf("Mate puzzle UI running at %s  (Ctrl+C to quit)\n", url)
	if !*noBrowser {
		go openBrowser(url)
	}

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		srv.Close()
		shutdown, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = hs.Shutdown(shutdown)
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
