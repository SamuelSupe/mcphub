package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type DoctorOptions struct {
	ClientID   string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type DiagnosticCheck struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Action  string `json:"action,omitempty"`
}

type DoctorReport struct {
	Profile         string            `json:"profile"`
	ClientID        string            `json:"client_id,omitempty"`
	Server          string            `json:"server,omitempty"`
	Endpoint        string            `json:"endpoint,omitempty"`
	Healthy         bool              `json:"healthy"`
	Checks          []DiagnosticCheck `json:"checks"`
	EffectiveScopes []string          `json:"effective_scopes,omitempty"`
	Tools           []string          `json:"tools,omitempty"`
	MoreTools       bool              `json:"more_tools"`
}

// Doctor uses the connector's credential path and read-only MCP initialization
// and discovery. It may refresh a token, but never opens a browser, starts a
// Broker, creates an authorization, or calls a tool.
func Doctor(ctx context.Context, store *Store, name string, opts DoctorOptions) DoctorReport {
	if name == "" {
		name = "default"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	r := DoctorReport{Profile: name, ClientID: opts.ClientID, Healthy: true, Checks: []DiagnosticCheck{}}
	add := func(code, status, message, action string) {
		r.Checks = append(r.Checks, DiagnosticCheck{code, status, message, action})
		if status == "fail" {
			r.Healthy = false
		}
	}
	login := fmt.Sprintf("mcphub-cli login --profile %s", name)
	status, err := store.Status(ctx, name)
	if err != nil {
		add("credential_store", "fail", "Credential files could not be read securely.", "Check the profile name and private directory ownership/permissions.")
		return r
	}
	add("credential_store", "pass", "Private credential storage is accessible.", "")
	r.Server = status.ServerURL
	if !status.LoggedIn {
		add("login", "fail", "This profile has no saved login.", login+" --server HTTPS_MCP_URL --client-id REGISTERED_CLIENT_ID")
		return r
	}
	if status.Admin {
		add("profile_kind", "fail", "This is an administrator profile.", "Use a separate MCP user profile for connect and doctor.")
		return r
	}
	if status.PendingRevocations > 0 {
		add("revocation", "warn", "Some previous remote session revocations are unconfirmed.", "Revoke those sessions in the user authorization portal.")
	}
	if !status.CanRefresh {
		add("renewal", "warn", "No refresh token; log in again when the access token expires.", login)
	}
	var entry localClient
	var bound clientCredentials
	if opts.ClientID != "" {
		entry, err = store.localClient(name, opts.ClientID)
		if err == nil {
			err = store.locked(ctx, name, func() error {
				p, err := store.load(name)
				if err != nil {
					return err
				}
				if p.Broker == nil {
					return ErrLoginRequired
				}
				bound = p.Broker.Clients[opts.ClientID]
				if bound.IPCHash != configstore.SecretHash(entry.Secret) || bound.Credential == "" {
					return errors.New("client pairing does not match this profile")
				}
				return nil
			})
		}
		if err != nil {
			add("client_pairing", "fail", "Client pairing is missing or does not belong to this profile.", "Run mcphub-cli client list, then client add or client authorize for this profile.")
			return r
		}
		r.Endpoint = bound.Grant.EndpointID
		add("client_pairing", "pass", "Client entry and private IPC credential match this profile.", "")
		if !time.Now().Before(bound.Grant.ExpiresAt) {
			add("client_grant", "fail", "The client authorization has expired.", fmt.Sprintf("mcphub-cli client authorize --profile %s --client %s", name, opts.ClientID))
			return r
		}
	}
	c, hc, err := connectorHTTPClient(ctx, store, name, ConnectOptions{HTTPClient: opts.HTTPClient, clientID: opts.ClientID, grantID: bound.Grant.GrantID})
	if err != nil {
		add("token", "fail", publicError(err).Error(), login+"; check network access to the identity service.")
		return r
	}
	add("token", "pass", "An unexpired access token is available; refreshed when necessary.", "")
	if opts.ClientID != "" {
		a, authErr := newAuthorizationClient(ctx, store, name, opts.HTTPClient)
		var snapshot grantSnapshot
		if authErr == nil {
			snapshot, _, authErr = a.current(ctx, bound.Credential, "")
		}
		if authErr != nil {
			add("client_grant", "fail", publicError(authErr).Error(), fmt.Sprintf("Check connectivity, then mcphub-cli client authorize --profile %s --client %s if authorization has ended.", name, opts.ClientID))
			return r
		}
		r.EffectiveScopes = snapshot.EffectiveScopes
		add("client_grant", "pass", "The server verified the client authorization and current policy.", "")
		if slices.ContainsFunc(bound.Grant.AllowedScopes, func(scope string) bool { return !slices.Contains(snapshot.EffectiveScopes, scope) }) {
			add("effective_scopes", "fail", "The current token no longer covers all scopes in this client authorization.", "Log in with the required scopes or explicitly authorize a narrower client entry, then restart the MCP connection.")
			return r
		}
	}
	var transport mcp.Transport = &mcp.StreamableClientTransport{Endpoint: c.endpoint, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}
	if opts.ClientID != "" {
		probeCtx, endProbe := context.WithTimeout(ctx, time.Second)
		_, brokerErr := BrokerControl(probeCtx, store, "status")
		endProbe()
		if brokerErr == nil {
			conn, reader, _, err := ipcHandshake(ctx, store, brokerMessage{Operation: "connect", Profile: name, Client: opts.ClientID, Secret: entry.Secret})
			if err != nil {
				add("broker", "fail", "The running Broker rejected this client connection.", "Check broker status and client pairing; restart the MCP connection.")
				return r
			}
			defer conn.Close()
			transport = &mcp.IOTransport{Reader: &bufferedIPC{reader, conn}, Writer: conn}
			add("broker", "pass", "Connected through the running local Broker.", "")
		} else {
			add("broker", "warn", "Broker is stopped or unreachable; checking the remote connection directly.", "connect starts the Broker on demand. Inspect broker status/logs if a Broker should already be running.")
		}
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "mcphub-doctor", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	session, err := cli.Connect(ctx, transport, nil)
	if err != nil {
		add("mcp_handshake", "fail", publicError(err).Error(), "Check MCPHub readiness, HTTPS, token issuer/audience, and the selected client authorization.")
		return r
	}
	defer session.Close()
	add("mcp_handshake", "pass", "MCP initialization succeeded.", "")
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		add("tool_catalog", "fail", publicError(err).Error(), "Ask the administrator to check endpoint permissions and rate limits.")
		return r
	}
	for _, tool := range list.Tools {
		if tool != nil {
			r.Tools = append(r.Tools, tool.Name)
		}
	}
	r.MoreTools = list.NextCursor != ""
	if len(r.Tools) == 0 {
		add("tool_catalog", "warn", "No tools are visible on the first catalog page.", "Check publication, endpoint readiness, scopes, and whether a paired --client is required.")
	} else {
		add("tool_catalog", "pass", fmt.Sprintf("Discovered %d tools on the first catalog page. No tools were executed.", len(r.Tools)), "")
	}
	return r
}
