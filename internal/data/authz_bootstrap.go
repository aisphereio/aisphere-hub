// Package data contains startup repair helpers for Hub authorization data.
//
// IAM is the owner of the shared SpiceDB schema, including the skill resource
// definition. Hub must not overwrite an existing IAM-managed schema. The only
// schema fragment kept here is the optional skill_version definition used when
// Hub is started against an empty development SpiceDB instance.
package data

import (
	"context"
	"strings"

	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/logx"
)

// HubAuthzSchemaVersion versions the Hub-only development schema fragment.
// Production schema changes should be applied through aisphere-iam.
const HubAuthzSchemaVersion = "1.1.0"

// HubAuthzSchema is a minimal development bootstrap fragment. It assumes the
// IAM-owned schema defines skill with view/edit/delete permissions.
//
// When SpiceDB already has any schema, Hub leaves it untouched. In production,
// skill_version should be added to the IAM schema if per-version authorization
// is needed. Current runtime checks still authorize through the parent skill.
const HubAuthzSchema = `definition skill_version {
  relation skill: skill
  permission view = skill->view
  permission edit = skill->edit
  permission delete = skill->delete
}`

// BootstrapAuthzSchema writes the minimal Hub development schema only when
// SpiceDB has no schema. Existing schemas are always treated as externally
// managed and are never replaced by Hub.
func BootstrapAuthzSchema(ctx context.Context, resources *Resources, log logx.Logger) error {
	if resources == nil || resources.AuthzService == nil {
		if log != nil {
			log.WithContext(ctx).Info("authz schema bootstrap skipped: authz not configured")
		}
		return nil
	}
	if log == nil {
		log = logx.Noop()
	}
	log = log.Named("authz.bootstrap")

	schema, err := resources.AuthzService.ReadSchema(ctx)
	if err != nil {
		// An empty development SpiceDB may report schema-not-found. Attempt the
		// minimal bootstrap; a real connectivity/permission problem will surface
		// again from WriteSchema with a more actionable error.
		log.WithContext(ctx).Warn("read schema failed; attempting minimal development bootstrap", logx.Err(err))
	} else if strings.TrimSpace(schema.Text) != "" {
		if hasHubAuthzDefinitions(schema.Text) {
			log.WithContext(ctx).Info("hub authz definition already present; skipping bootstrap",
				logx.Int("size", len(schema.Text)),
			)
			return nil
		}
		log.WithContext(ctx).Info("authz schema is managed externally; Hub will not modify it",
			logx.Int("current_size", len(schema.Text)),
		)
		return nil
	}

	if err := resources.AuthzService.WriteSchema(ctx, authz.Schema{Text: HubAuthzSchema}); err != nil {
		log.WithContext(ctx).Error("minimal authz schema bootstrap failed", logx.Err(err))
		return err
	}
	log.WithContext(ctx).Info("minimal hub authz schema bootstrapped",
		logx.String("schema_version", HubAuthzSchemaVersion),
		logx.Int("size", len(HubAuthzSchema)),
	)
	return nil
}

func hasHubAuthzDefinitions(schema string) bool {
	normalized := strings.ToLower(schema)
	return strings.Contains(normalized, "definition skill_version ")
}

const authzRelationshipBootstrapBatchSize = 100

type skillOwnerRelationshipRow struct {
	Name    string
	OwnerID string
}

// BootstrapAuthzRelationships repairs the SpiceDB projection for durable Hub
// rows that already exist in PostgreSQL. The normal request path writes the
// skill:{name}#owner@user:{owner_id} tuple during CreateSkill; this startup pass
// covers historical rows and previous best-effort grant failures.
//
// SpiceDB writes are idempotent because kernel/authz/spicedb uses TOUCH for
// WriteRelationships, so this function is safe to run on every startup.
func BootstrapAuthzRelationships(ctx context.Context, resources *Resources, log logx.Logger) error {
	if resources == nil || resources.AuthzService == nil {
		if log != nil {
			log.WithContext(ctx).Info("authz relationship bootstrap skipped: authz not configured")
		}
		return nil
	}
	if resources.DB == nil {
		if log != nil {
			log.WithContext(ctx).Info("authz relationship bootstrap skipped: db not configured")
		}
		return nil
	}
	if log == nil {
		log = logx.Noop()
	}
	log = log.Named("authz.relationship_bootstrap")

	var rows []skillOwnerRelationshipRow
	if err := resources.DB.GORM(ctx).
		Model(&skillModel{}).
		Select("name, owner_id").
		Where("name <> '' AND owner_id <> ''").
		Find(&rows).Error; err != nil {
		log.WithContext(ctx).Error("load skill owner relationships failed", logx.Err(err))
		return err
	}

	rels := skillOwnerRelationships(rows)
	if len(rels) == 0 {
		log.WithContext(ctx).Info("authz relationship bootstrap skipped: no skill owners found")
		return nil
	}

	written := 0
	for start := 0; start < len(rels); start += authzRelationshipBootstrapBatchSize {
		end := start + authzRelationshipBootstrapBatchSize
		if end > len(rels) {
			end = len(rels)
		}
		result, err := resources.AuthzService.WriteRelationships(ctx, rels[start:end]...)
		if err != nil {
			log.WithContext(ctx).Error("write skill owner relationships failed",
				logx.Int("batch_start", start),
				logx.Int("batch_size", end-start),
				logx.Err(err),
			)
			return err
		}
		written += result.Written
	}

	log.WithContext(ctx).Info("authz relationships bootstrapped",
		logx.Int("skills", len(rels)),
		logx.Int("written", written),
	)
	return nil
}

func skillOwnerRelationships(rows []skillOwnerRelationshipRow) []authz.Relationship {
	rels := make([]authz.Relationship, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		ownerID := strings.TrimSpace(row.OwnerID)
		if name == "" || ownerID == "" {
			continue
		}
		key := name + "\x00" + ownerID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rels = append(rels, authz.Relationship{
			Resource: authz.ObjectRef{Type: "skill", ID: name},
			Relation: "owner",
			Subject:  authz.SubjectRef{Type: authz.SubjectTypeUser, ID: ownerID},
		})
	}
	return rels
}
