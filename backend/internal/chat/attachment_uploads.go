package chat

import (
	"context"
)

func (s *Store) LookupAttachmentUpload(ctx context.Context, user, conversation, key string) (Attachment, error) {
	a, err := s.App(nil, "").LookupAttachmentUpload(ctx, user, conversation, key)
	return a, compatErr(err)
}
