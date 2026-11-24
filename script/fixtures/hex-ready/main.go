// Probe the same local engine used by the selected tool before a live test.
// Create and cancel an empty stream, leaving the slot available for the press.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"mimac.local/mimac/internal/adapters/hex"
)

func check() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := hex.NewClient(ctx, home)
	if err != nil {
		return err
	}
	stream, err := client.CreateStream(ctx)
	if err != nil {
		return err
	}
	cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	return stream.Cancel(cleanup)
}

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, "Hex stream preflight:", err)
		os.Exit(1)
	}
	fmt.Println("Hex accepted and released the empty stream")
}
