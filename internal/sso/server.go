package sso

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

type Server struct {
	cfg     *config.Config
	store   *configstore.Store
	client  *http.Client
	key     ed25519.PrivateKey
	mu      sync.Mutex
	pending map[string]login
	codes   map[string]authorizationCode
}
type login struct {
	expires                  time.Time
	browser, verifier, nonce string
	oauth                    oauth2.Config
	idVerifier               *oidc.IDTokenVerifier
	issuerRequired           bool
	downstream               authorizationCode
}
type authorizationCode struct {
	ClientID, Redirect, Resource, State, Challenge, Nonce, UserID string
	Scopes                                                        []string
	ACR                                                           string
	AuthTime                                                      int64
	Expires                                                       time.Time
}

func New(ctx context.Context, cfg *config.Config, store *configstore.Store) (*Server, error) {
	if store == nil || cfg.Auth.SSO == nil {
		return nil, errors.New("SSO requires configuration storage")
	}
	if os.Getenv(cfg.Auth.SSO.Upstream.ClientSecretEnv) == "" {
		return nil, errors.New("SSO upstream client secret is missing")
	}
	if name := cfg.Auth.SSO.DirectoryTokenEnv; name != "" && len(os.Getenv(name)) < 32 {
		return nil, errors.New("SSO directory token must contain at least 32 characters")
	}
	key, err := store.SSOSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: store, key: key, client: authn.LoginHTTPClient(), pending: map[string]login{}, codes: map[string]authorizationCode{}}, nil
}

func (s *Server) Handles(path string) bool {
	return strings.HasPrefix(path, "/sso/") || path == "/.well-known/oauth-authorization-server/sso" || path == "/.well-known/openid-configuration/sso"
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	issuer, _ := url.Parse(s.cfg.Auth.Issuer)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	if !strings.EqualFold(r.Host, issuer.Host) {
		http.Error(w, "invalid host", 400)
		return
	}
	switch r.URL.Path {
	case "/sso/.well-known/openid-configuration", "/.well-known/openid-configuration/sso", "/.well-known/oauth-authorization-server/sso", "/sso/.well-known/oauth-authorization-server":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		s.metadata(w)
	case "/sso/jwks":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		respond(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: s.key.Public(), KeyID: "mcphub-sso", Algorithm: "EdDSA", Use: "sig"}}})
	case "/sso/authorize":
		if r.Method == http.MethodGet {
			s.authorize(w, r)
		} else {
			http.Error(w, "method not allowed", 405)
		}
	case "/sso/callback":
		if r.Method == http.MethodGet {
			s.callback(w, r)
		} else {
			http.Error(w, "method not allowed", 405)
		}
	case "/sso/token":
		if r.Method == http.MethodPost {
			s.token(w, r)
		} else {
			http.Error(w, "method not allowed", 405)
		}
	case "/sso/directory":
		s.directory(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) metadata(w http.ResponseWriter) {
	issuer := s.cfg.Auth.Issuer
	respond(w, 200, map[string]any{
		"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"EdDSA"},
		"token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"},
		"authorization_response_iss_parameter_supported": true, "scopes_supported": []string{"openid", "offline_access"},
	})
}

func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func oauthError(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]string{"error": code})
}

func (s *Server) cleanupLocked() {
	now := time.Now()
	for state, p := range s.pending {
		if !now.Before(p.expires) {
			delete(s.pending, state)
		}
	}
	for code, p := range s.codes {
		if !now.Before(p.Expires) {
			delete(s.codes, code)
		}
	}
}

func (s *Server) allowedClient(id, redirect, resource string) bool {
	for _, c := range s.cfg.Auth.SSO.Clients {
		if c.ID != id || !slices.Contains(c.Resources, resource) {
			continue
		}
		for _, registered := range c.RedirectURIs {
			if registered == redirect {
				return true
			}
			base, _ := url.Parse(registered)
			target, err := url.Parse(redirect)
			// RFC 8252 permits an ephemeral port only for the registered loopback URI.
			if err == nil && base.Scheme == "http" && base.Host == "127.0.0.1" && target.Hostname() == "127.0.0.1" && target.Port() != "" {
				copy := *target
				copy.Host = "127.0.0.1"
				if copy.String() == registered {
					return true
				}
			}
		}
	}
	return false
}

func (s *Server) directory(w http.ResponseWriter, r *http.Request) {
	secret := os.Getenv(s.cfg.Auth.SSO.DirectoryTokenEnv)
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if secret == "" || len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(configstore.SecretHash(secret)), []byte(configstore.SecretHash(provided))) != 1 {
		http.Error(w, "directory authentication required", 401)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var snapshot configstore.DirectorySnapshot
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid directory snapshot", 400)
		return
	}
	err := s.store.SyncDirectory(configstore.WithActor(r.Context(), "directory-sync"), s.cfg.Auth.SSO.Upstream.Namespace(), snapshot)
	if errors.Is(err, configstore.ErrConflict) {
		http.Error(w, "directory version must increase", 409)
		return
	}
	if err != nil {
		http.Error(w, "directory snapshot rejected", 400)
		return
	}
	respond(w, 200, map[string]any{"version": snapshot.Version, "users": len(snapshot.Users), "groups": len(snapshot.Groups)})
}
