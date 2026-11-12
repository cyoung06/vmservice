// Package auth is sign-in: the OpenID Connect authorization code flow with PKCE, and the session
// cookie it ends in.
//
// The session is an encrypted cookie holding the owner id, the username and an expiry, and nothing
// is kept on the server. The required group is checked once, in the ID token at sign-in. Removing someone from
// the group takes effect when their cookie expires (-session-lifetime).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config is what sign-in needs.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// Origin is the public origin (https://vm.example.org, no trailing slash); the redirect URI is
	// Origin + /auth/callback.
	Origin string
	// Group must be in the ID token's GroupsClaim; empty: anyone the provider signs in.
	Group string
	// AdminGroup in the groups makes the session an admin's; empty: no admins.
	AdminGroup    string
	GroupsClaim   string
	UsernameClaim string
	KeyFile       string
	Lifetime      time.Duration
	// OnSignIn, if set, runs before the session is issued (e.g. recording the member); an error
	// refuses the sign-in.
	OnSignIn func(ctx context.Context, owner, username string) error
}

// Session is a signed-in user.
type Session struct {
	// Owner is the user's stable id (OwnerID of the issuer and the ID token's sub): what their
	// machines are labeled with. A username can be freed and signed up again; sub can't.
	Owner    string    `json:"o"`
	Username string    `json:"u"` // for display
	Admin    bool      `json:"a,omitempty"`
	Expires  time.Time `json:"e"`
}

// OwnerID is a label-safe id for a user: the first 40 hex digits of SHA-256(issuer, NUL, sub).
func OwnerID(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return hex.EncodeToString(sum[:])[:40]
}

var ownerID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// pending is the login cookie: what the callback needs to finish a sign-in it started.
type pending struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Next     string    `json:"x"`
	Expires  time.Time `json:"e"`
}

const (
	sessionCookie = "vmservice_session"
	loginCookie   = "vmservice_login"
	loginWindow   = 10 * time.Minute
)

// Auth is sign-in and the session check.
type Auth struct {
	cfg      Config
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	seal     *sealer
	secure   bool
	log      *slog.Logger
}

// New discovers the provider and reads the session key.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*Auth, error) {
	seal, err := newSealer(cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery of %s: %w", cfg.Issuer, err)
	}
	origin := cfg.Origin
	cfg.Group = strings.TrimPrefix(cfg.Group, "/") // Keycloak shows groups as paths; the claim's are trimmed too
	cfg.AdminGroup = strings.TrimPrefix(cfg.AdminGroup, "/")
	return &Auth{
		cfg: cfg,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  origin + "/auth/callback",
			Scopes:       []string{oidc.ScopeOpenID, "profile"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		seal:     seal,
		secure:   strings.HasPrefix(origin, "https://"),
		log:      log,
	}, nil
}

// Routes adds /auth/login, /auth/callback and /auth/logout.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/logout", a.logout)
}

// Session returns the request's session, or false if there is none or it has expired.
func (a *Auth) Session(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	var s Session
	if a.seal.open(sessionCookie, c.Value, &s) != nil || !time.Now().Before(s.Expires) || !ownerID.MatchString(s.Owner) {
		return Session{}, false
	}
	return s, true
}

func random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// localPath keeps next a path on this origin: "/x", not "//host" or "https://host".
func localPath(next string) string {
	u, err := url.Parse(next)
	if next == "" || err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		return "/"
	}
	return next
}

func (a *Auth) setCookie(w http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	p := pending{
		State:    random(),
		Nonce:    random(),
		Verifier: oauth2.GenerateVerifier(),
		Next:     localPath(r.URL.Query().Get("next")),
		Expires:  time.Now().Add(loginWindow),
	}
	value, err := a.seal.seal(loginCookie, p)
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	a.setCookie(w, loginCookie, value, p.Expires)
	http.Redirect(w, r, a.oauth.AuthCodeURL(p.State, oidc.Nonce(p.Nonce), oauth2.S256ChallengeOption(p.Verifier)), http.StatusFound)
}

func (a *Auth) refuse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, message)
	fmt.Fprintln(w, "Sign in again from the start page.")
}

func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	var p pending
	if err != nil || a.seal.open(loginCookie, c.Value, &p) != nil || !time.Now().Before(p.Expires) {
		a.refuse(w, http.StatusBadRequest, "This sign-in has expired or was started in another browser.")
		return
	}
	a.clearCookie(w, loginCookie)
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		// Logged, not shown: anyone can put text in this query string.
		a.log.Info("identity provider refused the sign-in", "error", e, "description", q.Get("error_description"))
		a.refuse(w, http.StatusForbidden, "The identity provider refused the sign-in.")
		return
	}
	if q.Get("state") != p.State {
		a.refuse(w, http.StatusBadRequest, "This sign-in doesn't match the one this browser started.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := a.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(p.Verifier))
	if err != nil {
		a.log.Warn("code exchange", "err", err)
		a.refuse(w, http.StatusBadGateway, "The identity provider didn't accept the sign-in code.")
		return
	}
	raw, _ := token.Extra("id_token").(string)
	idToken, err := a.verifier.Verify(ctx, raw)
	if err != nil || idToken.Nonce != p.Nonce {
		a.log.Warn("ID token", "err", err)
		a.refuse(w, http.StatusBadGateway, "The identity provider's ID token didn't verify.")
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		a.refuse(w, http.StatusBadGateway, "The ID token couldn't be read.")
		return
	}
	user, groups := userAndGroups(claims, a.cfg.UsernameClaim, a.cfg.GroupsClaim)
	if user == "" || len(user) > 256 || idToken.Subject == "" {
		a.refuse(w, http.StatusForbidden, fmt.Sprintf("The ID token has no usable %q or sub claim.", a.cfg.UsernameClaim))
		return
	}
	if a.cfg.Group != "" && !slices.Contains(groups, a.cfg.Group) {
		a.log.Info("sign-in refused: not in group", "user", user, "group", a.cfg.Group)
		a.refuse(w, http.StatusForbidden, fmt.Sprintf("%s isn't in the group %q, which this service requires.", user, a.cfg.Group))
		return
	}
	s := Session{Owner: OwnerID(a.cfg.Issuer, idToken.Subject), Username: user, Expires: time.Now().Add(a.cfg.Lifetime).Truncate(time.Second),
		Admin: a.cfg.AdminGroup != "" && slices.Contains(groups, a.cfg.AdminGroup)}
	if a.cfg.OnSignIn != nil {
		if err := a.cfg.OnSignIn(ctx, s.Owner, s.Username); err != nil {
			a.log.Error("sign-in hook", "user", user, "err", err)
			a.refuse(w, http.StatusBadGateway, "The sign-in couldn't be recorded; try again in a moment.")
			return
		}
	}
	value, err := a.seal.seal(sessionCookie, s)
	if err != nil {
		a.refuse(w, http.StatusInternalServerError, "The session couldn't be created.")
		return
	}
	a.setCookie(w, sessionCookie, value, s.Expires)
	a.log.Info("signed in", "user", user, "owner", s.Owner)
	http.Redirect(w, r, p.Next, http.StatusFound)
}

// userAndGroups reads the username and the groups (a list of strings; Keycloak's group mapper
// without full paths) from the ID token's claims.
func userAndGroups(claims map[string]any, userClaim, groupsClaim string) (string, []string) {
	user, _ := claims[userClaim].(string)
	var groups []string
	if list, ok := claims[groupsClaim].([]any); ok {
		for _, g := range list {
			if s, ok := g.(string); ok {
				groups = append(groups, strings.TrimPrefix(s, "/"))
			}
		}
	}
	return user, groups
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		http.Error(w, "missing X-Requested-With", http.StatusForbidden)
		return
	}
	a.clearCookie(w, sessionCookie)
	w.WriteHeader(http.StatusNoContent)
}
