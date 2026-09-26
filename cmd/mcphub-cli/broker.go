package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/client"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

func runBroker(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: mcphub-cli broker <run|status|stop>")
	}
	store, err := client.DefaultStore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "run":
		return client.RunBroker(ctx, store, nil)
	case "status", "stop":
		count, err := client.BrokerControl(ctx, store, args[0])
		if err != nil {
			return err
		}
		if args[0] == "stop" {
			fmt.Println("Broker stopped. Remote authorizations remain active; use logout to revoke them.")
		} else {
			fmt.Printf("Broker running; active local connections: %d\n", count)
		}
		return nil
	default:
		return fmt.Errorf("unknown broker command %q", args[0])
	}
}

func runClient(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mcphub-cli client <add|list|authorize|revoke> [flags]")
	}
	command := args[0]
	flags := flag.NewFlagSet("mcphub-cli client "+command, flag.ContinueOnError)
	profile := flags.String("profile", "default", "credential profile")
	id := flags.String("client", "", "paired client instance ID")
	name := flags.String("name", "", "display name for this client entry")
	endpoint := flags.String("endpoint", "", "MCPHub backend or HTTP tool group ID")
	ttl := flags.Duration("ttl", 0, "authorization duration (default: server maximum, up to 8h)")
	write := flags.Bool("allow-write-requests", false, "allow requesting writes; individual writes still require approval")
	prompts := flags.Bool("prompts", false, "allow endpoint prompts")
	resources := flags.Bool("resources", false, "allow endpoint resources")
	subscriptions := flags.Bool("subscriptions", false, "allow resource subscriptions (requires --resources)")
	var scopes, tools, resourceRules scopeFlags
	flags.Var(&scopes, "scope", "allowed OAuth scope (repeatable, exact match)")
	flags.Var(&tools, "tool", "original tool name (repeatable; omitted freezes currently eligible tools)")
	flags.Var(&resourceRules, "resource", "tool argument JSON Pointer and allowed value, e.g. /project=project-a (repeatable)")
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	store, err := client.DefaultStore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch command {
	case "list":
		values, online, err := client.ListClients(ctx, store, *profile, nil)
		if err != nil {
			return err
		}
		if online {
			fmt.Println("Server status checked online.")
		} else {
			fmt.Println("Offline: showing cached authorization records.")
		}
		for _, g := range values {
			printClientStatus(g, online)
		}
		return nil
	case "revoke":
		if *id == "" {
			return fmt.Errorf("--client is required")
		}
		if err := client.RevokeClient(ctx, store, *profile, *id, nil); err != nil {
			return err
		}
		fmt.Println("Client authorization revoked.")
		return nil
	case "add", "authorize":
		if command == "authorize" && *id == "" {
			return fmt.Errorf("--client is required")
		}
		if command == "add" && *id != "" {
			return fmt.Errorf("client add creates a new instance; omit --client")
		}
		var rules []config.ResourceRule
		for _, value := range resourceRules {
			pointer, allowed, ok := strings.Cut(value, "=")
			if !ok || pointer == "" || allowed == "" {
				return fmt.Errorf("--resource expects /argument=value")
			}
			found := false
			for i := range rules {
				if rules[i].Argument == pointer {
					rules[i].AllowedValues = append(rules[i].AllowedValues, allowed)
					found = true
				}
			}
			if !found {
				rules = append(rules, config.ResourceRule{Argument: pointer, AllowedValues: []string{allowed}})
			}
		}
		var changed []string
		flags.Visit(func(f *flag.Flag) { changed = append(changed, f.Name) })
		g, err := client.AuthorizeClient(ctx, store, *profile, client.ClientOptions{Changed: changed, ID: *id, Name: *name, Endpoint: *endpoint, Scopes: scopes, Tools: tools, ResourceRules: rules, AllowWrite: *write, TTLSeconds: int64(*ttl / time.Second), Capabilities: configstore.GrantCapabilities{Tools: true, Prompts: *prompts, Resources: *resources, Subscriptions: *subscriptions}, Output: os.Stderr})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Authorized %s until %s. MCP configuration:\n", g.ClientID, g.ExpiresAt.Format(time.RFC3339))
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{"mcpServers": map[string]any{g.ClientName: map[string]any{"command": "mcphub-cli", "args": []string{"connect", "--profile", *profile, "--client", g.ClientID}}}})
	default:
		return fmt.Errorf("unknown client command %q", command)
	}
}

func printClientStatus(g client.ClientStatus, online bool) {
	fmt.Printf("%s  %s  endpoint=%s  status=%s  granted_scopes=%s  expires=%s\n", g.ClientID, g.ClientName, g.EndpointID, g.Status, strings.Join(g.AllowedScopes, ","), g.ExpiresAt.Format(time.RFC3339))
	if online {
		fmt.Printf("  Effective scopes: %s; authorization error: %s\n", strings.Join(g.EffectiveScopes, ","), g.AuthorizationError)
	}
}
