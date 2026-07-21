package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// clusterRepo implements biz.ClusterRepository against the k8s_clusters table
// (design §5.3 / §8.1). CAS uses WHERE revision=? + RowsAffected==0 →
// ErrClusterRevisionConflict (pattern from pull_request.go:218-226). Status-
// machine transitions use WHERE status=expected (pattern from skill.go:203-
// 221). JSONB columns (labels_json) map to json.RawMessage; the model does
// (un)marshal at the boundary so biz.Cluster carries map[string]string.
//
// All read methods exclude soft-deleted rows (deleted_at IS NULL) to honor the
// partial unique indexes. Writes that should fail on conflict rely on those
// partial unique indexes (uq_k8s_clusters_org_name, uq_k8s_clusters_uid).
type clusterRepo struct {
	db    func(context.Context) *gorm.DB
	newID func() string
	now   func() time.Time
}

// NewClusterRepo builds a biz.ClusterRepository from Resources.
func NewClusterRepo(resources *Resources) biz.ClusterRepository {
	return &clusterRepo{
		db: func(ctx context.Context) *gorm.DB {
			if resources == nil || resources.DB == nil {
				return nil
			}
			return resources.DB.GORM(ctx)
		},
		newID: func() string { return uuid.NewString() },
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// newClusterRepoForDB is the test constructor (in-memory sqlite).
func newClusterRepoForDB(db *gorm.DB) *clusterRepo {
	return &clusterRepo{
		db:    func(ctx context.Context) *gorm.DB { return db.WithContext(ctx) },
		newID: func() string { return uuid.NewString() },
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// k8sClusterModel maps to k8s_clusters (migration §8.1). labels_json is JSONB;
// we store json.RawMessage and (un)marshal in toBiz/fromBiz. deleted_at is
// *time.Time so soft-delete sets it; the partial unique indexes filter on
// deleted_at IS NULL.
type k8sClusterModel struct {
	ID                 string             `gorm:"primaryKey;column:id;size:36"`
	OrgID              string             `gorm:"column:org_id;size:128;not null"`
	Name               string             `gorm:"column:name;size:128;not null"`
	DisplayName        string             `gorm:"column:display_name;size:256;not null;default:''"`
	Description        string             `gorm:"column:description;type:text;not null;default:''"`
	ServerURL          string             `gorm:"column:server_url;type:text;not null"`
	CredentialRef      string             `gorm:"column:credential_ref;size:36;not null"`
	CredentialRevision int64              `gorm:"column:credential_revision;not null"`
	Distribution       string             `gorm:"column:distribution;size:64;not null;default:''"`
	KubernetesVersion  string             `gorm:"column:kubernetes_version;size:64;not null;default:''"`
	ClusterUID         string             `gorm:"column:cluster_uid;size:128;not null;default:''"`
	Status             string             `gorm:"column:status;size:32;not null;default:CREATING"`
	HealthMessage      string             `gorm:"column:health_message;type:text;not null;default:''"`
	LabelsJSON         json.RawMessage    `gorm:"column:labels_json;type:jsonb;not null;default:'{}'::jsonb"`
	LastProbeAt        *time.Time         `gorm:"column:last_probe_at"`
	OwnerType          string             `gorm:"column:owner_type;size:32;not null"`
	OwnerID            string             `gorm:"column:owner_id;size:128;not null"`
	CreatedByType      string             `gorm:"column:created_by_type;size:32;not null"`
	CreatedBy          string             `gorm:"column:created_by;size:128;not null"`
	CreatedAt          time.Time          `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt          time.Time          `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt          *time.Time         `gorm:"column:deleted_at"`
	Revision           int64              `gorm:"column:revision;not null;default:1"`
}

func (k8sClusterModel) TableName() string { return "k8s_clusters" }

// CreateCluster inserts a new row. ID is allocated when empty. Status defaults
// to CREATING; revision defaults to 1 (migration DEFAULT). The caller has
// already validated ServerURL via EndpointPolicy and stored the credential
// via ClusterCredentialStore (design §5.7.2).
func (r *clusterRepo) CreateCluster(ctx context.Context, c *biz.Cluster) (*biz.Cluster, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("cluster repo: database not configured")
	}
	if c == nil {
		return nil, errors.New("cluster repo: nil cluster")
	}
	if c.ID == "" {
		c.ID = r.newID()
	}
	if c.Status == "" {
		c.Status = biz.ClusterStatusCreating
	}
	if c.Revision == 0 {
		c.Revision = 1
	}
	row, err := clusterModelFromBiz(c)
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("cluster repo: create %s: %w", c.ID, err)
	}
	return clusterModelToBiz(row), nil
}

// GetCluster loads a non-deleted cluster by id.
func (r *clusterRepo) GetCluster(ctx context.Context, id string) (*biz.Cluster, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("cluster repo: database not configured")
	}
	var row k8sClusterModel
	err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrClusterNotFound
		}
		return nil, fmt.Errorf("cluster repo: get %s: %w", id, err)
	}
	return clusterModelToBiz(row), nil
}

// GetClusterByOrgName loads by the (org_id, name) partial unique index.
func (r *clusterRepo) GetClusterByOrgName(ctx context.Context, orgID, name string) (*biz.Cluster, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("cluster repo: database not configured")
	}
	var row k8sClusterModel
	err := db.WithContext(ctx).
		Where("org_id = ? AND name = ? AND deleted_at IS NULL", orgID, name).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrClusterNotFound
		}
		return nil, fmt.Errorf("cluster repo: get by org/name: %w", err)
	}
	return clusterModelToBiz(row), nil
}

// ListClusterCandidates scans by (org_id, name > cursor) ordered by
// (org_id, name), limit maxScan, soft-deleted excluded (design §5.3.1 / §7.6.3
// candidate feed for BatchCheck). Returns the next cursor (last name, empty
// when exhausted). cursor="" starts from the beginning.
func (r *clusterRepo) ListClusterCandidates(ctx context.Context, orgID, cursor string, maxScan int) ([]*biz.Cluster, string, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, "", errors.New("cluster repo: database not configured")
	}
	if maxScan <= 0 {
		maxScan = 100
	}
	q := db.WithContext(ctx).
		Where("org_id = ? AND deleted_at IS NULL", orgID)
	if cursor != "" {
		q = q.Where("name > ?", cursor)
	}
	var rows []k8sClusterModel
	if err := q.Order("org_id ASC, name ASC").Limit(maxScan).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("cluster repo: list candidates: %w", err)
	}
	out := make([]*biz.Cluster, len(rows))
	for i, row := range rows {
		out[i] = clusterModelToBiz(row)
	}
	nextCursor := ""
	if len(rows) == maxScan {
		nextCursor = rows[len(rows)-1].Name
	}
	return out, nextCursor, nil
}

// ListClustersByOrg loads all non-deleted clusters for an org (bootstrap path,
// not paginated).
func (r *clusterRepo) ListClustersByOrg(ctx context.Context, orgID string) ([]*biz.Cluster, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("cluster repo: database not configured")
	}
	var rows []k8sClusterModel
	if err := db.WithContext(ctx).
		Where("org_id = ? AND deleted_at IS NULL", orgID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cluster repo: list by org: %w", err)
	}
	out := make([]*biz.Cluster, len(rows))
	for i, row := range rows {
		out[i] = clusterModelToBiz(row)
	}
	return out, nil
}

// UpdateClusterWithCAS applies field-masked updates guarded by expected_revision
// (design §5.7.4). revision is incremented on success. RowsAffected==0 →
// ErrClusterRevisionConflict. The caller validates the FieldMask whitelist
// (immutable fields rejected before calling). updates keys are column names
// (e.g. "display_name", "description", "labels_json"); labels_json must be
// pre-marshaled by the caller — but for ergonomics we accept map[string]string
// under the special key "labels" and marshal it here.
func (r *clusterRepo) UpdateClusterWithCAS(ctx context.Context, id string, expectedRevision int64, updates map[string]any) (*biz.Cluster, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("cluster repo: database not configured")
	}
	if len(updates) == 0 {
		return r.GetCluster(ctx, id)
	}
	// Translate biz-friendly "labels" map to labels_json JSONB.
	cleaned := make(map[string]any, len(updates))
	for k, v := range updates {
		if k == "labels" {
			if labels, ok := v.(map[string]string); ok {
				b, err := json.Marshal(labels)
				if err != nil {
					return nil, fmt.Errorf("cluster repo: marshal labels: %w", err)
				}
				cleaned["labels_json"] = b
				continue
			}
		}
		cleaned[k] = v
	}
	// revision is incremented atomically in the same UPDATE so a concurrent
	// writer cannot wedge the row.
	cleaned["revision"] = gorm.Expr("revision + 1")
	cleaned["updated_at"] = r.now()

	var updated *biz.Cluster
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sClusterModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(cleaned)
		if res.Error != nil {
			return fmt.Errorf("cluster repo: update %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrClusterRevisionConflict
		}
		var row k8sClusterModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = clusterModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateClusterStatus is the state-machine CAS (design §5.7.2): UPDATE WHERE
// id=? AND status=expected AND deleted_at IS NULL. RowsAffected==0 →
// ErrClusterNotFound. extraUpdates stamps probe results atomically with the
// flip (cluster_uid, kubernetes_version, last_probe_at, health_message).
// Passing expected="" skips the status guard (used for terminal transitions
// where any non-deleted row is acceptable).
func (r *clusterRepo) UpdateClusterStatus(ctx context.Context, id, expected, next string, extraUpdates map[string]any) (*biz.Cluster, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("cluster repo: database not configured")
	}
	updates := map[string]any{
		"status":     next,
		"updated_at": r.now(),
	}
	for k, v := range extraUpdates {
		// Special-case time values that may arrive as time.Time for
		// last_probe_at; store as *time.Time (NULL when zero).
		if k == "last_probe_at" {
			if t, ok := v.(time.Time); ok {
				if t.IsZero() {
					updates[k] = nil
				} else {
					updates[k] = t.UTC()
				}
				continue
			}
			if tp, ok := v.(*time.Time); ok {
				updates[k] = tp
				continue
			}
		}
		updates[k] = v
	}

	var updated *biz.Cluster
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&k8sClusterModel{}).Where("id = ? AND deleted_at IS NULL", id)
		if expected != "" {
			q = q.Where("status = ?", expected)
		}
		res := q.Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("cluster repo: update status %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrClusterNotFound
		}
		var row k8sClusterModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = clusterModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateClusterCredential stamps a new credential_ref + credential_revision
// guarded by expected_revision (design §5.7.3 rotate step 4). revision is
// incremented. RowsAffected==0 → ErrClusterRevisionConflict (a concurrent
// writer beat us).
func (r *clusterRepo) UpdateClusterCredential(ctx context.Context, id string, expectedRevision, newRevision int64, newRef string) (*biz.Cluster, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("cluster repo: database not configured")
	}
	var updated *biz.Cluster
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sClusterModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(map[string]any{
				"credential_ref":      newRef,
				"credential_revision": newRevision,
				"revision":            gorm.Expr("revision + 1"),
				"updated_at":          r.now(),
			})
		if res.Error != nil {
			return fmt.Errorf("cluster repo: update credential %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrClusterRevisionConflict
		}
		var row k8sClusterModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = clusterModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// SoftDeleteCluster sets deleted_at + status=DELETED (design §5.7.5). The
// caller has already checked the Namespaces guard (CountNamespacesForCluster
// == 0 for hard delete, or DeletePolicy=DETACH_ONLY).
func (r *clusterRepo) SoftDeleteCluster(ctx context.Context, id string) error {
	db := r.db(ctx)
	if db == nil {
		return errors.New("cluster repo: database not configured")
	}
	now := r.now()
	res := db.WithContext(ctx).Model(&k8sClusterModel{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{
			"deleted_at": now,
			"status":     biz.ClusterStatusDeleted,
			"updated_at": now,
		})
	if res.Error != nil {
		return fmt.Errorf("cluster repo: soft delete %s: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return biz.ErrClusterNotFound
	}
	return nil
}

// CountNamespacesForCluster counts non-deleted namespaces on a cluster, for
// the DeleteCluster hard-delete guard (design §5.7.5).
func (r *clusterRepo) CountNamespacesForCluster(ctx context.Context, clusterID string) (int64, error) {
	db := r.db(ctx)
	if db == nil {
		return 0, errors.New("cluster repo: database not configured")
	}
	var count int64
	if err := db.WithContext(ctx).
		Table("k8s_namespaces").
		Where("cluster_id = ? AND deleted_at IS NULL", clusterID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("cluster repo: count namespaces: %w", err)
	}
	return count, nil
}

// --- model <-> biz ---

func clusterModelToBiz(m k8sClusterModel) *biz.Cluster {
	var labels map[string]string
	if len(m.LabelsJSON) > 0 {
		_ = json.Unmarshal(m.LabelsJSON, &labels)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	var lastProbe time.Time
	if m.LastProbeAt != nil {
		lastProbe = *m.LastProbeAt
	}
	return &biz.Cluster{
		ID:                 m.ID,
		OrgID:              m.OrgID,
		Name:               m.Name,
		DisplayName:        m.DisplayName,
		Description:        m.Description,
		ServerURL:          m.ServerURL,
		CredentialRef:      m.CredentialRef,
		CredentialRevision: m.CredentialRevision,
		Distribution:       m.Distribution,
		KubernetesVersion:  m.KubernetesVersion,
		ClusterUID:         m.ClusterUID,
		Status:             m.Status,
		HealthMessage:      m.HealthMessage,
		Labels:             labels,
		LastProbeAt:        lastProbe,
		OwnerType:          m.OwnerType,
		OwnerID:            m.OwnerID,
		CreatedByType:      m.CreatedByType,
		CreatedBy:          m.CreatedBy,
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
		Revision:           m.Revision,
	}
}

func clusterModelFromBiz(c *biz.Cluster) (k8sClusterModel, error) {
	labels := c.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return k8sClusterModel{}, fmt.Errorf("marshal labels: %w", err)
	}
	var lastProbe *time.Time
	if !c.LastProbeAt.IsZero() {
		t := c.LastProbeAt.UTC()
		lastProbe = &t
	}
	return k8sClusterModel{
		ID:                 c.ID,
		OrgID:              c.OrgID,
		Name:               c.Name,
		DisplayName:        c.DisplayName,
		Description:        c.Description,
		ServerURL:          c.ServerURL,
		CredentialRef:      c.CredentialRef,
		CredentialRevision: c.CredentialRevision,
		Distribution:       c.Distribution,
		KubernetesVersion:  c.KubernetesVersion,
		ClusterUID:         c.ClusterUID,
		Status:             c.Status,
		HealthMessage:      c.HealthMessage,
		LabelsJSON:         labelsJSON,
		LastProbeAt:        lastProbe,
		OwnerType:          c.OwnerType,
		OwnerID:            c.OwnerID,
		CreatedByType:      c.CreatedByType,
		CreatedBy:          c.CreatedBy,
		Revision:           c.Revision,
	}, nil
}

// compile-time interface check.
var _ biz.ClusterRepository = (*clusterRepo)(nil)

// strings is used for future TrimSpace guards on name/org_id; anchored here so
// unused-import doesn't fire when those guards are added incrementally.
var _ = strings.TrimSpace
