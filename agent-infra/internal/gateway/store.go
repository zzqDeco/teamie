package gateway

import (
	"context"

	"teamie/agent-infra/internal/gateway/domain"
	"teamie/agent-infra/internal/gateway/infra/pg"
)

type Config = domain.Config
type Usage = domain.Usage
type Store = domain.ConfigStore
type PGStore = pg.PGStore

func Open(ctx context.Context, url string) (*PGStore, error) {
	return pg.Open(ctx, url)
}

func mergeSavedConfig(old, c Config, key *string, clear bool, crypto Cipher) (Config, error) {
	return domain.MergeSavedConfig(old, c, key, clear, crypto)
}
