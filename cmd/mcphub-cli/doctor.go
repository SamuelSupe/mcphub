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
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/client"
)

func runDoctor(args []string) error {
	flags := flag.NewFlagSet("mcphub-cli doctor", flag.ContinueOnError)
	profile := flags.String("profile", "default", "MCP credential profile")
	id := flags.String("client", "", "paired client instance to diagnose")
	jsonOutput := flags.Bool("json", false, "output a structured report without credentials")
	timeout := flags.Duration("timeout", 15*time.Second, "total diagnostic deadline (1s to 2m)")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *timeout < time.Second || *timeout > 2*time.Minute {
		return errors.New("doctor accepts flags only; --timeout must be between 1s and 2m")
	}
	store, err := client.DefaultStore()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	report := client.Doctor(ctx, store, *profile, client.DoctorOptions{ClientID: *id, Timeout: *timeout})
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Printf("MCPHub diagnostics: profile=%s client=%s\n", report.Profile, report.ClientID)
		for _, check := range report.Checks {
			fmt.Printf("[%s] %s: %s\n", check.Status, check.Code, check.Message)
			if check.Action != "" {
				fmt.Printf("  Next: %s\n", check.Action)
			}
		}
	}
	if !report.Healthy {
		return errors.New("diagnostics found a blocking problem; follow the reported next steps")
	}
	return nil
}
