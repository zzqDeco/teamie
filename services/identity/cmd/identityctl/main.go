package main

import (
	"context"
	"os"

	"teamie/services/identity/internal/adapter/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
