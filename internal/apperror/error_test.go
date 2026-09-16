package apperror

import (
	"errors"
	"net/http"
	"testing"

	platformcode "github.com/lihongjie0209/microservice-platform-go/errorcode"
)

func TestApplicationCodesComeFromSharedRegistry(t *testing.T) {
	t.Parallel()
	for _, code := range []int{
		CodeOK, CodeInvalidArgument, CodeNotFound, CodeRequestTimeout,
		CodeTooManyRequests, CodeUnauthorized, CodeForbidden, CodeConflict,
		CodeRequestInProgress, CodeInternal, CodeDependencyUnavailable,
		CodeAuthorizationUnavailable, CodePermissionPolicyMissing,
	} {
		if !platformcode.Code(code).Valid() {
			t.Fatalf("code %d is not registered in the shared SDK", code)
		}
	}
}

func TestInternalKeepsTechnicalCauseOutOfPublicMessage(t *testing.T) {
	t.Parallel()
	cause := errors.New("database password leaked")
	err := Internal(cause)
	if err.Message != "internal server error" || err.HTTPStatus != http.StatusInternalServerError || !errors.Is(err, cause) {
		t.Fatalf("Internal() = %+v", err)
	}
}
