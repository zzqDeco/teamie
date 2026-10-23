package control

import (
	"context"

	"teamie/agent-control/internal/control/domain"
)

func intersect(selected []string, sets ...[]string) []string {
	return domain.Intersect(selected, sets...)
}

func (s *Store) Reconcile(ctx context.Context, id string) error {
	return s.App().Reconcile(ctx, id)
}

func (s *Store) Run(ctx context.Context) {
	s.App().Run(ctx)
}
