package controller

import "net/http"

// ConsoleFixtureRequest presents the existing fixture session as a browser would.
func ConsoleFixtureRequest(input interface{ fixtureServer() *Server }, r *http.Request, raw string) error {
	s := input.fixtureServer()
	r.Header.Del("Authorization")
	r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	session, err := s.operator.LookupSession(r.Context(), raw)
	if err != nil {
		return err
	}
	r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: session.CSRFToken})
	r.Header.Set(csrfHeaderName, session.CSRFToken)
	return nil
}
