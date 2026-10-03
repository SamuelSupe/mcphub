package app

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func deviceError(w http.ResponseWriter, err error) {
	var grant configstore.GrantError
	if errors.As(err, &grant) {
		err = configstore.DeviceDenied
		if grant == configstore.ErrGrantLimit {
			err = configstore.DeviceLimited
		}
	}
	code := "temporarily_unavailable"
	status := http.StatusServiceUnavailable
	var de configstore.DeviceError
	if errors.As(err, &de) {
		code = string(de)
		status = http.StatusBadRequest
		if de == configstore.DeviceLimited {
			w.Header().Set("Retry-After", "60")
			status = 429
		}
	}
	writeJSON(w, status, map[string]string{"error": code})
}
func deviceAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func (a *App) createDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	rt := a.currentRuntime()
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil || len(r.URL.Query()) != 0 || r.Header.Get("Authorization") != "" {
		deviceError(w, configstore.DeviceInvalid)
		return
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			deviceError(w, configstore.DeviceInvalid)
			return
		}
	}
	f := r.PostForm
	if !a.sso.DeviceClientAllowed(f.Get("client_id"), f.Get("resource")) || f.Get("client_secret") != "" {
		deviceError(w, configstore.DeviceInvalid)
		return
	}
	var extension struct {
		Version int `json:"version"`
		clientAuthorizationInput
	}
	if json.Unmarshal([]byte(f.Get("mcphub")), &extension) != nil || extension.Version != 1 {
		deviceError(w, configstore.DeviceInvalid)
		return
	}
	in := extension.clientAuthorizationInput
	if len(in.ClientID) < 16 || len(in.ClientID) > 64 || strings.ContainsFunc(in.ClientID, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '-' }) || len(in.ClientName) < 1 || len(in.ClientName) > 128 || strings.ContainsFunc(in.ClientName, unicode.IsControl) || len(in.EndpointID) > 128 || len(in.Tools) > 256 || len(in.Scopes) > 64 || len(in.ResourceRules) > 32 || len(in.SessionProof) > 256 || len(in.SessionID) > 128 || (in.TTLSeconds != 0 && in.TTLSeconds < 60) || in.TTLSeconds > int64(rt.cfg.ClientAuthorization.GrantTTL()/time.Second) || in.Capabilities.Prompts || in.Capabilities.Resources || in.Capabilities.Subscriptions {
		deviceError(w, configstore.DeviceInvalid)
		return
	}
	scopes := strings.Fields(f.Get("scope"))
	if len(scopes) > 64 {
		deviceError(w, configstore.DeviceInvalid)
		return
	}
	ttl := in.TTLSeconds
	if ttl == 0 {
		ttl = min(3600, int64(rt.cfg.ClientAuthorization.GrantTTL()/time.Second))
	}
	d, code, err := a.store.CreateDeviceAuthorization(r.Context(), configstore.DeviceAuthorization{ClientID: f.Get("client_id"), Resource: f.Get("resource"), Scopes: scopes, Request: configstore.ClientGrant{GrantBinding: configstore.GrantBinding{ClientID: in.ClientID, SessionID: in.SessionID}, ClientName: in.ClientName, EndpointID: in.EndpointID, AllowedTools: in.Tools, AllowedScopes: in.Scopes, ResourceRules: in.ResourceRules, AllowWriteRequests: in.AllowWrite, Capabilities: configstore.GrantCapabilities{Tools: true}}, SessionProof: in.SessionProof, TTLSeconds: ttl}, deviceAddress(r))
	if err != nil {
		deviceError(w, err)
		return
	}
	uri := a.userAuth.cfg.PublicURL + "/client-auth/device"
	writeJSON(w, 200, map[string]any{"device_code": code, "user_code": d.UserCode, "verification_uri": uri, "verification_uri_complete": uri + "?user_code=" + d.UserCode, "expires_in": 300, "interval": 5, "mcphub": map[string]any{"version": 1, "request_id": d.ID}})
}
func (a *App) deviceAuthorizationToken(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	if f.Get("mcphub_cancel") == "1" {
		if err := a.store.CancelDeviceAuthorization(r.Context(), f.Get("device_code"), f.Get("client_id")); err != nil {
			deviceError(w, err)
		} else {
			writeJSON(w, 200, map[string]string{"status": "canceled"})
		}
		return
	}
	d, err := a.store.PollDeviceAuthorization(r.Context(), f.Get("device_code"), f.Get("client_id"), f.Get("resource"))
	if err != nil {
		deviceError(w, err)
		return
	}
	if !a.sso.DeviceClientAllowed(d.ClientID, d.Resource) {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	rt := a.currentRuntime()
	p, err := a.store.EffectiveIdentity(r.Context(), d.Request.Subject)
	if err != nil || !a.sso.ProviderAllowed(p.Provider, p.DirectoryManaged) {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	g := d.Request
	if err = rt.hub.PrepareClientGrant(&g, p.Permissions.EffectiveScopes(rt.cfg.Admin), &p); err != nil {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	d, session, refresh, err := a.store.RedeemDeviceAuthorization(r.Context(), d.ID, d.ClientID)
	if err != nil {
		deviceError(w, err)
		return
	}
	token, err := a.sso.DeviceSessionToken(r.Context(), session, refresh)
	if err != nil {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	result := map[string]any{"access_token": token.AccessToken, "token_type": "Bearer", "expires_in": max(1, int64(time.Until(token.Expiry)/time.Second)), "scope": token.Extra("scope"), "mcphub": map[string]any{"version": 1, "grant_id": d.Request.GrantID, "broker_session_id": d.Request.SessionID, "broker_session_proof": d.Proof, "replaces_broker_session_id": d.ReplacesSession, "exchange_credential": d.Exchange}}
	if refresh != "" {
		result["refresh_token"] = refresh
		result["refresh_token_expires_in"] = max(1, int64(time.Until(session.ExpiresAt)/time.Second))
	}
	writeJSON(w, 200, result)
}
func (a *App) deviceBrowser(w http.ResponseWriter, r *http.Request, rt *runtime, info *mcpauth.TokenInfo, path string) {
	if !rt.cfg.Auth.Builtin() {
		http.NotFound(w, r)
		return
	}
	p := sso.Identity(info)
	if p == nil {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	code := r.URL.Query().Get("user_code")
	var in struct {
		UserCode      string          `json:"user_code"`
		EndpointID    string          `json:"endpoint_id"`
		Tools         []string        `json:"allowed_tools"`
		AllowWrite    bool            `json:"allow_write_requests"`
		TTLSeconds    int64           `json:"ttl_seconds"`
		ResourceRules json.RawMessage `json:"resource_rules"`
	}
	if r.Method == http.MethodPost {
		if !decodeAdminJSON(w, r, &in) {
			return
		}
		code = in.UserCode
	}
	d, err := a.store.DeviceByUserCode(r.Context(), code, deviceAddress(r))
	if err != nil {
		deviceError(w, err)
		return
	}
	if d.Status != "pending" && d.Request.Subject != p.ID {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	if path == "/device" && r.Method == http.MethodGet {
		options := rt.hub.AuthorizationOptions(info.Scopes, p)
		writeJSON(w, 200, map[string]any{"request_id": d.ID, "user_code": d.UserCode, "status": d.Status, "client_name": d.Request.ClientName, "client_instance_id": d.Request.ClientID, "endpoint_id": d.Request.EndpointID, "allowed_tools": d.Request.AllowedTools, "resource_rules": d.Request.ResourceRules, "allow_write_requests": d.Request.AllowWriteRequests, "created_at": d.CreatedAt, "expires_at": d.ExpiresAt, "max_ttl_seconds": d.TTLSeconds, "endpoints": options, "subject": p.ID, "provider": p.Provider})
		return
	}
	if path == "/device/deny" && r.Method == http.MethodPost {
		err = a.store.DenyDeviceAuthorization(r.Context(), d.ID)
		if err != nil {
			deviceError(w, err)
		} else {
			writeJSON(w, 200, map[string]string{"status": "denied"})
		}
		return
	}
	if path != "/device/confirm" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if d.Status != "pending" || len(in.Tools) == 0 || len(in.Tools) > 256 || in.TTLSeconds <= 0 || in.TTLSeconds > d.TTLSeconds || (d.Request.EndpointID != "" && in.EndpointID != d.Request.EndpointID) || (in.AllowWrite && !d.Request.AllowWriteRequests) {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	for _, tool := range in.Tools {
		if len(d.Request.AllowedTools) > 0 && !slices.Contains(d.Request.AllowedTools, tool) {
			deviceError(w, configstore.DeviceDenied)
			return
		}
	}
	g := d.Request
	g.Issuer, g.Subject, g.Resource = rt.cfg.Auth.Issuer, p.ID, rt.cfg.Server.PublicURL
	g.EndpointID, g.AllowedTools, g.AllowWriteRequests = in.EndpointID, in.Tools, in.AllowWrite
	// Requested resource restrictions cannot be replaced by browser consent.
	if len(in.ResourceRules) > 0 && string(in.ResourceRules) != "null" {
		var extra []json.RawMessage
		if json.Unmarshal(in.ResourceRules, &extra) != nil || len(extra) > 32 {
			deviceError(w, configstore.DeviceDenied)
			return
		}

		if err = json.Unmarshal(in.ResourceRules, &g.ResourceRules); err != nil || len(d.Request.ResourceRules) > 0 {
			deviceError(w, configstore.DeviceDenied)
			return
		}
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	if a.currentRuntime() != rt {
		deviceError(w, configstore.DeviceDenied)
		return
	}
	if err = rt.hub.PrepareClientGrant(&g, info.Scopes, p); err != nil {
		writeGrantError(w, err)
		return
	}
	for _, scope := range g.AllowedScopes {
		if !slices.Contains(d.Scopes, scope) {
			deviceError(w, configstore.DeviceDenied)
			return
		}
	}
	g.EndpointUID, g.EndpointPolicy, err = a.store.ClientEndpointPolicy(r.Context(), g.EndpointID)
	if err != nil {
		writeGrantError(w, err)
		return
	}
	g, err = a.store.ConfirmDeviceAuthorization(r.Context(), d.ID, g, *p, a.sso.Connections().Revision, time.Duration(in.TTLSeconds)*time.Second, rt.cfg.ClientAuthorization.GrantTTL())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "approved", "grant": g})
}
