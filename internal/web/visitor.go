package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

const (
	visitorCookie = "mpl_visitor"
	visitorTTL    = 30 * 24 * time.Hour
)

// visitorID returns the browser's anonymous workspace ID, issuing a new random
// one (in a cookie) on first visit. There is no sign-in: the ID only keeps one
// browser's puzzles and engine separate from another's.
func visitorID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(visitorCookie); err == nil && validID(c.Value) {
		return c.Value
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name: visitorCookie, Value: id, Path: "/",
		MaxAge: int(visitorTTL / time.Second), HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil,
	})
	return id
}

func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
