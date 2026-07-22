package biz

import (
	"context"
	"errors"

	"github.com/aisphereio/kernel/authn"
)

// ClusterPermissionsFor computes the caller-facing permission flags for an
// already-loaded cluster. List services use this to avoid returning objects
// whose permissions field is silently nil.
func (uc *ClusterUsecase) ClusterPermissionsFor(ctx context.Context, principal authn.Principal, cluster *Cluster) (*ClusterPermissions, error) {
	return uc.computeClusterPermissions(ctx, principal, cluster)
}

// NamespacePermissionsFor computes the caller-facing permission flags for an
// already-loaded namespace. List services use this to gate management actions
// without issuing a second database read for each row.
func (uc *NamespaceUsecase) NamespacePermissionsFor(ctx context.Context, principal authn.Principal, namespace *Namespace) (*NamespacePermissions, error) {
	return uc.computeNamespacePermissions(ctx, principal, namespace)
}

// ListNamespaces resolves every namespace the caller may view through
// SpiceDB, then hydrates those IDs from PostgreSQL. Unlike the old owner-only
// placeholder, this includes owned, explicitly shared, inherited, and PUBLIC
// namespaces and is therefore suitable for the global sharing selector.
func (uc *NamespaceUsecase) ListNamespaces(
	ctx context.Context,
	principal authn.Principal,
	orgID string,
	cursor string,
	limit int,
) ([]*Namespace, string, error) {
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, "", err
	}
	if orgID == "" {
		orgID = principal.OrgID
	}
	if limit <= 0 || limit > uc.opts.MaxScan {
		limit = uc.opts.MaxScan
	}

	lookup, err := uc.rels.LookupResources(ctx, AuthzLookupResourcesRequest{
		Subject:      subject,
		ResourceType: "k8s_namespace",
		Permission:   "view",
		OrgID:        orgID,
		Limit:        limit,
		Cursor:       cursor,
	})
	if err != nil {
		return nil, "", err
	}

	namespaces := make([]*Namespace, 0, len(lookup.Resources))
	for _, resource := range lookup.Resources {
		if resource.Type != "" && resource.Type != "k8s_namespace" {
			continue
		}
		namespace, getErr := uc.namespaces.GetNamespace(ctx, resource.ID)
		if errors.Is(getErr, ErrNamespaceNotFound) {
			// A stale SpiceDB relationship should not make the whole list fail;
			// bootstrap/reconcile will eventually remove the dangling tuple.
			continue
		}
		if getErr != nil {
			return nil, "", getErr
		}
		namespaces = append(namespaces, namespace)
	}
	return namespaces, lookup.NextCursor, nil
}
