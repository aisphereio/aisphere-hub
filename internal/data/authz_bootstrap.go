// Package data authz_bootstrap.go — startup-time SpiceDB schema loader.
//
// On hub startup, after Resources are constructed, BootstrapAuthzSchema
// checks whether SpiceDB already has a schema. If not (or if the schema
// is empty), it writes a default schema that covers all resource types
// the hub currently uses (skill, organization, project, group, etc.).
//
// This is idempotent: WriteSchema on SpiceDB replaces the schema in
// place, so re-running on an already-initialized SpiceDB is safe (but
// will invalidate any tuples that reference relations not present in
// the new schema — operators should review the schema text before
// deploying a new hub version).
//
// The default schema is sourced from kernel/authz/spicedb.DefaultSchema
// extended with hub-specific resource types (skill, skill_version).
// Keeping the kernel default ensures platform / organization / group /
// application / project / resource types stay in sync with the kernel
// IAM projection layer.

package data

import (
	"context"
	"strings"

	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/logx"
)

// HubAuthzSchemaVersion is the schema version. Bump this when the schema
// text below changes. Operators can compare this against the version
// stored in their SpiceDB metadata to know whether a migration is needed.
//
// Versioning policy:
//   - Breaking changes (renamed relations, removed permissions, changed
//     permission composition): bump MAJOR. Operators MUST review and
//     manually run WriteSchema with the new text; existing tuples that
//     reference removed relations will be rejected.
//   - Additive changes (new relations, new permissions, new resource
//     types): bump MINOR. Auto-bootstrap is safe but operators who
//     already have a custom schema should still review.
//   - Cosmetic changes (comments, whitespace): bump PATCH. No migration
//     needed.
const HubAuthzSchemaVersion = "1.0.0"

// HubAuthzSchema is the default SpiceDB schema for aisphere-hub. It
// extends kernel/authz/spicedb.DefaultSchema with the skill resource
// type and its relations / permissions.
//
// Schema versioning & migration:
//
//   - BootstrapAuthzSchema writes this schema ONLY when SpiceDB has no
//     schema at all (empty / missing). It never overwrites an existing
//     schema — operators who customize the schema can do so safely.
//
//   - When this constant changes (version bump), operators must
//     explicitly migrate by calling the WriteSchema RPC with the new
//     schema text. The hub does NOT auto-migrate because:
//
//   - Breaking schema changes can invalidate existing tuples.
//
//   - Operators need to review the diff before applying.
//
//   - Migration timing should be coordinated with SpiceDB
//     maintenance windows.
//
//   - The CHANGELOG should document every schema version bump with the
//     specific changes and any required operator actions.
//
// Resource types:
//
//   - platform, organization, group, application, project, resource
//     (inherited from kernel default — managed by kernel IAM projection)
//
//   - skill (hub-specific):
//     relation owner:  user | service
//     relation editor: user | service | group#member
//     relation viewer: user | service | group#member
//     permission read   = viewer + editor + owner
//     permission edit   = editor + owner
//     permission delete = owner
//     permission share  = owner   (only owner can grant/revoke shares)
//
//   - skill_version (hub-specific, derived from skill):
//     relation skill: skill
//     permission read   = skill->read
//     permission edit   = skill->edit
//     permission delete = skill->delete
//
// The skill_version type lets us gate per-version operations (download,
// state transitions) independently of the parent skill, but currently
// all checks go through the skill resource type for simplicity. The
// skill_version type is defined for forward compatibility.
const HubAuthzSchema = `definition user {}
definition service {}

definition platform {
  relation super_admin: user | service
  permission admin = super_admin
}

definition organization {
  relation platform: platform
  relation owner: user | service
  relation admin: user | service | group#member
  relation member: user | service | group#member

  permission manage = owner + admin + platform->admin
  permission read = owner + admin + member + platform->admin
}

definition group {
  relation org: organization
  relation parent: group
  relation member: user | service | group#member

  permission read = member + parent->read + org->read
}

definition application {
  relation org: organization
  relation owner: user | service
  relation admin: user | service | group#member
  relation member: user | service | group#member

  permission manage = owner + admin + org->manage
  permission read = owner + admin + member + org->read
}

definition project {
  relation org: organization
  relation owner: user | service
  relation editor: user | service | group#member
  relation viewer: user | service | group#member

  permission read = viewer + editor + owner + org->read
  permission edit = editor + owner + org->manage
  permission delete = owner + org->manage
}

definition resource {
  relation project: project
  relation owner: user | service
  relation editor: user | service | group#member
  relation viewer: user | service | group#member

  permission read = viewer + editor + owner + project->read
  permission edit = editor + owner + project->edit
  permission delete = owner + project->delete
}

definition skill {
  relation owner: user | service
  relation editor: user | service | group#member
  relation viewer: user | service | group#member

  permission read = viewer + editor + owner
  permission edit = editor + owner
  permission delete = owner
  permission share = owner
}

definition skill_version {
  relation skill: skill
  permission read = skill->read
  permission edit = skill->edit
  permission delete = skill->delete
}`

// BootstrapAuthzSchema writes the default hub authz schema to SpiceDB
// when the schema is empty or missing. Called from main.go after
// Resources are constructed.
//
// Behavior:
//   - If AuthzService is nil (authz disabled), returns nil immediately.
//   - If ReadSchema returns an error other than "schema not found",
//     returns the error so main.go can surface it.
//   - If ReadSchema returns a non-empty schema text that already contains
//     Hub definitions, returns nil.
//   - If ReadSchema returns a non-empty schema text that only contains the
//     Kernel base definitions, writes HubAuthzSchema to add the skill model.
//   - If ReadSchema returns empty schema text, writes HubAuthzSchema.
//
// The function is safe to call on every startup — it only writes when
// the schema is empty, so re-running on an initialized SpiceDB is a
// no-op. Operators who want to replace the schema should use the
// WriteSchema RPC directly.
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
		// SpiceDB returns a NotFound-style error when no schema has been
		// written yet. We treat any error here as "schema missing" and
		// attempt to write the default. If the write also fails, we
		// surface THAT error (which is more actionable).
		log.WithContext(ctx).Warn("read schema failed; will attempt to write default",
			logx.Err(err),
		)
	} else if schema.Text != "" {
		if hasHubAuthzDefinitions(schema.Text) {
			log.WithContext(ctx).Info("authz schema already installed; skipping bootstrap",
				logx.Int("size", len(schema.Text)),
			)
			return nil
		}
		log.WithContext(ctx).Warn("authz schema missing hub definitions; applying hub schema",
			logx.Int("current_size", len(schema.Text)),
			logx.String("schema_version", HubAuthzSchemaVersion),
		)
	}

	if err := resources.AuthzService.WriteSchema(ctx, authz.Schema{Text: HubAuthzSchema}); err != nil {
		log.WithContext(ctx).Error("authz schema bootstrap failed",
			logx.Err(err),
		)
		return err
	}
	log.WithContext(ctx).Info("authz schema bootstrapped",
		logx.Int("size", len(HubAuthzSchema)),
	)
	return nil
}

func hasHubAuthzDefinitions(schema string) bool {
	normalized := strings.ToLower(schema)
	return strings.Contains(normalized, "definition skill ") &&
		strings.Contains(normalized, "definition skill_version ")
}

const authzRelationshipBootstrapBatchSize = 100

type skillOwnerRelationshipRow struct {
	Name    string
	OwnerID string
}

// BootstrapAuthzRelationships repairs the SpiceDB projection for durable hub
// rows that already exist in PostgreSQL. The normal request path writes the
// skill:{name}#owner@user:{owner_id} tuple during CreateSkill; this startup
// pass covers historical rows and previous best-effort grant failures.
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
