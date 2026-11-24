//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"atoman/internal/platform/papersruntime"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "Papers service: expected fixed role")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := papersruntime.Run(ctx, os.Args[1]); err != nil {
		// Do not log protected configuration, database connection strings,
		// untrusted compile data, or storage-provider responses.
		fmt.Fprintln(os.Stderr, "Papers service startup or execution verification failed")
		os.Exit(1)
	}
}
