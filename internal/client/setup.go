package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type SetupOptions struct {
	ServerURL, ClientID, Command string
	CallbackPort                 int
	Input                        io.Reader
	Output, ConfigOutput         io.Writer
	HTTPClient                   *http.Client
	OpenBrowser                  func(string) error
}

// Setup prompts before consent and prints only credential-free configuration to
// ConfigOutput. It never modifies an application's existing configuration file.
func Setup(ctx context.Context, store *Store, name string, opts SetupOptions) error {
	if name == "" {
		name = "default"
	}
	if opts.Input == nil || opts.Output == nil || opts.ConfigOutput == nil {
		return errors.New("setup requires input, progress and configuration streams")
	}
	reader := bufio.NewScanner(opts.Input)
	reader.Buffer(make([]byte, 4096), 16<<10)
	ask := func(prompt, defaultValue string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprint(opts.Output, prompt)
		if defaultValue != "" {
			fmt.Fprintf(opts.Output, " [%s]", defaultValue)
		}
		fmt.Fprint(opts.Output, ": ")
		// A terminal read may remain blocked even after its descriptor is closed.
		// Let cancellation return; callers in long-lived processes own closing Input.
		scanned := make(chan bool, 1)
		go func() { scanned <- reader.Scan() }()
		var available bool
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case available = <-scanned:
		}
		if !available {
			if err := reader.Err(); err != nil {
				return "", err
			}
			return "", errors.New("setup cancelled: input ended")
		}
		value := strings.TrimSpace(reader.Text())
		if value == "" {
			value = defaultValue
		}
		return value, nil
	}
	status, err := store.Status(ctx, name)
	if err != nil {
		return err
	}
	if status.Admin {
		return errors.New("use a separate MCP user profile for setup")
	}
	if status.LoggedIn && ((opts.ServerURL != "" && opts.ServerURL != status.ServerURL) || (opts.ClientID != "" && opts.ClientID != status.ClientID)) {
		return errors.New("profile points to a different server/client; use a new profile or explicitly log in again")
	}
	if !status.LoggedIn || (!status.ExpiresAt.IsZero() && !status.ExpiresAt.After(time.Now()) && !status.CanRefresh) {
		server, id := opts.ServerURL, opts.ClientID
		if server == "" {
			server, err = ask("MCPHub HTTPS URL", status.ServerURL)
			if err != nil {
				return err
			}
		}
		if id == "" {
			id, err = ask("Registered public OIDC client ID", status.ClientID)
			if err != nil {
				return err
			}
		}
		if err := Login(ctx, store, LoginOptions{Profile: name, ServerURL: server, ClientID: id, CallbackPort: opts.CallbackPort, Output: opts.Output, HTTPClient: opts.HTTPClient, OpenBrowser: opts.OpenBrowser}); err != nil {
			return err
		}
	}
	a, err := newAuthorizationClient(ctx, store, name, opts.HTTPClient)
	if err != nil {
		return err
	}
	if a.discovery.OptionsURL == "" {
		return errors.New("server does not support setup discovery; use client add or upgrade MCPHub")
	}
	var catalog struct {
		Endpoints []config.ClientEndpointOption `json:"endpoints"`
	}
	if err := a.request(ctx, http.MethodGet, a.discovery.OptionsURL, "", nil, &catalog); err != nil {
		return err
	}
	if len(catalog.Endpoints) == 0 {
		return errors.New("no published tools match this login; ask an administrator to check publication and scopes")
	}
	for i, e := range catalog.Endpoints {
		fmt.Fprintf(opts.Output, "%d. %s (%d tools)\n", i+1, e.ID, len(e.Tools))
	}
	choice, err := ask("Choose endpoint number", "")
	if err != nil {
		return err
	}
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > len(catalog.Endpoints) {
		return errors.New("choose one listed endpoint number")
	}
	endpoint := catalog.Endpoints[index-1]
	for i, tool := range endpoint.Tools {
		fmt.Fprintf(opts.Output, "%d. %s [%s] scopes=%s\n", i+1, tool.Name, tool.Effect, strings.Join(tool.RequiredScopes, ","))
		if len(tool.ResourceRules) > 0 {
			data, _ := json.Marshal(tool.ResourceRules)
			fmt.Fprintf(opts.Output, "   Required resource limits: %s\n", data)
		}
	}
	choice, err = ask("Choose tool numbers (comma separated; no default)", "")
	if err != nil {
		return err
	}
	selected, err := setupToolSelection(choice, len(endpoint.Tools))
	if err != nil {
		return err
	}
	clientOpts := ClientOptions{Endpoint: endpoint.ID, Capabilities: configstore.GrantCapabilities{Tools: true}, HTTPClient: opts.HTTPClient, OpenBrowser: opts.OpenBrowser, Output: opts.Output}
	for _, index := range selected {
		tool := endpoint.Tools[index]
		clientOpts.Tools = append(clientOpts.Tools, tool.Name)
		clientOpts.Scopes = append(clientOpts.Scopes, tool.RequiredScopes...)
		clientOpts.AllowWrite = clientOpts.AllowWrite || tool.Effect != "read"
	}
	slices.Sort(clientOpts.Scopes)
	clientOpts.Scopes = slices.Compact(clientOpts.Scopes)
	if clientOpts.AllowWrite {
		answer, err := ask("Selected tools require per-operation approval. Allow requesting writes? Type yes", "no")
		if err != nil {
			return err
		}
		if answer != "yes" {
			return errors.New("setup cancelled; no client authorization was created")
		}
	}
	fmt.Fprintln(opts.Output, "Additional resource limits apply to every selected tool. Leave blank to keep the existing server limits.")
	for {
		value, err := ask("Additional resource limit /json/pointer=value (blank to finish)", "")
		if err != nil {
			return err
		}
		if value == "" {
			break
		}
		pointer, allowed, ok := strings.Cut(value, "=")
		if !ok || !strings.HasPrefix(pointer, "/") || allowed == "" {
			return errors.New("resource limit must be /json/pointer=value")
		}
		found := false
		for i := range clientOpts.ResourceRules {
			if clientOpts.ResourceRules[i].Argument == pointer {
				clientOpts.ResourceRules[i].AllowedValues = append(clientOpts.ResourceRules[i].AllowedValues, allowed)
				found = true
			}
		}
		if !found {
			clientOpts.ResourceRules = append(clientOpts.ResourceRules, config.ResourceRule{Argument: pointer, AllowedValues: []string{allowed}})
		}
	}
	maximum := a.discovery.MaxGrantTTL / 60
	if maximum < 1 || maximum > 480 {
		return errors.New("server returned an invalid authorization duration")
	}
	value, err := ask(fmt.Sprintf("Authorization duration in minutes (1-%d)", maximum), strconv.FormatInt(min(maximum, 60), 10))
	if err != nil {
		return err
	}
	minutes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || minutes < 1 || minutes > maximum {
		return errors.New("authorization duration exceeds server limits")
	}
	clientOpts.TTLSeconds = minutes * 60
	clientOpts.Name, err = ask("Client entry name", name+"-"+endpoint.ID)
	if err != nil {
		return err
	}
	format, err := ask("Configuration: 1 = portable mcpServers JSON; 2 = VS Code servers JSON", "1")
	if err != nil {
		return err
	}
	if format != "1" && format != "2" {
		return errors.New("choose configuration format 1 or 2")
	}
	fmt.Fprintf(opts.Output, "Review: endpoint=%s tools=%s scopes=%s writes=%t duration=%dm\n", endpoint.ID, strings.Join(clientOpts.Tools, ","), strings.Join(clientOpts.Scopes, ","), clientOpts.AllowWrite, minutes)
	answer, err := ask("Open browser to authorize this client? Type yes", "no")
	if err != nil {
		return err
	}
	if answer != "yes" {
		return errors.New("setup cancelled; no client authorization was created")
	}
	g, err := AuthorizeClient(ctx, store, name, clientOpts)
	if err != nil {
		return err
	}
	command := opts.Command
	if command == "" {
		command = "mcphub-cli"
	}
	directory, err := filepath.Abs(store.Dir)
	if err != nil {
		return err
	}
	entry := map[string]any{"command": command, "args": []string{"connect", "--profile", name, "--client", g.ClientID}, "env": map[string]string{"MCPHUB_HOME": directory}}
	key := "mcpServers"
	if format == "2" {
		key = "servers"
		entry["type"] = "stdio"
	}
	encoder := json.NewEncoder(opts.ConfigOutput)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{key: map[string]any{g.ClientName: entry}}); err != nil {
		return err
	}
	report := Doctor(ctx, store, name, DoctorOptions{ClientID: g.ClientID, HTTPClient: opts.HTTPClient, Timeout: 15 * time.Second})
	for _, check := range report.Checks {
		fmt.Fprintf(opts.Output, "[%s] %s: %s\n", check.Status, check.Code, check.Message)
		if check.Action != "" {
			fmt.Fprintln(opts.Output, "  Next:", check.Action)
		}
	}
	if !report.Healthy {
		return errors.New("client authorized and configuration printed, but diagnostics failed; fix the reported problem before use")
	}
	fmt.Fprintln(opts.Output, "Setup verified. Merge the printed entry into your MCP configuration and restart that connection. No tools were executed.")
	return nil
}

func setupToolSelection(value string, count int) ([]int, error) {
	var selected []int
	for _, part := range strings.Split(value, ",") {
		index, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || index < 1 || index > count {
			return nil, errors.New("select at least one listed tool number; empty selection never grants all tools")
		}
		if !slices.Contains(selected, index-1) {
			selected = append(selected, index-1)
		}
	}
	return selected, nil
}
