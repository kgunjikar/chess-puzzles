package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kgunjikar/chess-puzzles/internal/engine"
)

type harness struct {
	t   *testing.T
	app *Server
	url string
}

func newHarness(t *testing.T, enginePath string) *harness {
	app, err := New(Options{EnginePath: enginePath, IdleTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(func() { ts.Close(); app.Close() })
	return &harness{t: t, app: app, url: ts.URL}
}

// visitor returns a client with its own cookie jar, i.e. a separate browser.
func (h *harness) visitor() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (h *harness) call(c *http.Client, method, path, body string) (int, map[string]any) {
	req, _ := http.NewRequest(method, h.url+path, strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func (h *harness) visitorID(c *http.Client) string {
	u, _ := url.Parse(h.url)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == visitorCookie {
			return ck.Value
		}
	}
	return ""
}

func TestOpenAccess(t *testing.T) {
	h := newHarness(t, "stockfish")
	c := h.visitor()

	resp, err := c.Get(h.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Mate Puzzle Lab") || strings.Contains(string(b), "Sign in") {
		t.Fatalf("/ should serve the app with no sign-in (status %d)", resp.StatusCode)
	}
	id := h.visitorID(c)
	if !validID(id) {
		t.Fatalf("no visitor cookie issued: %q", id)
	}
	if code, _ := h.call(c, "GET", "/api/state", ""); code != 200 {
		t.Fatalf("/api/state = %d, want 200", code)
	}
	if h.visitorID(c) != id {
		t.Fatal("visitor ID should be stable across requests")
	}

	// A client without cookies still works; it just gets a fresh workspace.
	if code, _ := h.call(&http.Client{}, "GET", "/api/state", ""); code != 200 {
		t.Fatalf("cookieless /api/state = %d", code)
	}

	// Garbage cookies are replaced, not trusted.
	u, _ := url.Parse(h.url)
	bad := h.visitor()
	bad.Jar.SetCookies(u, []*http.Cookie{{Name: visitorCookie, Value: "../../etc"}})
	h.call(bad, "GET", "/api/state", "")
	if got := h.visitorID(bad); !validID(got) {
		t.Fatalf("invalid cookie kept: %q", got)
	}

	// Non-JSON POSTs are refused (CSRF guard).
	req, _ := http.NewRequest("POST", h.url+"/api/stop", strings.NewReader("x=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form POST = %d, want 415", resp.StatusCode)
	}
}

func TestPerVisitorWorkers(t *testing.T) {
	path := os.Getenv("STOCKFISH")
	if path == "" {
		path = engine.Find()
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("stockfish not available; set $STOCKFISH")
	}
	h := newHarness(t, path)
	alice, bob := h.visitor(), h.visitor()

	fen := "6k1/p1p3p1/4p3/7B/3P4/1R3R2/1PP4P/1NB3K1 w - - 1 25"
	code, pos := h.call(alice, "POST", "/api/position",
		`{"fen":"`+fen+`","moves":["h5g6","e6e5","b3b8"]}`)
	if code != 200 || pos["mate"] != true {
		t.Fatalf("position = %d %v", code, pos)
	}
	if code, ev := h.call(bob, "POST", "/api/evaluate", `{"fen":"`+fen+`","depth":10}`); code != 200 || ev["line"] == nil {
		t.Fatalf("evaluate = %d %v", code, ev)
	}

	h.app.mu.Lock()
	a, b := h.app.users[h.visitorID(alice)], h.app.users[h.visitorID(bob)]
	h.app.mu.Unlock()
	if a == nil || b == nil || a == b {
		t.Fatal("each visitor should have their own session")
	}
	for _, s := range []*session{a, b} {
		s.wmu.Lock()
		alive := s.alive
		s.wmu.Unlock()
		if !alive {
			t.Fatalf("%s has no running worker", s.id)
		}
	}

	// Puzzles are isolated: a generated puzzle for alice is not visible to bob.
	if code, _ := h.call(alice, "POST", "/api/start", `{"mates":[1],"count":1,"verify_depth":14,"seed":"3"}`); code != 200 {
		t.Fatalf("start = %d", code)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, st := h.call(alice, "GET", "/api/state", "")
		if st["running"] == false && st["total"].(float64) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("generator did not finish: %v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, st := h.call(bob, "GET", "/api/state", ""); st["total"].(float64) != 0 {
		t.Fatalf("bob sees alice's puzzles: %v", st)
	}
}
