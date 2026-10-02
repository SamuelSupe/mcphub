package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/SamuelSupe/mcphub/v2/internal/client"
)

func runPair(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mcpbridge pair <start|finish> [flags]")
	}
	flags := flag.NewFlagSet("mcpbridge pair "+args[0], flag.ContinueOnError)
	machine := flags.Bool("json", false, "print only public pairing status as JSON")
	var opts client.PairOptions
	var request string
	var wait bool
	switch args[0] {
	case "start":
		flags.StringVar(&opts.Server, "server", "", "HTTPS MCP endpoint")
		flags.StringVar(&opts.Profile, "profile", "default", "local credential profile")
		flags.StringVar(&opts.ClientID, "client-id", "mcpbridge", "public OAuth client ID")
		flags.StringVar(&opts.Name, "name", "", "display name of this Agent connection")
		flags.StringVar(&opts.Endpoint, "endpoint", "", "optional service ID (otherwise selected in browser)")
		flags.BoolVar(&opts.AllowWrite, "allow-write-requests", false, "request optional write access; explicit browser consent is required")
		flags.Int64Var(&opts.TTLSeconds, "ttl", 0, "maximum grant lifetime in seconds (at least 60; 0 defaults to 1 hour)")
		var scopes, tools scopeFlags
		flags.Var(&scopes, "scope", "requested OAuth scope (repeatable)")
		flags.Var(&tools, "tool", "restrict requested tool names (repeatable)")

		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		opts.Scopes, opts.Tools = scopes, tools
	case "finish":
		flags.StringVar(&request, "request", "", "request ID returned by pair start")
		flags.BoolVar(&wait, "wait", false, "wait with server-directed polling until pairing ends")
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
	default:
		return errors.New("pair supports start and finish")
	}
	if flags.NArg() != 0 {
		return errors.New("pair does not accept positional arguments")
	}
	store, err := client.DefaultStore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var status client.PairStatus
	if args[0] == "start" {
		status, err = client.PairStart(ctx, store, opts)
	} else {
		status, err = client.PairFinish(ctx, store, request, wait, nil)
	}
	if *machine {
		if err != nil && status.RequestID == "" {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "error", "next_step": err.Error()})
		} else {
			_ = json.NewEncoder(os.Stdout).Encode(status)
		}
	} else if status.RequestID != "" {
		fmt.Fprintf(os.Stdout, "Status: %s\nRequest: %s\nPairing code: %s\nOpen: %s\n%s\n", status.Status, status.RequestID, status.UserCode, status.VerificationURIComplete, status.NextStep)
	}
	if err != nil {
		return err
	}
	if args[0] == "finish" && status.Status != "ready" && status.Status != "pending_user" {
		return errors.New("pairing ended: " + status.Status)
	}
	return nil
}
