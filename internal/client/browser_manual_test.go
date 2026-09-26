package client

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

// This opt-in check uses the real system browser and only disposable test identities.
func TestNativeBrowserLogin(t *testing.T) {
	if os.Getenv("MCPHUB_BROWSER_QA") != "1" || runtime.GOOS != "darwin" {
		t.Skip("set MCPHUB_BROWSER_QA=1 on macOS for interactive browser verification")
	}
	f := newLoginFixture(t)
	upstream := f.issuer.Config.Handler
	f.issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/authorize" {
			upstream.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		switch q.Get("qa_action") {
		case "allow":
			upstream.ServeHTTP(w, r)
		case "deny":
			u, _ := url.Parse(q.Get("redirect_uri"))
			u.RawQuery = url.Values{"error": {"access_denied"}, "state": {q.Get("state")}, "iss": {f.issuer.URL}}.Encode()
			http.Redirect(w, r, u.String(), http.StatusFound)
		default:
			q.Set("qa_action", "allow")
			allow := "/authorize?" + q.Encode()
			q.Set("qa_action", "deny")
			deny := "/authorize?" + q.Encode()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><html lang="en"><meta charset="utf-8"><title>MCPHub test authorization</title><h1>MCPHub test authorization</h1><p>Disposable local test identity. No real account or password is used.</p><p><a href="%s">Authorize MCPHub</a></p><p><a href="%s">Deny authorization</a></p></html>`, html.EscapeString(allow), html.EscapeString(deny))
		}
	})
	// The browser consent screen is a disposable loopback fixture, so Chrome
	// needs no certificate exception. Discovery, token exchange and the Hub
	// still use the HTTPS test servers and their explicitly trusted test CA.
	consent := httptest.NewServer(f.issuer.Config.Handler)
	defer consent.Close()
	consentURL, _ := url.Parse(consent.URL)
	opts := LoginOptions{Profile: "work", ServerURL: f.hub.URL + "/mcp", ClientID: "mcphub-cli", HTTPClient: f.client, Scopes: []string{"mcp:alpha"}, Output: os.Stderr}
	opts.OpenBrowser = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		u.Scheme, u.Host = consentURL.Scheme, consentURL.Host
		return openBrowser(u.String())
	}
	t.Log("Choose Authorize MCPHub in the browser.")
	if err := Login(t.Context(), f.store, opts); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(f.store.Dir, "work.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Choose Deny authorization in the new browser tab.")
	if err := Login(t.Context(), f.store, opts); err == nil {
		t.Fatal("denied login succeeded")
	}
	after, _ := os.ReadFile(filepath.Join(f.store.Dir, "work.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("browser denial replaced credentials")
	}
	t.Log("Leave the final browser tab open; login will be canceled after 10 seconds.")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := Login(ctx, f.store, opts); err == nil {
		t.Fatal("canceled login succeeded")
	}
	after, _ = os.ReadFile(filepath.Join(f.store.Dir, "work.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("browser cancellation replaced credentials")
	}
}

func TestNativeBrowserClientAuthorization(t *testing.T) {
	if os.Getenv("MCPHUB_BROWSER_QA") != "1" || runtime.GOOS != "darwin" {
		t.Skip("set MCPHUB_BROWSER_QA=1 on macOS for interactive broker portal verification")
	}
	f := newLoginFixture(t, true)
	if os.Getenv("MCPHUB_SETUP_BROWSER_QA") == "1" {
		dir, err := os.MkdirTemp("/tmp", "mh-setup-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		f.store = &Store{Dir: dir}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Only this disposable browser fixture uses a loopback HTTP frontend. Its
	// upstream exchanges still validate the test CA. Production remains HTTPS;
	// no browser certificate exceptions or OS trust changes are needed.
	upstream := *f.client
	upstream.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var frontend string
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base, path := f.hub.URL, r.URL.Path
		if strings.HasPrefix(path, "/qa-idp/") {
			base, path = f.issuer.URL, strings.TrimPrefix(path, "/qa-idp")
		}
		request := r.Clone(r.Context())
		request.URL, _ = url.Parse(base + path)
		request.URL.RawQuery, request.RequestURI, request.Host = r.URL.RawQuery, "", request.URL.Host
		if request.Header.Get("Origin") == frontend {
			request.Header.Set("Origin", f.hub.URL)
		}
		response, err := upstream.Do(request)
		if err != nil {
			http.Error(w, "Fixture unavailable", 502)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		location := response.Header.Get("Location")
		if strings.HasPrefix(location, f.issuer.URL+"/") {
			w.Header().Set("Location", frontend+"/qa-idp"+strings.TrimPrefix(location, f.issuer.URL))
		}
		if strings.HasPrefix(location, f.hub.URL+"/") {
			w.Header().Set("Location", frontend+strings.TrimPrefix(location, f.hub.URL))
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	proxy.Start()
	defer proxy.Close()
	_, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	frontend = "http://localhost:" + port
	open := func(raw string) error {
		raw = frontend + strings.TrimPrefix(raw, f.hub.URL)
		t.Logf("Review the disposable test request in Chrome: %s", raw)
		return exec.Command("open", "-a", "Google Chrome", raw).Run()
	}
	if os.Getenv("MCPHUB_SETUP_BROWSER_QA") == "1" {
		if err := Setup(ctx, f.store, "work", SetupOptions{Input: os.Stdin, Output: os.Stderr, ConfigOutput: os.Stdout, HTTPClient: f.client, OpenBrowser: open}); err != nil {
			t.Fatal(err)
		}
		if f.toolCalls.Load() != 0 {
			t.Fatal("setup executed a tool")
		}
		return
	}
	opts := ClientOptions{Name: "Chrome QA editor", Endpoint: "alpha", Scopes: []string{"mcp:alpha"}, Tools: []string{"echo"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"project-a"}}}, HTTPClient: f.client, OpenBrowser: open, Output: os.Stderr}
	t.Log("Sign in as the disposable test user and confirm this client's read access.")
	g, err := AuthorizeClient(ctx, f.store, "work", opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.ID, opts.Changed = g.ClientID, []string{"resource"}
	opts.ResourceRules[0].AllowedValues = []string{"project-a", "project-b"}
	t.Log("Confirm the explicit resource expansion; both projects must appear in the consent card.")
	g, err = AuthorizeClient(ctx, f.store, "work", opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.ID, opts.Changed, opts.Name = "", nil, "Chrome QA denied client"
	t.Log("Deny the next request. Then revoke the active editor grant in the portal; inspect Chinese/English and mobile layout before revoking.")
	if _, err := AuthorizeClient(ctx, f.store, "work", opts); err == nil {
		t.Fatal("denied request succeeded")
	}
	a, err := newAuthorizationClient(ctx, f.store, "work", f.client)
	if err != nil {
		t.Fatal(err)
	}
	for {
		var current configstore.ClientGrant
		if err := a.request(ctx, http.MethodGet, a.discovery.RequestsURL+"/"+g.GrantID, "", nil, &current); err != nil {
			t.Fatal(err)
		}
		if current.Status == "revoked" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("portal revocation was not completed")
		case <-time.After(time.Second):
		}
	}
}
