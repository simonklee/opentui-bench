package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	githubCallbackPath = "/api/auth/github/callback"
	oauthStateLifetime = 10 * time.Minute
	sessionLifetime    = 8 * time.Hour
	githubResponseMax  = 64 << 10
)

var githubLoginPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

type githubAuth struct {
	clientID     string
	clientSecret string
	callbackURL  string
	origin       string
	allowedUsers map[string]bool
	secret       []byte
	secure       bool
	client       *http.Client
}

type oauthState struct {
	Nonce    string `json:"nonce"`
	ReturnTo string `json:"return_to"`
	Expires  int64  `json:"expires"`
}

type browserSession struct {
	Login   string `json:"login"`
	Expires int64  `json:"expires"`
}

func loadGitHubAuth(apiKey string) (*githubAuth, error) {
	names := []string{
		"BENCH_GITHUB_CLIENT_ID", "BENCH_GITHUB_CLIENT_SECRET", "BENCH_GITHUB_ALLOWED_USERS",
		"BENCH_GITHUB_CALLBACK_URL", "BENCH_SESSION_SECRET",
	}
	configured := false
	for _, name := range names {
		configured = configured || os.Getenv(name) != ""
	}
	if !configured {
		return nil, nil
	}
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return nil, fmt.Errorf("%s must be set when GitHub authentication is configured", name)
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("BENCH_API_KEY must be set when GitHub authentication is configured")
	}
	secret := os.Getenv("BENCH_SESSION_SECRET")
	if len(strings.TrimSpace(secret)) < 32 {
		return nil, fmt.Errorf("BENCH_SESSION_SECRET must contain at least 32 bytes")
	}
	callback, err := url.Parse(os.Getenv("BENCH_GITHUB_CALLBACK_URL"))
	if err != nil || callback.Hostname() == "" || callback.User != nil || callback.RawQuery != "" ||
		callback.ForceQuery || callback.Fragment != "" || callback.RawPath != "" || callback.Path != githubCallbackPath {
		return nil, fmt.Errorf("BENCH_GITHUB_CALLBACK_URL must be an absolute URL with path %s and no credentials, query, or fragment", githubCallbackPath)
	}
	localhost := callback.Hostname() == "localhost" || callback.Hostname() == "127.0.0.1" || callback.Hostname() == "::1"
	if callback.Scheme != "https" && !(callback.Scheme == "http" && localhost) {
		return nil, fmt.Errorf("BENCH_GITHUB_CALLBACK_URL must use HTTPS (HTTP is allowed only on localhost)")
	}
	defaultPort := ":443"
	if callback.Scheme == "http" {
		defaultPort = ":80"
	}
	origin := callback.Scheme + "://" + strings.TrimSuffix(strings.ToLower(callback.Host), defaultPort)
	allowed := make(map[string]bool)
	for _, login := range strings.Split(os.Getenv("BENCH_GITHUB_ALLOWED_USERS"), ",") {
		login = strings.ToLower(strings.TrimSpace(login))
		if !githubLoginPattern.MatchString(login) {
			return nil, fmt.Errorf("BENCH_GITHUB_ALLOWED_USERS must contain comma-separated GitHub logins")
		}
		allowed[login] = true
	}
	return &githubAuth{
		clientID:     os.Getenv("BENCH_GITHUB_CLIENT_ID"),
		clientSecret: os.Getenv("BENCH_GITHUB_CLIENT_SECRET"),
		callbackURL:  callback.String(),
		origin:       origin,
		allowedUsers: allowed,
		secret:       []byte(secret),
		secure:       callback.Scheme == "https",
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (a *githubAuth) cookieName(kind string) string {
	if a.secure {
		return "__Host-bench_" + kind
	}
	return "bench_" + kind
}

func (a *githubAuth) signature(kind, payload string) []byte {
	mac := hmac.New(sha256.New, a.secret)
	_, _ = io.WriteString(mac, kind+"."+payload)
	return mac.Sum(nil)
}

func (a *githubAuth) setCookie(w http.ResponseWriter, kind string, value any, lifetime time.Duration) {
	data, _ := json.Marshal(value)
	payload := base64.RawURLEncoding.EncodeToString(data)
	signature := base64.RawURLEncoding.EncodeToString(a.signature(kind, payload))
	a.writeCookie(w, kind, payload+"."+signature, int(lifetime/time.Second), time.Now().Add(lifetime))
}

func (a *githubAuth) clearCookie(w http.ResponseWriter, kind string) {
	a.writeCookie(w, kind, "", -1, time.Unix(1, 0))
}

func (a *githubAuth) writeCookie(w http.ResponseWriter, kind, value string, maxAge int, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: a.cookieName(kind), Value: value, Path: "/",
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: maxAge, Expires: expires,
	})
}

func (a *githubAuth) readCookie(r *http.Request, kind string, target any) bool {
	cookie, err := r.Cookie(a.cookieName(kind))
	if err != nil || len(cookie.Value) > 4096 {
		return false
	}
	payload, signature, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(sig, a.signature(kind, payload)) {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	return err == nil && json.Unmarshal(data, target) == nil
}

func (s *Server) sessionLogin(r *http.Request) string {
	if s.githubAuth == nil {
		return ""
	}
	var session browserSession
	if !s.githubAuth.readCookie(r, "session", &session) || session.Expires <= time.Now().Unix() ||
		!s.githubAuth.allowedUsers[strings.ToLower(session.Login)] {
		return ""
	}
	return session.Login
}

func (s *Server) requireInvestigationAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" && s.githubAuth == nil {
			next(w, r)
			return
		}
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.apiKey != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(s.apiKey)) == 1 {
			next(w, r)
			return
		}
		if s.sessionLogin(r) == "" {
			authError(w, "Sign in with GitHub, then retry.", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.githubAuth.sameOrigin(r) {
			authError(w, "Request origin does not match the configured site.", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (a *githubAuth) sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	return len(origins) == 1 && origins[0] == a.origin
}

func authError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		authError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	login := s.sessionLogin(r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"configured":              s.githubAuth != nil,
		"authentication_required": s.apiKey != "" || s.githubAuth != nil,
		"authenticated":           login != "",
		"login":                   login,
	})
}

func localReturnTo(value string) string {
	if len(value) > 2048 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, `\`) {
		return "/"
	}
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, `\`) {
		return "/"
	}
	return u.String()
}

func (s *Server) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		authError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.githubAuth == nil {
		authError(w, "GitHub sign-in is not configured. This website is read-only when authentication is required.", http.StatusServiceUnavailable)
		return
	}
	state := oauthState{
		Nonce:    rand.Text(),
		ReturnTo: localReturnTo(r.URL.Query().Get("return_to")),
		Expires:  time.Now().Add(oauthStateLifetime).Unix(),
	}
	a := s.githubAuth
	a.setCookie(w, "oauth_state", state, oauthStateLifetime)
	query := url.Values{
		"client_id": {a.clientID}, "redirect_uri": {a.callbackURL},
		"state": {state.Nonce}, "allow_signup": {"false"},
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+query.Encode(), http.StatusFound)
}

func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		authError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.githubAuth == nil {
		authError(w, "GitHub sign-in is not configured.", http.StatusServiceUnavailable)
		return
	}
	a := s.githubAuth
	a.clearCookie(w, "oauth_state")
	var state oauthState
	query := r.URL.Query()
	if !a.readCookie(r, "oauth_state", &state) || state.Expires <= time.Now().Unix() || state.Nonce == "" ||
		len(query["state"]) != 1 || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state.Nonce)) != 1 {
		authError(w, "Sign-in expired or state did not match. Start GitHub sign-in again.", http.StatusBadRequest)
		return
	}
	if query.Get("error") != "" || len(query["code"]) != 1 || query.Get("code") == "" || len(query.Get("code")) > 1024 {
		authError(w, "GitHub sign-in was not completed. Start sign-in again.", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	login, err := a.exchangeLogin(ctx, query.Get("code"))
	if err != nil {
		authError(w, "GitHub sign-in failed. Start sign-in again.", http.StatusBadGateway)
		return
	}
	if !a.allowedUsers[strings.ToLower(login)] {
		authError(w, "This GitHub account is not allowed to sign in.", http.StatusForbidden)
		return
	}
	a.setCookie(w, "session", browserSession{Login: login, Expires: time.Now().Add(sessionLifetime).Unix()}, sessionLifetime)
	http.Redirect(w, r, localReturnTo(state.ReturnTo), http.StatusSeeOther)
}

func (a *githubAuth) exchangeLogin(ctx context.Context, code string) (string, error) {
	body := url.Values{"client_id": {a.clientID}, "client_secret": {a.clientSecret}, "code": {code}, "redirect_uri": {a.callbackURL}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	if err := a.readProviderJSON(req, &token); err != nil {
		return "", err
	}
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") || token.Error != "" {
		return "", fmt.Errorf("invalid GitHub token response")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	var user struct {
		Login string `json:"login"`
	}
	if err := a.readProviderJSON(req, &user); err != nil {
		return "", err
	}
	if !githubLoginPattern.MatchString(user.Login) {
		return "", fmt.Errorf("invalid GitHub user response")
	}
	return user.Login, nil
}

func (a *githubAuth) readProviderJSON(req *http.Request, target any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "opentui-bench")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub request failed")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, githubResponseMax+1))
	if err != nil {
		return err
	}
	if len(body) > githubResponseMax {
		return fmt.Errorf("GitHub response too large")
	}
	return json.Unmarshal(body, target)
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		authError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.githubAuth != nil {
		if !s.githubAuth.sameOrigin(r) {
			authError(w, "Request origin does not match the configured site.", http.StatusForbidden)
			return
		}
		s.githubAuth.clearCookie(w, "session")
		s.githubAuth.clearCookie(w, "oauth_state")
	}
	w.WriteHeader(http.StatusNoContent)
}
