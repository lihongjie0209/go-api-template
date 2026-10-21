package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/identity"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	"github.com/lihongjie0209/go-api-template/internal/tenant"
	"github.com/stretchr/testify/require"
)

type contactPagerStub struct {
	input  identity.PageInput
	result pagination.Result[identity.User]
	err    error
	calls  int
}

func (s *contactPagerStub) Page(_ context.Context, input identity.PageInput) (pagination.Result[identity.User], error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func TestMemberContactSearcherRejectsEmptyContactFiltersBeforeIdentityLookup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		emails []string
		phones []string
	}{
		{name: "both omitted"},
		{name: "blank email", emails: []string{" "}},
		{name: "blank phone", phones: []string{" "}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stub := &contactPagerStub{result: pagination.Result[identity.User]{Items: []identity.User{{ID: "unrelated-user"}}, Total: 1}}
			searcher := &memberContactSearcher{users: stub}
			_, err := searcher.SearchContactUserIDs(t.Context(), test.emails, test.phones)
			require.ErrorIs(t, err, tenant.ErrInvalid)
			require.Zero(t, stub.calls)
		})
	}
}

func TestMemberContactSearcherUsesExactBoundedIdentityFilters(t *testing.T) {
	stub := &contactPagerStub{result: pagination.Result[identity.User]{Items: []identity.User{{ID: "user-1"}}, Total: 1}}
	searcher := &memberContactSearcher{users: stub}
	ids, err := searcher.SearchContactUserIDs(t.Context(), []string{"alice@example.com"}, []string{"13800000000"})
	require.NoError(t, err)
	require.Equal(t, []string{"user-1"}, ids)
	require.Equal(t, []string{"alice@example.com"}, stub.input.Emails)
	require.Equal(t, []string{"13800000000"}, stub.input.Phones)
	require.Equal(t, 200, stub.input.PageSize)
}

func TestMemberContactSearcherRejectsPartialCandidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		result pagination.Result[identity.User]
		want   error
	}{
		{name: "too many candidates", result: pagination.Result[identity.User]{Total: 201}, want: tenant.ErrContactFilterTooBroad},
		{name: "count and item drift", result: pagination.Result[identity.User]{Total: 1}, want: tenant.ErrIdentityUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			searcher := &memberContactSearcher{users: &contactPagerStub{result: test.result}}
			_, err := searcher.SearchContactUserIDs(t.Context(), []string{"a@example.com"}, nil)
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestMemberContactSearcherPropagatesIdentityFailure(t *testing.T) {
	upstream := errors.New("identity unavailable")
	searcher := &memberContactSearcher{users: &contactPagerStub{err: upstream}}
	_, err := searcher.SearchContactUserIDs(t.Context(), []string{"a@example.com"}, nil)
	require.ErrorIs(t, err, upstream)
}
