package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/SamuelSupe/mcphub/internal/app"
	"github.com/SamuelSupe/mcphub/internal/config"
)

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: mcphub <serve|validate> --config PATH")
	}
	switch args[1] {
	case "validate":
		return validateCommand(args[2:])
	case "serve":
		return serveCommand(args[2:])
	default:
		return fmt.Errorf("unknown command %q", args[1])
	}
}

func validateCommand(args []string) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	path := flags.String("config", "", "path to YAML configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("validate does not accept positional arguments")
	}
	if *path == "" {
		return fmt.Errorf("--config is required")
	}
	if _, err := config.Load(*path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "configuration valid")
	return nil
}

func serveCommand(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	path := flags.String("config", "", "path to YAML configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("serve does not accept positional arguments")
	}
	if *path == "" {
		return fmt.Errorf("--config is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	service, err := app.New(ctx, cfg, *path, logger)
	if err != nil {
		return err
	}
	defer service.Close()
	logger.Info("mcphub listening", "address", cfg.Server.Listen, "public_url", cfg.Server.PublicURL)
	return service.Run()
}
