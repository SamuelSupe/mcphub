package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SamuelSupe/mcphub/internal/client"
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
		return fmt.Errorf("usage: mcphub-cli <login|connect|status|logout|admin> [flags]")
	}
	command := args[1]
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
		return client.Connect(ctx, store, *profile, client.ConnectOptions{})
	case "logout":
		if err := store.Logout(ctx, *profile); err != nil {
			return err
		}
		fmt.Printf("Cleared local credentials for profile %q. The identity-service browser session is unchanged.\n", *profile)
	case "status":
		status, err := store.Status(ctx, *profile)
		if err != nil {
			return err
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
		fmt.Printf("Profile: %s\nServer: %s\nCached access token: %s\nExpires: %s\nCan refresh: %t\nThis is local cache status; authorization has not been checked online.\n", *profile, status.ServerURL, state, status.ExpiresAt.Format(time.RFC3339), status.CanRefresh)
	}
	return nil
}
