package authorization

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/lihongjie0209/go-api-template/internal/datapermission"
)

type PolicyRevisions struct {
	data *datapermission.RuntimeLoader
}

func NewPolicyRevisions(data *datapermission.RuntimeLoader) *PolicyRevisions {
	return &PolicyRevisions{data: data}
}

func (r *PolicyRevisions) Revision() string {
	if r == nil {
		return ""
	}
	dataRevision := ""
	if r.data != nil {
		dataRevision = r.data.Revision()
	}
	digest := sha256.Sum256([]byte(dataRevision))
	return hex.EncodeToString(digest[:16])
}
