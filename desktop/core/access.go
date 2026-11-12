package core

import (
	"crypto/subtle"
	"net/http"
	"os"
)

// On a phone every app can reach 127.0.0.1, unlike on a desktop where the
// library already belongs to the signed-in user. The Android app therefore
// starts the server with a one-time secret (NOCTURNE_ACCESS_TOKEN) and gives
// it to its own WebView as a cookie; requests without it are refused.
// Desktop builds do not set the variable, so nothing changes for them.
const accessCookie = "nocturne_access"

func accessToken() string { return os.Getenv("NOCTURNE_ACCESS_TOKEN") }

func (s *Server) allowed(r *http.Request) bool {
	if s.access == "" {
		return true
	}
	c, err := r.Cookie(accessCookie)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.access)) == 1
}
