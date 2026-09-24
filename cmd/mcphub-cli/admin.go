package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/SamuelSupe/mcphub/internal/client"
)

func runAdmin(args []string) error {
	flags := flag.NewFlagSet("mcphub-cli admin", flag.ContinueOnError)
	profile := flags.String("profile", "default", "administrator credential profile")
	file := flags.String("file", "", "JSON request file; - reads stdin")
	etag := flags.String("if-match", "", "ETag from the current resource, including quotes")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: mcphub-cli admin [flags] <get|post|put|delete> /API-PATH\nExamples: get /overview; get /backends; get /tool-groups; get /events")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 2 {
		flags.Usage()
		return errors.New("admin requires a method and a path")
	}
	var body []byte
	if *file != "" {
		input := os.Stdin
		if *file != "-" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			input = f
		}
		var err error
		body, err = io.ReadAll(io.LimitReader(input, (6<<20)+1))
		if err != nil {
			return err
		}
	}
	store, err := client.DefaultStore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return client.AdminRequest(ctx, store, *profile, client.AdminRequestOptions{Method: strings.ToUpper(flags.Arg(0)), Path: flags.Arg(1), IfMatch: *etag, Body: body, Output: os.Stdout, Diagnostics: os.Stderr})
}
