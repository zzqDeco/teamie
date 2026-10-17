package main

import (
	"context"
	"os"

	"teamie/backend/internal/identityctl"
)

func main() {
	os.Exit(identityctl.Run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
