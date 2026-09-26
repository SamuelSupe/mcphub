package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/SamuelSupe/mcphub/v2/internal/client"
)

func runSetup(args []string) error {
	flags := flag.NewFlagSet("mcphub-cli setup", flag.ContinueOnError)
	profile := flags.String("profile", "default", "MCP credential profile")
	server := flags.String("server", "", "MCPHub HTTPS URL for first login")
	id := flags.String("client-id", "", "registered public OIDC client ID for first login")
	port := flags.Int("callback-port", 0, "registered loopback callback port (0: random)")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("setup accepts flags only")
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("callback-port must be between 0 and 65535")
	}
	store, err := client.DefaultStore()
	if err != nil {
		return err
	}
	command, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return client.Setup(ctx, store, *profile, client.SetupOptions{ServerURL: *server, ClientID: *id, CallbackPort: *port, Command: command, Input: os.Stdin, Output: os.Stderr, ConfigOutput: os.Stdout})
}
