package authorization

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
)

type PolicyRevisions struct {
	operation *pbac.RuntimeLoader
	data      *datapermission.RuntimeLoader
}

func NewPolicyRevisions(operation *pbac.RuntimeLoader, data *datapermission.RuntimeLoader) *PolicyRevisions {
	return &PolicyRevisions{operation: operation, data: data}
}

func (r *PolicyRevisions) Revision() string {
	if r == nil {
		return ""
	}
	operationRevision, dataRevision := "", ""
	if r.operation != nil {
		operationRevision = r.operation.Revision()
	}
	if r.data != nil {
		dataRevision = r.data.Revision()
	}
	digest := sha256.Sum256([]byte(operationRevision + "\x00" + dataRevision))
	return hex.EncodeToString(digest[:16])
}
