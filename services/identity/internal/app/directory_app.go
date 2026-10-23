package app

import (
	"context"

	"teamie/services/identity/internal/domain"
)

// DirectoryApp is an alias for Service focusing on principal directory queries and profile updates.
type DirectoryApp = Service

// GetUsers returns up to 1000 principals by ID.
func (s *Service) GetUsers(ctx context.Context, ids []string) ([]domain.User, error) {
	if len(ids) > 1000 {
		return nil, domain.ErrTooManyUsers()
	}
	return s.Users.FindByIDs(ctx, ids)
}

// Profile updates a principal's display name.
func (s *Service) Profile(ctx context.Context, id, name string) (domain.User, error) {
	name, err := domain.ValidateDisplayName(name)
	if err != nil {
		return domain.User{}, err
	}
	return s.Users.UpdateDisplayName(ctx, id, name)
}
