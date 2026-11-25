package site

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kgunjikar/chess-puzzles/internal/puzzle"
)

func testEntries() []Entry {
	p := puzzle.Puzzle{ID: 1, MateIn: 2, FEN: "6k1/p1p3p1/4p3/7B/3P4/1R3R2/1PP4P/1NB3K1 w - - 1 25",
		ToMove: "white", Solution: []string{"h5g6", "e6e5", "b3b8"}, SAN: []string{"Bg6", "e5", "Rb8#"}}
	q := puzzle.Puzzle{ID: 2, MateIn: 1, FEN: "4kr2/p1R2p2/4p2p/4N1p1/5B2/5BP1/PP2PP1P/3R2K1 w - - 3 24",
		ToMove: "white", Solution: []string{"f3c6"}, SAN: []string{"Bc6#"}}
	pos := func(n int) []Position {
		out := make([]Position, n)
		for i := range out {
			out[i] = Position{FEN: "x", Legal: []string{}}
		}
		return out
	}
	return []Entry{{Puzzle: p, Positions: pos(4)}, {Puzzle: q, Positions: pos(2)}}
}

func build(t *testing.T, cfg Config) string {
	t.Helper()
	eng := t.TempDir()
	for _, f := range EngineFiles {
		if err := os.WriteFile(filepath.Join(eng, f), []byte("stub "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = 120
	}
	if cfg.Title == "" {
		cfg.Title = "Test Lab"
	}
	out := filepath.Join(t.TempDir(), "dist")
	if err := Build(cfg, testEntries(), eng, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildMinimal(t *testing.T) {
	out := build(t, Config{})
	for _, f := range []string{"index.html", "puzzle/1/index.html", "puzzle/2/index.html", "mate-in-1/index.html",
		"mate-in-2/index.html", "about/index.html", "privacy/index.html", "404.html", "puzzles.json", "robots.txt",
		"assets/app.css", "assets/engine/stockfish-19-lite-single.wasm"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	page := read(t, out, "puzzle/1/index.html")
	if strings.Contains(page, "adsbygoogle") {
		t.Error("no ads should be emitted without config")
	}
	for _, f := range []string{"ads.txt", "sitemap.xml"} {
		if _, err := os.Stat(filepath.Join(out, f)); err == nil {
			t.Errorf("%s should not exist without config", f)
		}
	}

	// Embedded puzzle data round-trips through the HTML attribute.
	m := regexp.MustCompile(`data-puzzle="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no puzzle data")
	}
	var data struct {
		ID        int        `json:"id"`
		Solution  []string   `json:"solution"`
		Positions []Position `json:"positions"`
		Next      *int       `json:"next"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &data); err != nil {
		t.Fatal(err)
	}
	if data.ID != 1 || len(data.Positions) != 4 || data.Next != nil {
		t.Fatalf("bad puzzle data: %+v", data)
	}
	if !strings.Contains(page, "25. Bg6 e5 26. Rb8#") {
		t.Error("written solution missing or misnumbered")
	}
	if strings.Count(page, `class="sq `) != 64 {
		t.Error("static board should have 64 squares")
	}

	var idx struct{ Puzzles [][3]int }
	if err := json.Unmarshal([]byte(read(t, out, "puzzles.json")), &idx); err != nil || len(idx.Puzzles) != 2 || idx.Puzzles[0] != [3]int{1, 2, 1} {
		t.Fatalf("bad index: %v %v", idx, err)
	}
}

func TestBuildWithAds(t *testing.T) {
	cfg := Config{BaseURL: "https://puzzles.example.com", Contact: "hi@example.com"}
	cfg.AdSense.Client, cfg.AdSense.SlotSidebar, cfg.AdSense.SlotInline = "ca-pub-1234567890", "111", "222"
	out := build(t, cfg)

	page := read(t, out, "puzzle/1/index.html")
	for _, want := range []string{
		"adsbygoogle.js?client=ca-pub-1234567890", `data-ad-slot="111"`, `data-ad-slot="222"`,
		`<link rel="canonical" href="https://puzzles.example.com/puzzle/1/">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("puzzle page missing %q", want)
		}
	}
	if got := read(t, out, "ads.txt"); got != "google.com, pub-1234567890, DIRECT, f08c47fec0942fa0\n" {
		t.Errorf("ads.txt = %q", got)
	}
	sm := read(t, out, "sitemap.xml")
	if !strings.Contains(sm, "<loc>https://puzzles.example.com/puzzle/2/</loc>") || !strings.Contains(sm, "<loc>https://puzzles.example.com/</loc>") {
		t.Error("sitemap incomplete")
	}
	priv := read(t, out, "privacy/index.html")
	if !strings.Contains(priv, "Google AdSense") || !strings.Contains(priv, "No accounts") {
		t.Error("privacy page should describe ads and say there are no accounts")
	}
	for _, f := range []string{"index.html", "puzzle/1/index.html", "privacy/index.html"} {
		if p := read(t, out, f); strings.Contains(p, "firebase") || strings.Contains(p, "Sign in") {
			t.Errorf("%s should have no sign-in", f)
		}
	}
}

func TestLoadConfigRejectsBadAdSenseID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "site.json")
	_ = os.WriteFile(p, []byte(`{"adsense":{"client":"pub-123"}}`), 0o644)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("expected error for client without ca- prefix")
	}
}
