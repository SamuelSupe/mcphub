package client

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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
