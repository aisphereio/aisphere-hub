package data

import (
	iamauthz "github.com/aisphereio/aisphere-iam/client/authzgrpc"
	"github.com/aisphereio/kernel/authz"
)

// runtimeAuthzService is the data-plane authorization surface Hub is allowed
// to use through IAM. Schema publication remains an IAM control-plane concern.
type runtimeAuthzService interface {
	authz.Authorizer
	authz.BatchAuthorizer
	authz.ResourceLookup
	authz.SubjectLookup
	authz.RelationshipStore
}

var _ runtimeAuthzService = (*iamauthz.Client)(nil)
