package main

import (
	"context"
	"os"

	"github.com/h0ek/gotify-vps-agent/internal/cli"
)

func main() {
	application := cli.New(os.Stdin, os.Stdout, os.Stderr)
	os.Exit(application.Run(context.Background(), os.Args[1:]))
}
