package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
)

type PolicyRevisions struct {
	data   *datapermission.RuntimeLoader
	grants grantSnapshotter
}

type grantSnapshotter interface {
	Snapshot(context.Context, platformprincipal.Principal) (context.Context, string, error)
}

func NewPolicyRevisions(data *datapermission.RuntimeLoader, authorizer platformauthz.Authorizer) (*PolicyRevisions, error) {
	grants, ok := authorizer.(grantSnapshotter)
	if !ok || nilDependency(grants) {
		return nil, errors.New("authorization grant snapshot provider is required")
	}
	return &PolicyRevisions{data: data, grants: grants}, nil
}

// ForRequest pins an operation-grant snapshot and combines its digest with the
// published data-permission revision. A failed grant read cannot yield a hint.
func (r *PolicyRevisions) ForRequest(ctx context.Context, principal platformprincipal.Principal) (context.Context, string, error) {
	if r == nil || r.grants == nil {
		return ctx, "", platformauthz.ErrDecisionUnavailable
	}
	snapshotCtx, grantRevision, err := r.grants.Snapshot(ctx, principal)
	if err != nil {
		return ctx, "", fmt.Errorf("%w: %v", platformauthz.ErrDecisionUnavailable, err)
	}
	dataRevision := ""
	if r.data != nil {
		dataRevision = r.data.Revision()
	}
	digest := sha256.Sum256([]byte("capability/v2\x00" + grantRevision + "\x00" + dataRevision))
	return snapshotCtx, hex.EncodeToString(digest[:16]), nil
}
