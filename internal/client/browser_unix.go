//go:build !windows

package client

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

func openBrowser(rawURL string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, command, rawURL).Run()
}
