package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func githubTestEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"BENCH_API_KEY":              "worker-test-key",
		"BENCH_GITHUB_CLIENT_ID":     "test-client-id",
		"BENCH_GITHUB_CLIENT_SECRET": "test-client-secret",
		"BENCH_GITHUB_ALLOWED_USERS": "Alice, bOB",
		"BENCH_GITHUB_CALLBACK_URL":  "https://bench.example/api/auth/github/callback",
		"BENCH_SESSION_SECRET":       "test-session-signing-secret-at-least-32-bytes",
	} {
		t.Setenv(name, value)
	}
}

func githubTestServer(t *testing.T) *Server {
	t.Helper()
	githubTestEnv(t)
	auth, err := loadGitHubAuth("worker-test-key")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{apiKey: "worker-test-key", githubAuth: auth}
}

func authRequest(handler http.HandlerFunc, method, path, origin string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func responseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q missing in %v", name, w.Header())
	return nil
}

func testSessionCookie(t *testing.T, s *Server, login string, expires int64) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	s.githubAuth.setCookie(w, "session", browserSession{Login: login, Expires: expires}, sessionLifetime)
	return responseCookie(t, w, s.githubAuth.cookieName("session"))
}

func beginGitHubSignIn(t *testing.T, s *Server, returnTo string) (*http.Cookie, string) {
	t.Helper()
	w := authRequest(s.handleGitHubLogin, http.MethodGet, "/api/auth/github?return_to="+url.QueryEscape(returnTo), "")
	if w.Code != http.StatusFound {
		t.Fatalf("sign-in: %d %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.Path != "/login/oauth/authorize" {
		t.Fatalf("unexpected provider redirect: %v %v", u, err)
	}
	query := u.Query()
	if query.Get("client_id") != s.githubAuth.clientID || query.Get("redirect_uri") != s.githubAuth.callbackURL || len(query.Get("state")) < 20 {
		t.Fatalf("unexpected authorize query: %v", query)
	}
	return responseCookie(t, w, s.githubAuth.cookieName("oauth_state")), query.Get("state")
}

type githubTestTransport func(*http.Request) (*http.Response, error)

func (f githubTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockGitHub(t *testing.T, s *Server, handler http.HandlerFunc) {
	t.Helper()
	provider := httptest.NewTLSServer(handler)
	t.Cleanup(provider.Close)
	u, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.githubAuth.client.Transport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://github.com/login/oauth/access_token" && r.URL.String() != "https://api.github.com/user" {
			t.Errorf("unexpected provider URL: %s", r.URL)
			return nil, fmt.Errorf("unexpected provider URL")
		}
		clone := r.Clone(r.Context())
		clone.Host = r.URL.Host
		clone.URL.Scheme, clone.URL.Host = u.Scheme, u.Host
		return provider.Client().Transport.RoundTrip(clone)
	})
}

func TestGitHubAuthConfiguration(t *testing.T) {
	tests := []struct {
		name, env, value, wantError string
	}{
		{"complete", "", "", ""},
		{"missing client ID", "BENCH_GITHUB_CLIENT_ID", "", "BENCH_GITHUB_CLIENT_ID"},
		{"missing client secret", "BENCH_GITHUB_CLIENT_SECRET", "", "BENCH_GITHUB_CLIENT_SECRET"},
		{"missing allowlist", "BENCH_GITHUB_ALLOWED_USERS", "", "BENCH_GITHUB_ALLOWED_USERS"},
		{"empty allowlist", "BENCH_GITHUB_ALLOWED_USERS", ",", "BENCH_GITHUB_ALLOWED_USERS"},
		{"invalid login", "BENCH_GITHUB_ALLOWED_USERS", "alice,*", "BENCH_GITHUB_ALLOWED_USERS"},
		{"missing callback", "BENCH_GITHUB_CALLBACK_URL", "", "BENCH_GITHUB_CALLBACK_URL"},
		{"missing signing secret", "BENCH_SESSION_SECRET", "", "BENCH_SESSION_SECRET"},
		{"short signing secret", "BENCH_SESSION_SECRET", "short", "BENCH_SESSION_SECRET"},
		{"missing worker key", "BENCH_API_KEY", "", "BENCH_API_KEY"},
		{"blank worker key", "BENCH_API_KEY", "  ", "BENCH_API_KEY"},
		{"relative callback", "BENCH_GITHUB_CALLBACK_URL", githubCallbackPath, "BENCH_GITHUB_CALLBACK_URL"},
		{"insecure callback", "BENCH_GITHUB_CALLBACK_URL", "http://bench.example" + githubCallbackPath, "BENCH_GITHUB_CALLBACK_URL"},
		{"wrong callback path", "BENCH_GITHUB_CALLBACK_URL", "https://bench.example/callback", "BENCH_GITHUB_CALLBACK_URL"},
		{"callback credentials", "BENCH_GITHUB_CALLBACK_URL", "https://secret@bench.example" + githubCallbackPath, "BENCH_GITHUB_CALLBACK_URL"},
		{"callback query", "BENCH_GITHUB_CALLBACK_URL", "https://bench.example" + githubCallbackPath + "?a=1", "BENCH_GITHUB_CALLBACK_URL"},
		{"callback fragment", "BENCH_GITHUB_CALLBACK_URL", "https://bench.example" + githubCallbackPath + "#fragment", "BENCH_GITHUB_CALLBACK_URL"},
		{"callback malformed", "BENCH_GITHUB_CALLBACK_URL", ":%", "BENCH_GITHUB_CALLBACK_URL"},
		{"localhost", "BENCH_GITHUB_CALLBACK_URL", "http://localhost:3000" + githubCallbackPath, ""},
		{"loopback", "BENCH_GITHUB_CALLBACK_URL", "http://127.0.0.1:3000" + githubCallbackPath, ""},
		{"loopback IPv6", "BENCH_GITHUB_CALLBACK_URL", "http://[::1]:3000" + githubCallbackPath, ""},
		{"fake localhost", "BENCH_GITHUB_CALLBACK_URL", "http://localhost.evil.example" + githubCallbackPath, "BENCH_GITHUB_CALLBACK_URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			githubTestEnv(t)
			if test.env != "" {
				t.Setenv(test.env, test.value)
			}
			t.Setenv("SVG_CACHE_DIR", t.TempDir())
			s, err := NewServer(nil, ":0")
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("want error naming %s, got %v", test.wantError, err)
				}
				return
			}
			if err != nil || s.githubAuth == nil || !s.githubAuth.allowedUsers["alice"] || !s.githubAuth.allowedUsers["bob"] {
				t.Fatalf("configuration failed: %v", err)
			}
			if s.githubAuth.client.Timeout <= 0 || s.githubAuth.client.CheckRedirect == nil {
				t.Fatal("provider requests must have a timeout and reject redirects")
			}
			cookie, _ := beginGitHubSignIn(t, s, "/")
			if cookie.Secure != strings.HasPrefix(s.githubAuth.callbackURL, "https:") || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("unexpected cookie attributes: %v", cookie)
			}
		})
	}
}

func TestAuthUnconfigured(t *testing.T) {
	for _, key := range []string{"", "worker-test-key"} {
		t.Run("key="+key, func(t *testing.T) {
			for _, name := range []string{"BENCH_GITHUB_CLIENT_ID", "BENCH_GITHUB_CLIENT_SECRET", "BENCH_GITHUB_ALLOWED_USERS", "BENCH_GITHUB_CALLBACK_URL", "BENCH_SESSION_SECRET"} {
				t.Setenv(name, "")
			}
			t.Setenv("BENCH_API_KEY", key)
			t.Setenv("SVG_CACHE_DIR", t.TempDir())
			s, err := NewServer(nil, ":0")
			if err != nil || s.githubAuth != nil {
				t.Fatalf("unconfigured OAuth should start: %v", err)
			}
			w := authRequest(s.handleAuthSession, http.MethodGet, "/api/auth/session", "")
			var session struct {
				Configured             bool `json:"configured"`
				Authenticated          bool `json:"authenticated"`
				AuthenticationRequired bool `json:"authentication_required"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.Configured || session.Authenticated || session.AuthenticationRequired != (key != "") {
				t.Fatalf("unexpected session: %s (%v)", w.Body.String(), err)
			}
			if w := authRequest(s.handleGitHubLogin, http.MethodGet, "/api/auth/github", ""); w.Code != http.StatusServiceUnavailable {
				t.Fatalf("unconfigured login: %d", w.Code)
			}
			next := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
			for _, handler := range []http.HandlerFunc{s.requireAuth(next), s.requireInvestigationAuth(next)} {
				want := http.StatusNoContent
				if key != "" {
					want = http.StatusUnauthorized
				}
				if w := authRequest(handler, http.MethodPost, "/write", ""); w.Code != want {
					t.Fatalf("write status: %d, want %d", w.Code, want)
				}
			}
		})
	}
}

func TestGitHubSignInAndLogout(t *testing.T) {
	s := githubTestServer(t)
	var calls atomic.Int32
	mockGitHub(t, s, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Method != http.MethodPost || r.Host != "github.com" || r.Form.Get("client_id") != "test-client-id" ||
				r.Form.Get("client_secret") != "test-client-secret" || r.Form.Get("code") != "provider-code" ||
				r.Form.Get("redirect_uri") != s.githubAuth.callbackURL || r.Header.Get("Authorization") != "" {
				t.Errorf("unexpected token exchange: %s %s %v", r.Method, r.Host, r.Form)
			}
			_, _ = io.WriteString(w, `{"access_token":"provider-token-secret","token_type":"bearer"}`)
		case "/user":
			if r.Method != http.MethodGet || r.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer provider-token-secret" || r.URL.RawQuery != "" {
				t.Error("unexpected user request")
			}
			_, _ = io.WriteString(w, `{"login":"aLiCe"}`)
		default:
			t.Errorf("unexpected provider path: %s", r.URL.Path)
		}
	})
	returnTo := "/investigations/42?tab=attempts#candidate"
	stateCookie, state := beginGitHubSignIn(t, s, returnTo)
	if !stateCookie.Secure || !stateCookie.HttpOnly || stateCookie.SameSite != http.SameSiteLaxMode || stateCookie.MaxAge != 600 || stateCookie.Path != "/" || stateCookie.Domain != "" {
		t.Fatalf("unsafe state cookie: %v", stateCookie)
	}
	w := authRequest(s.handleGitHubCallback, http.MethodGet, githubCallbackPath+"?code=provider-code&state="+state, "", stateCookie)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != returnTo || calls.Load() != 2 {
		t.Fatalf("callback: %d %v %s, calls=%d", w.Code, w.Header(), w.Body.String(), calls.Load())
	}
	if responseCookie(t, w, stateCookie.Name).MaxAge != -1 {
		t.Fatal("callback did not clear state")
	}
	sessionCookie := responseCookie(t, w, s.githubAuth.cookieName("session"))
	if !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.MaxAge != 28800 || sessionCookie.Domain != "" || sessionCookie.Path != "/" {
		t.Fatalf("unsafe session cookie: %v", sessionCookie)
	}
	payload, _, _ := strings.Cut(sessionCookie.Value, ".")
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil || len(stored) != 2 || stored["login"] != "aLiCe" || stored["expires"].(float64) <= float64(time.Now().Unix()) {
		t.Fatalf("session must contain only login and expiry: %s (%v)", data, err)
	}
	for _, secret := range []string{"provider-token-secret", "test-client-secret", "provider-code"} {
		if strings.Contains(w.Body.String()+fmt.Sprint(w.Header())+string(data), secret) {
			t.Fatal("provider credential leaked to browser")
		}
	}
	w = authRequest(s.handleAuthSession, http.MethodGet, "/api/auth/session", "", sessionCookie)
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"authenticated":true`) || !strings.Contains(w.Body.String(), `"login":"aLiCe"`) {
		t.Fatalf("session response: %s", w.Body.String())
	}
	next := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	if w := authRequest(s.requireAuth(next), http.MethodPost, "/worker", s.githubAuth.origin, sessionCookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("browser session authorized worker endpoint: %d", w.Code)
	}
	if w := authRequest(s.handleAuthLogout, http.MethodPost, "/api/auth/logout", "https://evil.example", sessionCookie); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Fatalf("cross-origin logout allowed: %d", w.Code)
	}
	w = authRequest(s.handleAuthLogout, http.MethodPost, "/api/auth/logout", s.githubAuth.origin, sessionCookie)
	if w.Code != http.StatusNoContent || responseCookie(t, w, stateCookie.Name).MaxAge != -1 {
		t.Fatalf("logout failed: %d", w.Code)
	}
	cleared := responseCookie(t, w, sessionCookie.Name)
	if cleared.MaxAge != -1 || !cleared.Expires.Before(time.Now()) || cleared.Value != "" {
		t.Fatalf("session not cleared: %v", cleared)
	}
	if w := authRequest(s.requireInvestigationAuth(next), http.MethodPost, "/write", s.githubAuth.origin, cleared); w.Code != http.StatusUnauthorized {
		t.Fatalf("cleared session authorized write: %d", w.Code)
	}
}

func TestGitHubCallbackRejectsInvalidState(t *testing.T) {
	s := githubTestServer(t)
	s.githubAuth.client.Transport = githubTestTransport(func(_ *http.Request) (*http.Response, error) {
		t.Error("invalid state reached provider")
		return nil, fmt.Errorf("provider should not be called")
	})
	cookie, state := beginGitHubSignIn(t, s, "/")
	otherCookie, _ := beginGitHubSignIn(t, s, "/")
	expiredResponse := httptest.NewRecorder()
	s.githubAuth.setCookie(expiredResponse, "oauth_state", oauthState{Nonce: state, ReturnTo: "/", Expires: time.Now().Add(-time.Second).Unix()}, oauthStateLifetime)
	expired := responseCookie(t, expiredResponse, cookie.Name)
	tampered := *cookie
	tampered.Value = "x" + tampered.Value
	for _, test := range []struct {
		name, query string
		cookie      *http.Cookie
	}{
		{"missing cookie", "state=" + state + "&code=provider-code", nil},
		{"different browser", "state=" + state + "&code=provider-code", otherCookie},
		{"tampered cookie", "state=" + state + "&code=provider-code", &tampered},
		{"expired", "state=" + state + "&code=provider-code", expired},
		{"missing state", "code=provider-code", cookie},
		{"wrong state", "state=wrong&code=provider-code", cookie},
		{"duplicate state", "state=" + state + "&state=" + state + "&code=provider-code", cookie},
		{"no code", "state=" + state, cookie},
		{"duplicate code", "state=" + state + "&code=one&code=two", cookie},
		{"provider denied", "state=" + state + "&error=denied&error_description=provider-secret", cookie},
	} {
		t.Run(test.name, func(t *testing.T) {
			var cookies []*http.Cookie
			if test.cookie != nil {
				cookies = append(cookies, test.cookie)
			}
			w := authRequest(s.handleGitHubCallback, http.MethodGet, githubCallbackPath+"?"+test.query, "", cookies...)
			if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "provider-secret") || responseCookie(t, w, cookie.Name).MaxAge != -1 {
				t.Fatalf("invalid state response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGitHubProviderFailures(t *testing.T) {
	for _, test := range []struct {
		name, tokenBody, userBody string
		tokenStatus, userStatus   int
		want                      int
	}{
		{"not allowed", `{"access_token":"provider-secret","token_type":"bearer"}`, `{"login":"mallory"}`, 200, 200, 403},
		{"token rejected", `provider-secret`, "", 401, 200, 502},
		{"token error", `{"error":"provider-secret","error_description":"client-secret"}`, "", 200, 200, 502},
		{"token malformed", `{provider-secret`, "", 200, 200, 502},
		{"token oversized", strings.Repeat("x", githubResponseMax+1), "", 200, 200, 502},
		{"token redirect", "", "", 302, 200, 502},
		{"user rejected", `{"access_token":"provider-secret","token_type":"bearer"}`, "provider-secret", 200, 403, 502},
		{"user malformed", `{"access_token":"provider-secret","token_type":"bearer"}`, `{"login":`, 200, 200, 502},
		{"user empty", `{"access_token":"provider-secret","token_type":"bearer"}`, `{}`, 200, 200, 502},
		{"user oversized", `{"access_token":"provider-secret","token_type":"bearer"}`, strings.Repeat("x", githubResponseMax+1), 200, 200, 502},
		{"user redirect", `{"access_token":"provider-secret","token_type":"bearer"}`, "", 200, 302, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := githubTestServer(t)
			mockGitHub(t, s, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://evil.example/steal")
				if r.URL.Path == "/login/oauth/access_token" {
					w.WriteHeader(test.tokenStatus)
					_, _ = io.WriteString(w, test.tokenBody)
				} else {
					w.WriteHeader(test.userStatus)
					_, _ = io.WriteString(w, test.userBody)
				}
			})
			cookie, state := beginGitHubSignIn(t, s, "/")
			w := authRequest(s.handleGitHubCallback, http.MethodGet, githubCallbackPath+"?code=provider-code&state="+state, "", cookie)
			if w.Code != test.want || w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 1 {
				t.Fatalf("provider failure: %d %s %v", w.Code, w.Body.String(), w.Header())
			}
			for _, secret := range []string{"provider-secret", "client-secret", "provider-code"} {
				if strings.Contains(w.Body.String()+fmt.Sprint(w.Header()), secret) {
					t.Fatal("provider credential leaked on error")
				}
			}
		})
	}
}

func TestInvestigationSessionAuth(t *testing.T) {
	s := githubTestServer(t)
	next := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	valid := testSessionCookie(t, s, "ALICE", time.Now().Add(time.Hour).Unix())
	expired := testSessionCookie(t, s, "alice", time.Now().Unix())
	notAllowed := testSessionCookie(t, s, "mallory", time.Now().Add(time.Hour).Unix())
	tampered := *valid
	tampered.Value = "x" + tampered.Value
	wrongKind := httptest.NewRecorder()
	s.githubAuth.setCookie(wrongKind, "oauth_state", browserSession{Login: "alice", Expires: time.Now().Add(time.Hour).Unix()}, sessionLifetime)
	stateAsSession := responseCookie(t, wrongKind, s.githubAuth.cookieName("oauth_state"))
	stateAsSession.Name = valid.Name
	for _, test := range []struct {
		name, origin, bearer string
		cookie               *http.Cookie
		want                 int
	}{
		{"anonymous", s.githubAuth.origin, "", nil, 401},
		{"valid session", s.githubAuth.origin, "", valid, 204},
		{"expired", s.githubAuth.origin, "", expired, 401},
		{"tampered", s.githubAuth.origin, "", &tampered, 401},
		{"not allowed", s.githubAuth.origin, "", notAllowed, 401},
		{"wrong purpose", s.githubAuth.origin, "", stateAsSession, 401},
		{"missing origin", "", "", valid, 403},
		{"null origin", "null", "", valid, 403},
		{"cross origin", "https://evil.example", "", valid, 403},
		{"wrong port", "https://bench.example:444", "", valid, 403},
		{"wrong scheme", "http://bench.example", "", valid, 403},
		{"valid bearer", "", "Bearer worker-test-key", nil, 204},
		{"bearer bypasses origin", "https://evil.example", "Bearer worker-test-key", expired, 204},
		{"wrong bearer", s.githubAuth.origin, "Bearer wrong", nil, 401},
		{"bare key", s.githubAuth.origin, "worker-test-key", nil, 401},
		{"empty bearer", "", "Bearer ", nil, 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/investigations", nil)
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Authorization", test.bearer)
			r.Header.Set("X-Forwarded-Host", "evil.example")
			r.Header.Set("X-Forwarded-Proto", "https")
			if test.cookie != nil {
				r.AddCookie(test.cookie)
			}
			w := httptest.NewRecorder()
			s.requireInvestigationAuth(next)(w, r)
			if w.Code != test.want {
				t.Fatalf("auth status: %d want %d: %s", w.Code, test.want, w.Body.String())
			}
		})
	}
	s.githubAuth.allowedUsers["alice"] = false
	if w := authRequest(s.requireInvestigationAuth(next), http.MethodPost, "/write", s.githubAuth.origin, valid); w.Code != http.StatusUnauthorized {
		t.Fatal("removing user from allowlist did not revoke access")
	}
	s.apiKey = ""
	if w := authRequest(s.requireInvestigationAuth(next), http.MethodPost, "/write", ""); w.Code != http.StatusUnauthorized {
		t.Fatal("configured OAuth with empty API key fell back to dev access")
	}
}

func TestAuthLocalReturnTo(t *testing.T) {
	for input, want := range map[string]string{
		"": "/", "/": "/", "/investigations/42?x=1#candidate": "/investigations/42?x=1#candidate",
		"https://evil.example/path": "/", "//evil.example/path": "/", "///evil.example": "/",
		`/\evil.example`: "/", `\evil.example`: "/", "/%5Cevil.example": "/", "/%2Fevil.example": "/",
		"/%": "/", "/\n/evil.example": "/", strings.Repeat("/a", 1025): "/",
	} {
		if got := localReturnTo(input); got != want {
			t.Errorf("localReturnTo(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGitHubOriginAnchor(t *testing.T) {
	for _, test := range []struct{ callback, origin string }{
		{"https://BENCH.example:443", "https://bench.example"},
		{"https://bench.example:8443", "https://bench.example:8443"},
		{"http://localhost:80", "http://localhost"},
		{"http://[::1]:80", "http://[::1]"},
	} {
		t.Run(test.callback, func(t *testing.T) {
			githubTestEnv(t)
			t.Setenv("BENCH_GITHUB_CALLBACK_URL", test.callback+githubCallbackPath)
			auth, err := loadGitHubAuth("worker-test-key")
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{apiKey: "worker-test-key", githubAuth: auth}
			cookie := testSessionCookie(t, s, "alice", time.Now().Add(time.Hour).Unix())
			next := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
			w := authRequest(s.requireInvestigationAuth(next), http.MethodPost, "/write", test.origin, cookie)
			if w.Code != http.StatusNoContent {
				t.Fatalf("same-origin write rejected: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAuthMethods(t *testing.T) {
	s := githubTestServer(t)
	for _, handler := range []http.HandlerFunc{s.handleAuthSession, s.handleGitHubLogin, s.handleGitHubCallback} {
		if w := authRequest(handler, http.MethodPost, "/", ""); w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST allowed: %d", w.Code)
		}
	}
	if w := authRequest(s.handleAuthLogout, http.MethodGet, "/api/auth/logout", s.githubAuth.origin); w.Code != http.StatusMethodNotAllowed || len(w.Result().Cookies()) != 0 {
		t.Fatalf("GET logout allowed: %d", w.Code)
	}
}
