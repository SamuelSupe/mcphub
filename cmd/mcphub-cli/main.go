package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/client"
)

type scopeFlags []string

func (s *scopeFlags) String() string         { return fmt.Sprint([]string(*s)) }
func (s *scopeFlags) Set(value string) error { *s = append(*s, value); return nil }

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: mcphub-cli <setup|login|connect|status|logout|client|broker|doctor|admin> [flags]")
	}
	command := args[1]
	if command == "setup" {
		return runSetup(args[2:])
	}
	if command == "doctor" {
		return runDoctor(args[2:])
	}
	if command == "client" {
		return runClient(args[2:])
	}
	if command == "broker" {
		return runBroker(args[2:])
	}
	if command == "admin" {
		return runAdmin(args[2:])
	}
	switch command {
	case "login", "connect", "status", "logout":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	flags := flag.NewFlagSet("mcphub-cli "+command, flag.ContinueOnError)
	profile := flags.String("profile", "default", "local credential profile")
	var server, clientID *string
	var clientEntry *string
	if command == "connect" {
		clientEntry = flags.String("client", "", "paired client instance; connects through the local broker")
	}
	var callbackPort *int
	var admin *bool
	var scopes scopeFlags
	if command == "login" {
		admin = flags.Bool("admin", false, "log in to the remote administration origin")
		server = flags.String("server", "", "HTTPS MCP endpoint or admin origin with --admin (reuses this profile when omitted)")
		clientID = flags.String("client-id", "", "preregistered public OAuth client ID")
		callbackPort = flags.Int("callback-port", 0, "loopback callback port (0 chooses an available port)")
		flags.Var(&scopes, "scope", "OAuth scope to request (repeatable)")
	}
	if err := flags.Parse(args[2:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", command)
	}
	store, err := client.DefaultStore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch command {
	case "login":
		if *server == "" || *clientID == "" {
			status, err := store.Status(ctx, *profile)
			if err != nil {
				return err
			}
			if status.Admin {
				*admin = true
			}
			if *server == "" {
				*server = status.ServerURL
			}
			if *clientID == "" {
				*clientID = status.ClientID
			}
		}
		return client.Login(ctx, store, client.LoginOptions{Admin: *admin, ServerURL: *server, ClientID: *clientID, Profile: *profile, Scopes: scopes, CallbackPort: *callbackPort, Output: os.Stderr})
	case "connect":
		if *clientEntry != "" {
			return client.ConnectBroker(ctx, store, *profile, *clientEntry, client.ConnectOptions{})
		}
		return client.Connect(ctx, store, *profile, client.ConnectOptions{})
	case "logout":
		revoked, err := client.Logout(ctx, store, *profile, nil)
		if err != nil {
			return err
		}
		fmt.Printf("Cleared local credentials for profile %q.\n", *profile)
		if revoked {
			fmt.Println("Remote broker session revoked, if present.")
		} else {
			fmt.Println("Local logout completed; remote revocation is unconfirmed. Revoke the old session in the MCPHub client portal.")
		}
	case "status":
		status, err := store.Status(ctx, *profile)
		if err != nil {
			return err
		}
		if status.PendingRevocations > 0 {
			fmt.Printf("Remote session revocations pending: %d; review the client authorization portal.\n", status.PendingRevocations)
		}
		if status.BrokerSession != "" {
			fmt.Printf("Cached broker session: %s\nPaired clients: %d\n", status.BrokerSession, status.ClientCount)
		}
		if !status.LoggedIn {
			fmt.Printf("Profile %q: not logged in\n", *profile)
			return nil
		}
		kind := "MCP connector"
		if status.Admin {
			kind = "administrator"
		}
		fmt.Printf("Purpose: %s\n", kind)
		state := "unexpired"
		if !time.Now().Before(status.ExpiresAt) {
			state = "expired"
		}
		fmt.Printf("Profile: %s\nServer: %s\nCached access token: %s\nExpires: %s\nCan refresh: %t\nThese login fields describe the local credential cache.\n", *profile, status.ServerURL, state, status.ExpiresAt.Format(time.RFC3339), status.CanRefresh)
		if status.BrokerSession != "" {
			values, online, err := client.ListClients(ctx, store, *profile, nil)
			if err != nil {
				return err
			}
			fmt.Printf("Client authorization server reachable: %t (offline results are cached)\n", online)
			for _, value := range values {
				printClientStatus(value, online)
			}
		}
	}
	return nil
}
