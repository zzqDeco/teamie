// runtimectl is an operator command. It has no HTTP route and never runs migrations.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"teamie/pkg/postgres"
	"teamie/services/agent-runtime/internal/domain"
	"teamie/services/agent-runtime/internal/infra/pg"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "acceptance":
		runAcceptance(os.Args[2:])
	case "orphan-runs":
		runOrphanRuns(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: runtimectl <acceptance|orphan-runs> [flags]")
	os.Exit(2)
}

func runAcceptance(args []string) {
	flags := flag.NewFlagSet("runtimectl acceptance", flag.ExitOnError)
	agent := flags.String("agent", "", "Agent ID")
	message := flags.String("message", "", "trigger message ID")
	_ = flags.Parse(args)
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
		fail("acceptance evidence unavailable")
	}
	defer pool.Close()
	out, err := pg.NewFromPool(pool).ReadAcceptanceEvidence(ctx, *agent, *message)
	if err != nil {
		fail("acceptance evidence unavailable")
	}
	if json.NewEncoder(os.Stdout).Encode(out) != nil {
		fail("acceptance evidence unavailable")
	}
}

func runOrphanRuns(args []string) {
	flags := flag.NewFlagSet("runtimectl orphan-runs", flag.ExitOnError)
	mode := flags.String("mode", "audit", "Operation mode: audit or reconcile")
	agent := flags.String("agent", "", "Optional Agent UUID filter")
	_ = flags.Parse(args)
	if *mode != "audit" && *mode != "reconcile" {
		fmt.Fprintln(os.Stderr, "invalid --mode: expected audit or reconcile")
		os.Exit(2)
	}
	if *agent != "" {
		if _, e := uuid.Parse(*agent); e != nil {
			fmt.Fprintln(os.Stderr, "invalid --agent UUID")
			os.Exit(2)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	report, opened, err := executeOrphanRuns(ctx, os.Getenv("DATABASE_URL"), *mode, *agent)
	if err != nil {
		if !opened {
			fail("orphan-runs inspection unavailable")
		}
		fail("orphan-runs operation failed")
	}
	if json.NewEncoder(os.Stdout).Encode(report) != nil {
		fail("orphan-runs output encode failed")
	}
}

func executeOrphanRuns(ctx context.Context, dsn, mode, agent string) (*domain.OrphanRunReport, bool, error) {
	if mode == "audit" {
		pool, err := postgres.Open(ctx, dsn, postgres.WithRuntimeParam("default_transaction_read_only", "on"))
		if err != nil {
			return nil, false, err
		}
		defer pool.Close()
		report, err := pg.NewFromPool(pool).AuditOrphanRuns(ctx, agent)
		return report, true, err
	}
	pool, err := pg.OpenPool(ctx, dsn, pg.RuntimeWriterSchemaSetting, pg.RuntimeSchemaVersion)
	if err != nil {
		return nil, false, err
	}
	defer pool.Close()
	if err := pg.ValidateRuntimeSchema(ctx, pool); err != nil {
		return nil, true, err
	}
	report, err := pg.NewFromPool(pool).ReconcileOrphanRuns(ctx, agent)
	return report, true, err
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
