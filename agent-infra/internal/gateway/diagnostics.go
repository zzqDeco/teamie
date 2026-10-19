package gateway

import (
	"context"
	"strings"

	"go.uber.org/zap"
	"teamie/backend/pkg/observability"
)

func gatewayOperation(ctx context.Context, id, session, execution string, streaming bool) (context.Context, *observability.Operation) {
	run, attempt, _ := strings.Cut(execution, ":")
	ctx, op := observability.Begin(ctx, "model-provider-call", zap.String("custom.agent.id", observability.ID(id)), zap.String("custom.runtime.session_id", observability.ID(session)), zap.String("custom.runtime.run_id", observability.ID(run)), zap.String("custom.runtime.attempt_id", observability.ID(attempt)), zap.Bool("custom.model.streaming", streaming))
	ctx = observability.WithHTTPAttempts(ctx)
	observability.L(ctx).Info("Model provider request started", zap.String("event.action", "model-provider-call-started"))
	return ctx, op
}
