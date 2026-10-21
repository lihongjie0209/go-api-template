package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
)

type memberContactSearcher struct {
	users interface {
		Page(context.Context, identity.PageInput) (pagination.Result[identity.User], error)
	}
}

func newMemberContactSearcher(users *identity.Service) *memberContactSearcher {
	return &memberContactSearcher{users: users}
}

var _ tenant.MemberContactSearcher = (*memberContactSearcher)(nil)

func (s *memberContactSearcher) SearchContactUserIDs(ctx context.Context, emails, phones []string) ([]string, error) {
	if len(emails) == 0 && len(phones) == 0 {
		return nil, tenant.ErrInvalid
	}
	for _, value := range emails {
		if strings.TrimSpace(value) == "" {
			return nil, tenant.ErrInvalid
		}
	}
	for _, value := range phones {
		if strings.TrimSpace(value) == "" {
			return nil, tenant.ErrInvalid
		}
	}
	result, err := s.users.Page(ctx, identity.PageInput{
		Request: pagination.Request{Page: 1, PageSize: pagination.MaxPageSize},
		Emails:  emails,
		Phones:  phones,
	})
	if err != nil {
		if errors.Is(err, identity.ErrInvalid) {
			return nil, tenant.ErrInvalid
		}
		return nil, fmt.Errorf("search identity contacts: %w", err)
	}
	if result.Total > pagination.MaxPageSize {
		return nil, tenant.ErrContactFilterTooBroad
	}
	if int64(len(result.Items)) != result.Total {
		return nil, tenant.ErrIdentityUnavailable
	}
	ids := make([]string, len(result.Items))
	for i, user := range result.Items {
		ids[i] = user.ID
	}
	return ids, nil
}
