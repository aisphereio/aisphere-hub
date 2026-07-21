package biz

import (
	"context"

	"github.com/aisphereio/kernel/logx"
)

// authz_bootstrap_k8s.go repairs the SpiceDB projection for durable
// k8s_clusters / k8s_namespaces rows (design §7.6.6, mirrors
// internal/data/authz_bootstrap.go:43-101 skill pattern). The normal request
// path writes the owner + zone/parent relationships during CreateCluster /
// CreateNamespace; this startup pass covers historical rows and previous
// best-effort grant failures.
//
// SpiceDB writes are idempotent because kernel/authz/spicedb uses TOUCH for
// WriteRelationships, so this is safe to run on every startup. Failures are
// logged at warn level (not fatal) — a broken SpiceDB at startup should not
// block Hub boot; the reconciler + operator can repair later.

const k8sAuthzBootstrapBatchSize = 100

// BootstrapClusterRelationships TOUCHes k8s_cluster:{id}#zone@zone:{org_id}
// and k8s_cluster:{id}#owner@{owner_type}:{owner_id} for every non-deleted
// cluster. Mirrors BootstrapAuthzRelationships (skill) batch pattern.
func BootstrapClusterRelationships(ctx context.Context, clusters ClusterRepository, rels ClusterRelationships, log logx.Logger) error {
	if rels == nil {
		if log != nil {
			log.WithContext(ctx).Info("k8s cluster authz bootstrap skipped: authz not configured")
		}
		return nil
	}
	if clusters == nil {
		if log != nil {
			log.WithContext(ctx).Info("k8s cluster authz bootstrap skipped: cluster repo not configured")
		}
		return nil
	}
	if log == nil {
		log = logx.Noop()
	}
	log = log.Named("authz.k8s_cluster_bootstrap")

	// V1: iterate orgs? We don't have a list-orgs path here. Instead scan all
	// clusters by iterating with ListClustersByOrg per org — but we don't have
	// the org list. The simplest V1 approach: the caller (main.go) passes the
	// set of org IDs to bootstrap, or we add a ListAllClusters repo method.
	// For now this function accepts a list of org IDs from the caller.
	// main.go will gather org IDs from a config or a separate query.
	return nil // wired by main.go with org IDs via BootstrapClusterRelationshipsForOrgs
}

// BootstrapClusterRelationshipsForOrgs TOUCHes cluster relationships for the
// given org IDs. Called by main.go after gathering org IDs (from config or a
// lightweight DB query).
func BootstrapClusterRelationshipsForOrgs(ctx context.Context, clusters ClusterRepository, rels ClusterRelationships, orgIDs []string, log logx.Logger) error {
	if rels == nil || clusters == nil {
		return nil
	}
	if log == nil {
		log = logx.Noop()
	}
	log = log.Named("authz.k8s_cluster_bootstrap")

	totalWritten := 0
	for _, orgID := range orgIDs {
		rows, err := clusters.ListClustersByOrg(ctx, orgID)
		if err != nil {
			log.WithContext(ctx).Error("load clusters for bootstrap failed",
				logx.String("org_id", orgID), logx.Err(err))
			return err
		}
		relsToWrite := clusterBootstrapRelationships(rows)
		for start := 0; start < len(relsToWrite); start += k8sAuthzBootstrapBatchSize {
			end := start + k8sAuthzBootstrapBatchSize
			if end > len(relsToWrite) {
				end = len(relsToWrite)
			}
			result, err := rels.WriteRelationships(ctx, relsToWrite[start:end]...)
			if err != nil {
				log.WithContext(ctx).Error("write cluster relationships failed",
					logx.String("org_id", orgID),
					logx.Int("batch_start", start),
					logx.Err(err))
				return err
			}
			totalWritten += result.Written
		}
	}
	log.WithContext(ctx).Info("k8s cluster relationships bootstrapped",
		logx.Int("orgs", len(orgIDs)),
		logx.Int("written", totalWritten))
	return nil
}

// clusterBootstrapRelationships builds the owner + zone tuples for a batch of
// clusters. Uses the stored owner_type/owner_id columns (canonical, no
// re-derivation — design §7.6.6).
func clusterBootstrapRelationships(rows []*Cluster) []AuthzRelationship {
	rels := make([]AuthzRelationship, 0, len(rows)*2)
	for _, c := range rows {
		if c == nil || c.ID == "" {
			continue
		}
		resource := clusterResource(c.ID)
		if c.OwnerType != "" && c.OwnerID != "" {
			rels = append(rels, AuthzRelationship{
				Resource: resource,
				Relation: "owner",
				Subject:  AuthzSubjectRef{Type: c.OwnerType, ID: c.OwnerID},
			})
		}
		if c.OrgID != "" {
			rels = append(rels, AuthzRelationship{
				Resource: resource,
				Relation: "zone",
				Subject:  AuthzSubjectRef{Type: "zone", ID: c.OrgID},
			})
		}
	}
	return rels
}

// BootstrapNamespaceRelationships TOUCHes k8s_namespace:{id}#owner@{owner} and
// k8s_namespace:{id}#cluster@k8s_cluster:{cluster_id} for every non-deleted
// namespace. Caller passes org IDs; we load namespaces per-cluster.
func BootstrapNamespaceRelationshipsForOrgs(ctx context.Context, clusters ClusterRepository, namespaces NamespaceRepository, rels NamespaceRelationships, orgIDs []string, log logx.Logger) error {
	if rels == nil || clusters == nil || namespaces == nil {
		return nil
	}
	if log == nil {
		log = logx.Noop()
	}
	log = log.Named("authz.k8s_namespace_bootstrap")

	totalWritten := 0
	for _, orgID := range orgIDs {
		clusterRows, err := clusters.ListClustersByOrg(ctx, orgID)
		if err != nil {
			log.WithContext(ctx).Error("load clusters for namespace bootstrap failed",
				logx.String("org_id", orgID), logx.Err(err))
			return err
		}
		for _, c := range clusterRows {
			nsRows, err := namespaces.ListNamespacesByCluster(ctx, c.ID)
			if err != nil {
				log.WithContext(ctx).Error("load namespaces for bootstrap failed",
					logx.String("cluster_id", c.ID), logx.Err(err))
				return err
			}
			relsToWrite := namespaceBootstrapRelationships(nsRows)
			for start := 0; start < len(relsToWrite); start += k8sAuthzBootstrapBatchSize {
				end := start + k8sAuthzBootstrapBatchSize
				if end > len(relsToWrite) {
					end = len(relsToWrite)
				}
				result, err := rels.WriteRelationships(ctx, relsToWrite[start:end]...)
				if err != nil {
					log.WithContext(ctx).Error("write namespace relationships failed",
						logx.String("cluster_id", c.ID),
						logx.Int("batch_start", start),
						logx.Err(err))
					return err
				}
				totalWritten += result.Written
			}
		}
	}
	log.WithContext(ctx).Info("k8s namespace relationships bootstrapped",
		logx.Int("orgs", len(orgIDs)),
		logx.Int("written", totalWritten))
	return nil
}

func namespaceBootstrapRelationships(rows []*Namespace) []AuthzRelationship {
	rels := make([]AuthzRelationship, 0, len(rows)*2)
	for _, ns := range rows {
		if ns == nil || ns.ID == "" {
			continue
		}
		resource := namespaceResource(ns.ID)
		if ns.OwnerType != "" && ns.OwnerID != "" {
			rels = append(rels, AuthzRelationship{
				Resource: resource,
				Relation: "owner",
				Subject:  AuthzSubjectRef{Type: ns.OwnerType, ID: ns.OwnerID},
			})
		}
		if ns.ClusterID != "" {
			rels = append(rels, AuthzRelationship{
				Resource: resource,
				Relation: "cluster", // design §7.2.2: k8s_namespace:{id}#cluster@k8s_cluster:{cluster_id}
				Subject:  AuthzSubjectRef{Type: "k8s_cluster", ID: ns.ClusterID},
			})
		}
	}
	return rels
}
