// Package site renders the static puzzle website: one page per puzzle, list
// pages per mate length, a home page, and the assets that run Stockfish in
// the browser. The output is plain files for any static host.
package site

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kgunjikar/chess-puzzles/internal/puzzle"
)

//go:embed templates/*.html assets/*
var files embed.FS

// Engine files (stockfish.js "lite, single-threaded" build) expected in the engine dir.
var EngineFiles = []string{"stockfish-19-lite-single.js", "stockfish-19-lite-single.wasm", "Copying.txt"}

// Config is the site configuration (site.json).
type Config struct {
	Title     string `json:"title"`
	Tagline   string `json:"tagline"`
	BaseURL   string `json:"base_url"`
	SourceURL string `json:"source_url"`
	Contact   string `json:"contact_email"`
	PageSize  int    `json:"page_size"`
	AdSense   struct {
		Client      string `json:"client"`       // ca-pub-XXXXXXXXXXXXXXXX
		SlotSidebar string `json:"slot_sidebar"` // ad unit IDs
		SlotInline  string `json:"slot_inline"`
	} `json:"adsense"`
}

// LoadConfig reads a site.json file and fills defaults.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Title == "" {
		c.Title = "Mate Puzzle Lab"
	}
	if c.Tagline == "" {
		c.Tagline = "Stockfish-verified mates in 1–5"
	}
	if c.PageSize <= 0 {
		c.PageSize = 120
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.AdSense.Client != "" && !strings.HasPrefix(c.AdSense.Client, "ca-pub-") {
		return c, errors.New("adsense.client must look like ca-pub-XXXXXXXXXXXXXXXX")
	}
	return c, nil
}

// Position is the board state after some plies of the solution.
type Position struct {
	FEN   string   `json:"fen"`
	Check bool     `json:"check"`
	Legal []string `json:"legal"`
}

// Entry is a puzzle with its precomputed main-line positions
// (Positions[k] is the position after k plies; len = len(Solution)+1).
type Entry struct {
	puzzle.Puzzle
	Positions []Position
}

type adSlot struct{ Client, Slot string }

type pageData struct {
	Site         Config
	Title        string
	Description  string
	Canonical    string
	ClientConfig string
	Lengths      []int
	Nav          int
	AdSide       adSlot
	AdInline     adSlot
	Today        string

	// page-specific
	Total    int
	ByLength []lengthInfo
	Latest   []puzzle.Puzzle

	N, Count, PageNum int
	Items             []puzzle.Puzzle
	Pages             []pageLink

	P          puzzle.Puzzle
	PuzzleJSON string
	Board      template.HTML
	MoveText   string
	Prev, Next int
}

type lengthInfo struct{ N, Count int }
type pageLink struct {
	Num     int
	URL     string
	Current bool
}

type builder struct {
	cfg     Config
	out     string
	assets  map[string]string // name -> versioned URL
	tmpl    map[string]*template.Template
	urls    []string
	lengths []int
}

// Build writes the whole site into out (which is replaced).
func Build(cfg Config, entries []Entry, engineDir, out string) error {
	if len(entries) == 0 {
		return errors.New("no puzzles to publish")
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	b := &builder{cfg: cfg, out: out, assets: map[string]string{}, tmpl: map[string]*template.Template{}}
	if err := b.copyAssets(engineDir); err != nil {
		return err
	}
	// The index URL is embedded in every page, so write it before rendering.
	if err := b.index(entries); err != nil {
		return err
	}
	if err := b.parseTemplates(); err != nil {
		return err
	}

	byN := map[int][]puzzle.Puzzle{}
	for _, e := range entries {
		byN[e.MateIn] = append(byN[e.MateIn], e.Puzzle)
	}
	for n := 1; n <= 5; n++ {
		if len(byN[n]) > 0 {
			b.lengths = append(b.lengths, n)
		}
	}

	// Prev/next within the same mate length.
	prev, next := map[int]int{}, map[int]int{}
	for _, list := range byN {
		for i, p := range list {
			if i > 0 {
				prev[p.ID] = list[i-1].ID
			}
			if i+1 < len(list) {
				next[p.ID] = list[i+1].ID
			}
		}
	}

	for _, e := range entries {
		if err := b.puzzlePage(e, prev[e.ID], next[e.ID]); err != nil {
			return fmt.Errorf("puzzle %d: %w", e.ID, err)
		}
	}
	for _, n := range b.lengths {
		if err := b.listPages(n, byN[n]); err != nil {
			return err
		}
	}
	if err := b.homePage(entries, byN); err != nil {
		return err
	}
	for _, p := range []struct{ name, url, title, desc string }{
		{"about", "/about/", "About & licenses", "How the puzzles are made, and the open-source licenses behind them."},
		{"privacy", "/privacy/", "Privacy policy", "What this site stores and how ads work."},
	} {
		d := b.base(p.title+" · "+cfg.Title, p.desc, p.url)
		if err := b.render(p.name, path.Join(p.url, "index.html"), d); err != nil {
			return err
		}
	}
	nf := b.base("Not found · "+cfg.Title, "Page not found.", "")
	if err := b.render("notfound", "404.html", nf); err != nil {
		return err
	}
	return b.meta()
}

func (b *builder) write(rel string, data []byte) error {
	p := filepath.Join(b.out, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func shortHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])[:10]
}

func (b *builder) copyAssets(engineDir string) error {
	entries, err := fs.ReadDir(files, "assets")
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, err := files.ReadFile("assets/" + e.Name())
		if err != nil {
			return err
		}
		if err := b.write("assets/"+e.Name(), data); err != nil {
			return err
		}
		b.assets[e.Name()] = "/assets/" + e.Name() + "?v=" + shortHash(data)
	}
	for _, name := range EngineFiles {
		data, err := os.ReadFile(filepath.Join(engineDir, name))
		if err != nil {
			return fmt.Errorf("engine file missing (run `make engine-wasm`): %w", err)
		}
		if err := b.write("assets/engine/"+name, data); err != nil {
			return err
		}
		b.assets["engine/"+name] = "/assets/engine/" + name + "?v=" + shortHash(data)
	}
	return nil
}

func (b *builder) parseTemplates() error {
	funcs := template.FuncMap{"asset": func(name string) (string, error) {
		u, ok := b.assets[name]
		if !ok {
			return "", fmt.Errorf("unknown asset %q", name)
		}
		return u, nil
	}}
	layout, err := template.New("").Funcs(funcs).ParseFS(files, "templates/layout.html")
	if err != nil {
		return err
	}
	for _, name := range []string{"home", "list", "puzzle", "about", "privacy", "notfound"} {
		t, err := template.Must(layout.Clone()).ParseFS(files, "templates/"+name+".html")
		if err != nil {
			return err
		}
		b.tmpl[name] = t
	}
	return nil
}

func (b *builder) clientConfig() string {
	engine := b.assets["engine/stockfish-19-lite-single.js"]
	wasm := b.assets["engine/stockfish-19-lite-single.wasm"]
	cfg := map[string]any{
		// The engine script reads its .wasm location from the URL hash.
		"engine": engine + "#" + wasm,
		"index":  b.assets["puzzles.json"],
	}
	j, _ := json.Marshal(cfg)
	return string(j)
}

func (b *builder) base(title, desc, url string) *pageData {
	d := &pageData{
		Site: b.cfg, Title: title, Description: desc, ClientConfig: b.clientConfig(),
		Lengths:  b.lengths,
		AdSide:   adSlot{b.cfg.AdSense.Client, b.cfg.AdSense.SlotSidebar},
		AdInline: adSlot{b.cfg.AdSense.Client, b.cfg.AdSense.SlotInline},
		Today:    time.Now().UTC().Format("2 January 2006"),
	}
	if url != "" {
		b.urls = append(b.urls, url)
		if b.cfg.BaseURL != "" {
			d.Canonical = b.cfg.BaseURL + url
		}
	}
	return d
}

func (b *builder) render(name, rel string, d *pageData) error {
	var buf bytes.Buffer
	if err := b.tmpl[name].ExecuteTemplate(&buf, "layout", d); err != nil {
		return err
	}
	return b.write(rel, buf.Bytes())
}

func side(p puzzle.Puzzle) string {
	if p.ToMove == "white" {
		return "White"
	}
	return "Black"
}

// moveText renders the solution in numbered SAN, e.g. "21. Qxc8+ Kf7 22. Rf1+ Kg7 23. Qxf8#".
func moveText(p puzzle.Puzzle) string {
	fields := strings.Fields(p.FEN)
	num := 1
	if len(fields) >= 6 {
		fmt.Sscanf(fields[5], "%d", &num)
	}
	white := p.ToMove == "white"
	var sb strings.Builder
	for i, m := range p.SAN {
		ply := i
		if !white {
			ply++
		}
		if ply%2 == 0 {
			fmt.Fprintf(&sb, "%d. ", num+ply/2)
		} else if i == 0 {
			fmt.Fprintf(&sb, "%d… ", num)
		}
		sb.WriteString(m)
		if i < len(p.SAN)-1 {
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

func (b *builder) puzzlePage(e Entry, prev, next int) error {
	if len(e.Positions) != len(e.Solution)+1 {
		return errors.New("positions do not match solution")
	}
	data := map[string]any{
		"id": e.ID, "mate_in": e.MateIn, "fen": e.FEN, "to_move": e.ToMove,
		"solution": e.Solution, "san": e.SAN, "positions": e.Positions,
		"prev": nullable(prev), "next": nullable(next),
	}
	j, err := json.Marshal(data)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("/puzzle/%d/", e.ID)
	moves := "moves"
	if e.MateIn == 1 {
		moves = "move"
	}
	d := b.base(
		fmt.Sprintf("Mate in %d puzzle #%d — %s to move · %s", e.MateIn, e.ID, side(e.Puzzle), b.cfg.Title),
		fmt.Sprintf("%s to play and checkmate in %d %s. A free chess puzzle checked by Stockfish. Solve it on the board, with hints.",
			side(e.Puzzle), e.MateIn, moves),
		url)
	d.P, d.PuzzleJSON, d.Prev, d.Next, d.Nav = e.Puzzle, string(j), prev, next, e.MateIn
	d.Board = BoardHTML(e.FEN, e.ToMove == "black")
	d.MoveText = moveText(e.Puzzle)
	return b.render("puzzle", path.Join(url, "index.html"), d)
}

func nullable(id int) any {
	if id == 0 {
		return nil
	}
	return id
}

func (b *builder) listPages(n int, list []puzzle.Puzzle) error {
	size := b.cfg.PageSize
	pages := (len(list) + size - 1) / size
	pageURL := func(k int) string {
		if k == 1 {
			return fmt.Sprintf("/mate-in-%d/", n)
		}
		return fmt.Sprintf("/mate-in-%d/page/%d/", n, k)
	}
	for k := 1; k <= pages; k++ {
		lo, hi := (k-1)*size, min(k*size, len(list))
		title := fmt.Sprintf("Mate in %d chess puzzles", n)
		if k > 1 {
			title += fmt.Sprintf(" (page %d)", k)
		}
		d := b.base(title+" · "+b.cfg.Title,
			fmt.Sprintf("%d free mate-in-%d chess puzzles, each checked by Stockfish. Solve them on an interactive board.", len(list), n),
			pageURL(k))
		d.N, d.Count, d.PageNum, d.Items, d.Nav = n, len(list), k, list[lo:hi], n
		for j := 1; j <= pages; j++ {
			d.Pages = append(d.Pages, pageLink{Num: j, URL: pageURL(j), Current: j == k})
		}
		if err := b.render("list", path.Join(pageURL(k), "index.html"), d); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) homePage(entries []Entry, byN map[int][]puzzle.Puzzle) error {
	d := b.base(b.cfg.Title+" — free checkmate puzzles, mate in 1 to 5",
		fmt.Sprintf("%d free checkmate puzzles, from mate in 1 to mate in 5, each with a single forced solution checked by Stockfish. Solve them in your browser.", len(entries)),
		"/")
	d.Total = len(entries)
	for _, n := range b.lengths {
		d.ByLength = append(d.ByLength, lengthInfo{n, len(byN[n])})
	}
	for i := len(entries) - 1; i >= 0 && len(d.Latest) < 12; i-- {
		d.Latest = append(d.Latest, entries[i].Puzzle)
	}
	return b.render("home", "index.html", d)
}

// index writes the compact puzzle list used for random/daily/next: [[id, mate_in, white?], ...].
func (b *builder) index(entries []Entry) error {
	rows := make([][3]int, len(entries))
	for i, e := range entries {
		w := 0
		if e.ToMove == "white" {
			w = 1
		}
		rows[i] = [3]int{e.ID, e.MateIn, w}
	}
	j, err := json.Marshal(map[string]any{"puzzles": rows})
	if err != nil {
		return err
	}
	b.assets["puzzles.json"] = "/puzzles.json?v=" + shortHash(j)
	return b.write("puzzles.json", j)
}

func (b *builder) meta() error {
	robots := "User-agent: *\nAllow: /\n"
	if b.cfg.BaseURL != "" {
		robots += "Sitemap: " + b.cfg.BaseURL + "/sitemap.xml\n"
		sort.Strings(b.urls)
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
		for _, u := range b.urls {
			fmt.Fprintf(&sb, "  <url><loc>%s%s</loc></url>\n", b.cfg.BaseURL, u)
		}
		sb.WriteString("</urlset>\n")
		if err := b.write("sitemap.xml", []byte(sb.String())); err != nil {
			return err
		}
	}
	if err := b.write("robots.txt", []byte(robots)); err != nil {
		return err
	}
	if c := b.cfg.AdSense.Client; c != "" {
		line := fmt.Sprintf("google.com, %s, DIRECT, f08c47fec0942fa0\n", strings.TrimPrefix(c, "ca-"))
		if err := b.write("ads.txt", []byte(line)); err != nil {
			return err
		}
	}
	// Cloudflare Pages / Netlify style headers (Firebase Hosting uses firebase.json instead).
	headers := `/assets/*
  Cache-Control: public, max-age=31536000, immutable
/puzzles.json
  Cache-Control: public, max-age=31536000, immutable
/*
  X-Content-Type-Options: nosniff
  Referrer-Policy: strict-origin-when-cross-origin
`
	return b.write("_headers", []byte(headers))
}
