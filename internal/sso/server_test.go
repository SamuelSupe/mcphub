package sso

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

// Exercises the complete browser/code/token boundary with a confidential
// upstream, including the OAuth2 envelope used by enterprise identity APIs.
func TestFederatedLoginAndLocalAuthorization(t *testing.T) {
	for _, protocol := range []string{"oidc", "oauth2"} {
		t.Run(protocol, func(t *testing.T) {
			ctx := t.Context()
			store, err := configstore.Open(ctx, filepath.Join(t.TempDir(), "sso.db"), make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "upstream"))
			var upstream *httptest.Server
			var mu sync.Mutex
			codes := map[string]url.Values{}
			badNonce, badTenant := false, false
			upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					respond(w, 200, map[string]any{"issuer": upstream.URL, "authorization_endpoint": upstream.URL + "/authorize", "token_endpoint": upstream.URL + "/token", "jwks_uri": upstream.URL + "/jwks", "code_challenge_methods_supported": []string{"S256"}, "id_token_signing_alg_values_supported": []string{"EdDSA"}, "authorization_response_iss_parameter_supported": true})
				case "/jwks":
					respond(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: pub, KeyID: "upstream", Algorithm: "EdDSA", Use: "sig"}}})
				case "/authorize":
					q := r.URL.Query()
					if q.Get("client_id") != "enterprise-app" || q.Get("code_challenge_method") != "S256" || q.Get("client_secret") != "" {
						http.Error(w, "invalid authorization request", 400)
						return
					}
					code := rand.Text()
					mu.Lock()
					codes[code] = q
					mu.Unlock()
					target, _ := url.Parse(q.Get("redirect_uri"))
					target.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {upstream.URL}}.Encode()
					http.Redirect(w, r, target.String(), 302)
				case "/token":
					_ = r.ParseForm()
					mu.Lock()
					q, ok := codes[r.Form.Get("code")]
					delete(codes, r.Form.Get("code"))
					nonceInvalid, tenantInvalid := badNonce, badTenant
					mu.Unlock()
					if !ok || r.Form.Get("client_secret") != "test-upstream-secret" || r.Form.Get("client_id") != "enterprise-app" || r.Form.Get("redirect_uri") != q.Get("redirect_uri") || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != q.Get("code_challenge") {
						oauthError(w, 400, "invalid_grant")
						return
					}
					nonce := q.Get("nonce")
					if nonceInvalid {
						nonce = "wrong"
					}
					tenant := "tenant-a"
					if tenantInvalid {
						tenant = "tenant-b"
					}
					raw, _ := jwt.Signed(signer).Claims(jwt.Claims{Issuer: upstream.URL, Subject: "external-user", Audience: jwt.Audience{"enterprise-app"}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}).Claims(map[string]any{"nonce": nonce, "name": "Alice", "tenant": tenant, "groups": []string{"staff"}, "departments": []string{"dev"}, "acr": "urn:test:mfa", "auth_time": time.Now().Unix()}).Serialize()
					respond(w, 200, map[string]any{"access_token": "upstream-private-token", "token_type": "Bearer", "expires_in": 600, "id_token": raw})
				case "/userinfo":
					if r.Header.Get("Authorization") != "Bearer upstream-private-token" {
						http.Error(w, "denied", 401)
						return
					}
					mu.Lock()
					tenantInvalid, responseInvalid := badTenant, badNonce
					mu.Unlock()
					tenant := "tenant-a"
					if tenantInvalid {
						tenant = "tenant-b"
					}
					resultCode := 0
					if responseInvalid {
						resultCode = 20022
					}
					respond(w, 200, map[string]any{"code": resultCode, "data": map[string]any{"open_id": "external-user", "name": "Alice", "tenant_key": tenant, "groups": []string{"staff"}, "departments": []string{"dev"}}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			var service *Server
			hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { service.ServeHTTP(w, r) }))
			defer hub.Close()
			t.Setenv("TEST_SSO_SECRET", "test-upstream-secret")
			provider := config.IdentityProvider{Protocol: protocol, Issuer: upstream.URL, ClientID: "enterprise-app", ClientSecretEnv: "TEST_SSO_SECRET", TokenAuthMethod: "client_secret_post", AuthorizationURL: upstream.URL + "/authorize", TokenURL: upstream.URL + "/token", UserInfoURL: upstream.URL + "/userinfo", NameClaim: "name", TenantClaim: "tenant", TenantValue: "tenant-a", GroupsClaim: "groups", DepartmentsClaim: "departments"}
			if protocol == "oauth2" {
				provider.SuccessClaim = "code"
				provider.SuccessValue = "0"
				provider.SubjectClaim = "data.open_id"
				provider.NameClaim = "data.name"
				provider.TenantClaim = "data.tenant_key"
				provider.GroupsClaim = "data.groups"
				provider.DepartmentsClaim = "data.departments"
			}
			resource := hub.URL + "/mcp"
			cfg := &config.Config{Server: config.ServerConfig{PublicURL: resource}, Auth: config.AuthConfig{Issuer: hub.URL + "/sso", SSO: &config.SSOConfig{Upstream: provider, Clients: []config.SSOClient{{ID: "cli", RedirectURIs: []string{"http://127.0.0.1/oauth/callback"}, Resources: []string{resource}}}}}, Admin: config.AdminConfig{RequiredScopes: []string{"mcphub:admin"}}}
			service, err = New(ctx, cfg, store)
			if err != nil {
				t.Fatal(err)
			}
			service.client = upstream.Client()
			service.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			browser := *hub.Client()
			browser.Jar, _ = cookiejar.New(nil)
			browser.CheckRedirect = service.client.CheckRedirect
			get := func(raw string) *http.Response {
				t.Helper()
				response, err := browser.Get(raw)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			flow := func(want int) (string, string) {
				t.Helper()
				verifier := oauth2.GenerateVerifier()
				query := url.Values{"client_id": {"cli"}, "resource": {resource}, "redirect_uri": {"http://127.0.0.1:41001/oauth/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "state": {"cli-state"}, "nonce": {"cli-nonce"}, "scope": {"openid offline_access db:read forbidden"}}
				response := get(hub.URL + "/sso/authorize?" + query.Encode())
				defer response.Body.Close()
				if response.StatusCode != 302 {
					t.Fatalf("start login: %d", response.StatusCode)
				}
				response = get(response.Header.Get("Location"))
				defer response.Body.Close()
				if response.StatusCode != 302 {
					t.Fatalf("upstream login: %d", response.StatusCode)
				}
				response = get(response.Header.Get("Location"))
				defer response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("callback: %d, want %d", response.StatusCode, want)
				}
				if want != 303 {
					return "", verifier
				}
				target, _ := url.Parse(response.Header.Get("Location"))
				if target.Query().Get("state") != "cli-state" || target.Query().Get("iss") != cfg.Auth.Issuer {
					t.Fatal("downstream state or issuer missing")
				}
				return target.Query().Get("code"), verifier
			}
			post := func(values url.Values, want int) map[string]any {
				t.Helper()
				response, err := browser.PostForm(hub.URL+"/sso/token", values)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("token status: %d, want %d", response.StatusCode, want)
				}
				var data map[string]any
				if err = json.NewDecoder(response.Body).Decode(&data); err != nil {
					t.Fatal(err)
				}
				return data
			}
			exchange := func(code, verifier string, want int) map[string]any {
				return post(url.Values{"grant_type": {"authorization_code"}, "client_id": {"cli"}, "resource": {resource}, "redirect_uri": {"http://127.0.0.1:41001/oauth/callback"}, "code": {code}, "code_verifier": {verifier}}, want)
			}
			if _, err = authn.LoginMetadata(ctx, cfg.Auth.Issuer, &browser, true); err != nil {
				t.Fatal("public client discovery", err)
			}
			flow(403)
			identities, err := store.Identities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var user configstore.Identity
			for _, p := range identities {
				if p.Kind == "user" {
					user = p
				}
			}
			if len(identities) != 3 || len(user.Groups) != 2 {
				t.Fatal("groups/departments were not synchronized")
			}
			user, err = store.UpdateIdentity(ctx, user.ID, user.Revision, true, config.IdentityPermissions{Scopes: []string{"db:read"}, Access: []config.IdentityAccess{{EndpointID: "db", Tools: []string{"read"}}}})
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			badNonce = true
			mu.Unlock()
			flow(401)
			mu.Lock()
			badNonce = false
			mu.Unlock()
			mu.Lock()
			badTenant = true
			mu.Unlock()
			flow(403)
			mu.Lock()
			badTenant = false
			mu.Unlock()
			code, verifier := flow(303)
			exchange(code, oauth2.GenerateVerifier(), 400)
			exchange(code, verifier, 400)
			code, verifier = flow(303)
			result := exchange(code, verifier, 200)
			exchange(code, verifier, 400)
			raw := result["access_token"].(string)
			info, err := service.Verifier(resource).Verify(ctx, raw, nil)
			if err != nil || info.UserID != user.ID || !slices.Contains(info.Scopes, "db:read") || slices.Contains(info.Scopes, "forbidden") {
				t.Fatal("local scope/subject binding", err)
			}
			otherConfig := *cfg
			otherSSO := *cfg.Auth.SSO
			otherSSO.Upstream.ClientID = "different-enterprise-app"
			otherConfig.Auth.SSO = &otherSSO
			otherService, err := New(ctx, &otherConfig, store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = otherService.Verifier(resource).Verify(ctx, raw, nil); err == nil {
				t.Fatal("old connection identity crossed an app boundary")
			}
			if _, err = service.Verifier(hub.URL+"/admin").Verify(ctx, raw, nil); err == nil {
				t.Fatal("MCP token accepted for admin audience")
			}
			idVerifier, err := authn.LoginIDVerifier(ctx, cfg.Auth.Issuer, "cli", &browser)
			if err != nil {
				t.Fatal(err)
			}
			id, err := idVerifier.Verify(ctx, result["id_token"].(string))
			if err != nil || id.Subject != user.ID || id.Nonce != "cli-nonce" {
				t.Fatal("local ID token failed", err)
			}
			if _, err = service.Verifier(resource).Verify(ctx, result["id_token"].(string), nil); err == nil {
				t.Fatal("ID token accepted as access token")
			}
			mu.Lock()
			var acr struct {
				ACR string `json:"acr"`
			}
			_ = id.Claims(&acr)
			mu.Unlock()
			if protocol == "oidc" && acr.ACR != "urn:test:mfa" {
				t.Fatal("verified MFA evidence lost")
			}
			if protocol == "oauth2" && acr.ACR != "" {
				t.Fatal("OAuth2 fabricated MFA evidence")
			}
			refresh := result["refresh_token"].(string)
			rotated := post(url.Values{"grant_type": {"refresh_token"}, "client_id": {"cli"}, "resource": {resource}, "refresh_token": {refresh}}, 200)
			if rotated["refresh_token"] == refresh {
				t.Fatal("refresh token not rotated")
			}
			post(url.Values{"grant_type": {"refresh_token"}, "client_id": {"cli"}, "resource": {resource}, "refresh_token": {refresh}}, 400)
			if _, err = service.Verifier(resource).Verify(ctx, rotated["access_token"].(string), nil); err == nil {
				t.Fatal("replayed refresh family remains valid")
			}
			code, verifier = flow(303)
			result = exchange(code, verifier, 200)
			user, err = store.Identity(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.UpdateIdentity(ctx, user.ID, user.Revision, false, user.Permissions); err != nil {
				t.Fatal(err)
			}
			if _, err = service.Verifier(resource).Verify(ctx, result["access_token"].(string), nil); err == nil {
				t.Fatal("disabled user retained access")
			}
			post(url.Values{"grant_type": {"refresh_token"}, "client_id": {"cli"}, "resource": {resource}, "refresh_token": {result["refresh_token"].(string)}}, 400)
			response := get(hub.URL + "/sso/callback?state=forged&code=forged")
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 400 {
				t.Fatal("forged callback accepted")
			}
			response = get(hub.URL + "/sso/authorize?" + url.Values{"client_id": {"cli"}, "redirect_uri": {"https://attacker.example/callback"}, "resource": {resource}}.Encode())
			response.Body.Close()
			if response.StatusCode != 400 || strings.Contains(response.Header.Get("Location"), "attacker") {
				t.Fatal("unregistered redirect accepted")
			}
			directoryConfig := *cfg
			directorySSO := *cfg.Auth.SSO
			directorySSO.DirectoryTokenEnv = "TEST_DIRECTORY_SECRET"
			directoryConfig.Auth.SSO = &directorySSO
			t.Setenv("TEST_DIRECTORY_SECRET", strings.Repeat("x", 32))
			directoryService, err := New(ctx, &directoryConfig, store)
			if err != nil {
				t.Fatal(err)
			}
			user, err = store.Identity(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			user, err = store.UpdateIdentity(ctx, user.ID, user.Revision, true, user.Permissions)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = directoryService.tokenIdentity(ctx, user.ID); err == nil {
				t.Fatal("claim-only identity accepted without provisioning")
			}
			request := httptest.NewRequest(http.MethodPut, hub.URL+"/sso/directory", strings.NewReader(`{"version":1,"users":[{"subject":"external-user","active":true,"roles":["admin"]}]}`))
			request.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
			responseRecorder := httptest.NewRecorder()
			directoryService.ServeHTTP(responseRecorder, request)
			if responseRecorder.Code != 400 {
				t.Fatal("directory credential could assign privileges")
			}
			request = httptest.NewRequest(http.MethodPut, hub.URL+"/sso/directory", strings.NewReader(`{"version":1,"users":[{"subject":"external-user","active":true}],"groups":[]}`))
			request.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
			responseRecorder = httptest.NewRecorder()
			directoryService.ServeHTTP(responseRecorder, request)
			if responseRecorder.Code != 200 {
				t.Fatal("directory provisioning failed")
			}
			if _, err = directoryService.tokenIdentity(ctx, user.ID); err != nil {
				t.Fatal("provisioned identity rejected", err)
			}
			request = httptest.NewRequest(http.MethodPut, hub.URL+"/sso/directory", strings.NewReader(`{"version":2,"users":[],"groups":[]}`))
			responseRecorder = httptest.NewRecorder()
			directoryService.ServeHTTP(responseRecorder, request)
			if responseRecorder.Code != 401 {
				t.Fatal("unauthenticated directory mutation accepted")
			}
			_ = store.Close()
			outage := post(url.Values{"grant_type": {"refresh_token"}, "client_id": {"cli"}, "resource": {resource}, "refresh_token": {result["refresh_token"].(string)}}, 503)
			if outage["error"] != "temporarily_unavailable" {
				t.Fatal("storage outage misclassified as invalid credentials")
			}

		})
	}
}
