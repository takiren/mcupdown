// Command mcctld is the daemon that manages Minecraft server containers.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("mcctld started")
	<-ctx.Done()
	slog.Info("mcctld stopped")
}
