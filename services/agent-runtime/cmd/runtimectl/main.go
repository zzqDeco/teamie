// runtimectl is an operator command. It has no HTTP route and never runs migrations.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"os"
	"os/signal"
	"syscall"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/infra/pg"
)

func main() {
	flags := flag.NewFlagSet("runtimectl acceptance", flag.ExitOnError)
	agent := flags.String("agent", "", "Agent ID")
	message := flags.String("message", "", "trigger message ID")
	if len(os.Args) < 2 || os.Args[1] != "acceptance" {
		fmt.Fprintln(os.Stderr, "usage: runtimectl acceptance --agent ID --message ID")
		os.Exit(2)
	}
	_ = flags.Parse(os.Args[2:])
	if _, e := uuid.Parse(*agent); e != nil {
		os.Exit(2)
	}
	if _, e := uuid.Parse(*message); e != nil {
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	pool, err := postgres.Open(ctx, os.Getenv("DATABASE_URL"), postgres.WithRuntimeParam("default_transaction_read_only", "on"))
	if err != nil {
		fail()
	}
	defer pool.Close()
	out, err := pg.NewFromPool(pool).ReadAcceptanceEvidence(ctx, *agent, *message)
	if err != nil {
		fail()
	}
	if json.NewEncoder(os.Stdout).Encode(out) != nil {
		fail()
	}
}
func fail() { fmt.Fprintln(os.Stderr, "acceptance evidence unavailable"); os.Exit(1) }
