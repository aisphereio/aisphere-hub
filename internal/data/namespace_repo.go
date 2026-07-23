package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// namespaceRepo implements biz.NamespaceRepository against k8s_namespaces +
// k8s_namespace_shares (design §6 / §8.3 / §8.4). CAS / status-machine
// patterns mirror clusterRepo. Two JSONB columns (labels_json,
// annotations_json) map to json.RawMessage.
type namespaceRepo struct {
	db    func(context.Context) *gorm.DB
	newID func() string
	now   func() time.Time
}

// NewNamespaceRepo builds a biz.NamespaceRepository from Resources.
func NewNamespaceRepo(resources *Resources) biz.NamespaceRepository {
	return &namespaceRepo{
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

func newNamespaceRepoForDB(db *gorm.DB) *namespaceRepo {
	return &namespaceRepo{
		db:    func(ctx context.Context) *gorm.DB { return db.WithContext(ctx) },
		newID: func() string { return uuid.NewString() },
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// k8sNamespaceModel maps to k8s_namespaces (migration §8.3).
type k8sNamespaceModel struct {
	ID                   string          `gorm:"primaryKey;column:id;size:36"`
	ClusterID            string          `gorm:"column:cluster_id;size:36;not null"`
	KubeName             string          `gorm:"column:kube_name;size:253;not null"`
	DisplayName          string          `gorm:"column:display_name;size:256;not null;default:''"`
	Description          string          `gorm:"column:description;type:text;not null;default:''"`
	Visibility           string          `gorm:"column:visibility;size:32;not null;default:PRIVATE"`
	VisibilitySyncStatus string          `gorm:"column:visibility_sync_status;size:32;not null;default:SYNCED"`
	Lifecycle            string          `gorm:"column:lifecycle;size:32;not null;default:CREATING"`
	Managed              bool            `gorm:"column:managed;not null;default:true"`
	KubernetesUID        string          `gorm:"column:kubernetes_uid;size:128;not null;default:''"`
	ResourceVersion      string          `gorm:"column:resource_version;size:128;not null;default:''"`
	LabelsJSON           json.RawMessage `gorm:"column:labels_json;type:jsonb;not null;default:'{}'::jsonb"`
	AnnotationsJSON      json.RawMessage `gorm:"column:annotations_json;type:jsonb;not null;default:'{}'::jsonb"`
	OwnerType            string          `gorm:"column:owner_type;size:32;not null"`
	OwnerID              string          `gorm:"column:owner_id;size:128;not null"`
	CreatedByType        string          `gorm:"column:created_by_type;size:32;not null"`
	CreatedBy            string          `gorm:"column:created_by;size:128;not null"`
	LastSyncAt           *time.Time      `gorm:"column:last_sync_at"`
	LastErrorCode        string          `gorm:"column:last_error_code;size:64;not null;default:''"`
	LastErrorMessage     string          `gorm:"column:last_error_message;type:text;not null;default:''"`
	CreatedAt            time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt            time.Time       `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt            *time.Time      `gorm:"column:deleted_at"`
	Revision             int64           `gorm:"column:revision;not null;default:1"`
}

func (k8sNamespaceModel) TableName() string { return "k8s_namespaces" }

// k8sNamespaceShareModel maps to k8s_namespace_shares (migration §8.4).
type k8sNamespaceShareModel struct {
	ID              string    `gorm:"primaryKey;column:id;size:36"`
	NamespaceID     string    `gorm:"column:namespace_id;size:36;not null"`
	Relation        string    `gorm:"column:relation;size:32;not null"`
	SubjectType     string    `gorm:"column:subject_type;size:32;not null"`
	SubjectID       string    `gorm:"column:subject_id;size:128;not null"`
	SubjectRelation string    `gorm:"column:subject_relation;size:64;not null;default:''"`
	SyncStatus      string    `gorm:"column:sync_status;size:32;not null;default:SYNCED"`
	CreatedByType   string    `gorm:"column:created_by_type;size:32;not null"`
	CreatedBy       string    `gorm:"column:created_by;size:128;not null"`
	CreatedAt       time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (k8sNamespaceShareModel) TableName() string { return "k8s_namespace_shares" }

// --- Namespace CRUD ---

func (r *namespaceRepo) CreateNamespace(ctx context.Context, ns *biz.Namespace) (*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	if ns == nil {
		return nil, errors.New("namespace repo: nil namespace")
	}
	if ns.ID == "" {
		ns.ID = r.newID()
	}
	if ns.Visibility == "" {
		ns.Visibility = biz.NamespaceVisibilityPrivate
	}
	if ns.VisibilitySyncStatus == "" {
		ns.VisibilitySyncStatus = biz.VisibilitySyncSynced
	}
	if ns.Lifecycle == "" {
		ns.Lifecycle = biz.NamespaceLifecycleCreating
	}
	if ns.Revision == 0 {
		ns.Revision = 1
	}
	row, err := namespaceModelFromBiz(ns)
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("namespace repo: create %s: %w", ns.ID, err)
	}
	return namespaceModelToBiz(row), nil
}

func (r *namespaceRepo) GetNamespace(ctx context.Context, id string) (*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	var row k8sNamespaceModel
	err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrNamespaceNotFound
		}
		return nil, fmt.Errorf("namespace repo: get %s: %w", id, err)
	}
	return namespaceModelToBiz(row), nil
}

func (r *namespaceRepo) GetNamespaceByClusterKubeName(ctx context.Context, clusterID, kubeName string) (*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	var row k8sNamespaceModel
	err := db.WithContext(ctx).
		Where("cluster_id = ? AND kube_name = ? AND deleted_at IS NULL", clusterID, kubeName).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrNamespaceNotFound
		}
		return nil, fmt.Errorf("namespace repo: get by cluster/kube_name: %w", err)
	}
	return namespaceModelToBiz(row), nil
}

func (r *namespaceRepo) ListNamespacesByCluster(ctx context.Context, clusterID string) ([]*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	var rows []k8sNamespaceModel
	if err := db.WithContext(ctx).
		Where("cluster_id = ? AND deleted_at IS NULL", clusterID).
		Order("kube_name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("namespace repo: list by cluster: %w", err)
	}
	out := make([]*biz.Namespace, len(rows))
	for i, row := range rows {
		out[i] = namespaceModelToBiz(row)
	}
	return out, nil
}

// ListNamespacesByOwner scans by (owner_type, owner_id, kube_name > cursor)
// for the candidate feed (design §7.6.3). Returns next cursor.
func (r *namespaceRepo) ListNamespacesByOwner(ctx context.Context, ownerType, ownerID, cursor string, maxScan int) ([]*biz.Namespace, string, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, "", errors.New("namespace repo: database not configured")
	}
	if maxScan <= 0 {
		maxScan = 100
	}
	q := db.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ? AND deleted_at IS NULL", ownerType, ownerID)
	if cursor != "" {
		q = q.Where("kube_name > ?", cursor)
	}
	var rows []k8sNamespaceModel
	if err := q.Order("owner_type ASC, owner_id ASC, kube_name ASC").Limit(maxScan).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("namespace repo: list by owner: %w", err)
	}
	out := make([]*biz.Namespace, len(rows))
	for i, row := range rows {
		out[i] = namespaceModelToBiz(row)
	}
	nextCursor := ""
	if len(rows) == maxScan {
		nextCursor = rows[len(rows)-1].KubeName
	}
	return out, nextCursor, nil
}

// UpdateNamespaceWithCAS applies field-masked updates guarded by expected_revision
// (design §7.5). revision incremented on success. Accepts "labels" and
// "annotations" map[string]string keys (marshaled to JSONB here).
func (r *namespaceRepo) UpdateNamespaceWithCAS(ctx context.Context, id string, expectedRevision int64, updates map[string]any) (*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	if len(updates) == 0 {
		return r.GetNamespace(ctx, id)
	}
	cleaned := make(map[string]any, len(updates))
	for k, v := range updates {
		switch k {
		case "labels":
			if m, ok := v.(map[string]string); ok {
				b, err := json.Marshal(m)
				if err != nil {
					return nil, fmt.Errorf("namespace repo: marshal labels: %w", err)
				}
				cleaned["labels_json"] = b
				continue
			}
		case "annotations":
			if m, ok := v.(map[string]string); ok {
				b, err := json.Marshal(m)
				if err != nil {
					return nil, fmt.Errorf("namespace repo: marshal annotations: %w", err)
				}
				cleaned["annotations_json"] = b
				continue
			}
		}
		cleaned[k] = v
	}
	cleaned["revision"] = gorm.Expr("revision + 1")
	cleaned["updated_at"] = r.now()

	var updated *biz.Namespace
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sNamespaceModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(cleaned)
		if res.Error != nil {
			return fmt.Errorf("namespace repo: update %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrClusterRevisionConflict
		}
		var row k8sNamespaceModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = namespaceModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateNamespaceVisibility stamps visibility + visibility_sync_status guarded
// by expected_revision (design §7.5.3 step 1 — the DB transactional write that
// precedes the synchronous SpiceDB projection). revision incremented.
func (r *namespaceRepo) UpdateNamespaceVisibility(ctx context.Context, id string, expectedRevision int64, visibility, syncStatus string) (*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	var updated *biz.Namespace
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sNamespaceModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(map[string]any{
				"visibility":             visibility,
				"visibility_sync_status": syncStatus,
				"revision":               gorm.Expr("revision + 1"),
				"updated_at":             r.now(),
			})
		if res.Error != nil {
			return fmt.Errorf("namespace repo: update visibility %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrClusterRevisionConflict
		}
		var row k8sNamespaceModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = namespaceModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateNamespaceStatus is the lifecycle state-machine CAS (design §6.4 / §6.6).
func (r *namespaceRepo) UpdateNamespaceStatus(ctx context.Context, id, expected, next string, extraUpdates map[string]any) (*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	updates := map[string]any{
		"lifecycle":   next,
		"updated_at":  r.now(),
	}
	for k, v := range extraUpdates {
		if k == "last_sync_at" {
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

	var updated *biz.Namespace
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&k8sNamespaceModel{}).Where("id = ? AND deleted_at IS NULL", id)
		if expected != "" {
			q = q.Where("lifecycle = ?", expected)
		}
		res := q.Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("namespace repo: update status %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrNamespaceNotFound
		}
		var row k8sNamespaceModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = namespaceModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (r *namespaceRepo) SoftDeleteNamespace(ctx context.Context, id string) error {
	db := r.db(ctx)
	if db == nil {
		return errors.New("namespace repo: database not configured")
	}
	now := r.now()
	res := db.WithContext(ctx).Model(&k8sNamespaceModel{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{
			"deleted_at": now,
			"lifecycle":  biz.NamespaceLifecycleDeleted,
			"updated_at": now,
		})
	if res.Error != nil {
		return fmt.Errorf("namespace repo: soft delete %s: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return biz.ErrNamespaceNotFound
	}
	return nil
}

// --- Share CRUD (design §7.4) ---

func (r *namespaceRepo) CreateShare(ctx context.Context, share *biz.NamespaceShare) (*biz.NamespaceShare, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	if share == nil {
		return nil, errors.New("namespace repo: nil share")
	}
	if share.ID == "" {
		share.ID = r.newID()
	}
	if share.SyncStatus == "" {
		share.SyncStatus = biz.VisibilitySyncSynced
	}
	row := namespaceShareModelFromBiz(share)
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("namespace repo: create share: %w", err)
	}
	return namespaceShareModelToBiz(row), nil
}

func (r *namespaceRepo) DeleteShare(ctx context.Context, id string) error {
	db := r.db(ctx)
	if db == nil {
		return errors.New("namespace repo: database not configured")
	}
	res := db.WithContext(ctx).Delete(&k8sNamespaceShareModel{}, "id = ?", id)
	if res.Error != nil {
		return fmt.Errorf("namespace repo: delete share %s: %w", id, res.Error)
	}
	return nil
}

func (r *namespaceRepo) ListSharesByNamespace(ctx context.Context, namespaceID string) ([]*biz.NamespaceShare, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	var rows []k8sNamespaceShareModel
	if err := db.WithContext(ctx).
		Where("namespace_id = ?", namespaceID).
		Order("created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("namespace repo: list shares: %w", err)
	}
	out := make([]*biz.NamespaceShare, len(rows))
	for i, row := range rows {
		out[i] = namespaceShareModelToBiz(row)
	}
	return out, nil
}

// --- Reconciler feeds (design §7.5.5) ---

func (r *namespaceRepo) ListNamespacesBySyncStatus(ctx context.Context, syncStatus string, limit int) ([]*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []k8sNamespaceModel
	if err := db.WithContext(ctx).
		Where("visibility_sync_status = ? AND deleted_at IS NULL", syncStatus).
		Order("updated_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("namespace repo: list by sync status: %w", err)
	}
	out := make([]*biz.Namespace, len(rows))
	for i, row := range rows {
		out[i] = namespaceModelToBiz(row)
	}
	return out, nil
}

// ListReadyNamespaces returns namespaces with lifecycle=READY (non-deleted) for
// the SandboxSyncReconciler to reconcile sandbox/warm-pool/claim state.
func (r *namespaceRepo) ListReadyNamespaces(ctx context.Context, limit int) ([]*biz.Namespace, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []k8sNamespaceModel
	if err := db.WithContext(ctx).
		Where("lifecycle = ? AND deleted_at IS NULL", biz.NamespaceLifecycleReady).
		Order("updated_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("namespace repo: list ready namespaces: %w", err)
	}
	out := make([]*biz.Namespace, len(rows))
	for i, row := range rows {
		out[i] = namespaceModelToBiz(row)
	}
	return out, nil
}

func (r *namespaceRepo) ListSharesBySyncStatus(ctx context.Context, syncStatus string, limit int) ([]*biz.NamespaceShare, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("namespace repo: database not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []k8sNamespaceShareModel
	if err := db.WithContext(ctx).
		Where("sync_status = ?", syncStatus).
		Order("updated_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("namespace repo: list shares by sync status: %w", err)
	}
	out := make([]*biz.NamespaceShare, len(rows))
	for i, row := range rows {
		out[i] = namespaceShareModelToBiz(row)
	}
	return out, nil
}

// --- model <-> biz ---

func namespaceModelToBiz(m k8sNamespaceModel) *biz.Namespace {
	var labels, annotations map[string]string
	if len(m.LabelsJSON) > 0 {
		_ = json.Unmarshal(m.LabelsJSON, &labels)
	}
	if len(m.AnnotationsJSON) > 0 {
		_ = json.Unmarshal(m.AnnotationsJSON, &annotations)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	var lastSync time.Time
	if m.LastSyncAt != nil {
		lastSync = *m.LastSyncAt
	}
	return &biz.Namespace{
		ID:                   m.ID,
		ClusterID:            m.ClusterID,
		KubeName:             m.KubeName,
		DisplayName:          m.DisplayName,
		Description:          m.Description,
		Visibility:           m.Visibility,
		VisibilitySyncStatus: m.VisibilitySyncStatus,
		Lifecycle:            m.Lifecycle,
		Managed:              m.Managed,
		KubernetesUID:        m.KubernetesUID,
		ResourceVersion:      m.ResourceVersion,
		Labels:               labels,
		Annotations:          annotations,
		OwnerType:            m.OwnerType,
		OwnerID:              m.OwnerID,
		CreatedByType:        m.CreatedByType,
		CreatedBy:            m.CreatedBy,
		LastSyncAt:           lastSync,
		LastErrorCode:        m.LastErrorCode,
		LastErrorMessage:     m.LastErrorMessage,
		CreatedAt:            m.CreatedAt,
		UpdatedAt:            m.UpdatedAt,
		Revision:             m.Revision,
	}
}

func namespaceModelFromBiz(ns *biz.Namespace) (k8sNamespaceModel, error) {
	labels := ns.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := ns.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return k8sNamespaceModel{}, fmt.Errorf("marshal labels: %w", err)
	}
	annotationsJSON, err := json.Marshal(annotations)
	if err != nil {
		return k8sNamespaceModel{}, fmt.Errorf("marshal annotations: %w", err)
	}
	var lastSync *time.Time
	if !ns.LastSyncAt.IsZero() {
		t := ns.LastSyncAt.UTC()
		lastSync = &t
	}
	return k8sNamespaceModel{
		ID:                   ns.ID,
		ClusterID:            ns.ClusterID,
		KubeName:             ns.KubeName,
		DisplayName:          ns.DisplayName,
		Description:          ns.Description,
		Visibility:           ns.Visibility,
		VisibilitySyncStatus: ns.VisibilitySyncStatus,
		Lifecycle:            ns.Lifecycle,
		Managed:              ns.Managed,
		KubernetesUID:        ns.KubernetesUID,
		ResourceVersion:      ns.ResourceVersion,
		LabelsJSON:           labelsJSON,
		AnnotationsJSON:      annotationsJSON,
		OwnerType:            ns.OwnerType,
		OwnerID:              ns.OwnerID,
		CreatedByType:        ns.CreatedByType,
		CreatedBy:            ns.CreatedBy,
		LastSyncAt:           lastSync,
		LastErrorCode:        ns.LastErrorCode,
		LastErrorMessage:     ns.LastErrorMessage,
		Revision:             ns.Revision,
	}, nil
}

func namespaceShareModelToBiz(m k8sNamespaceShareModel) *biz.NamespaceShare {
	return &biz.NamespaceShare{
		ID:              m.ID,
		NamespaceID:     m.NamespaceID,
		Relation:        m.Relation,
		SubjectType:     m.SubjectType,
		SubjectID:       m.SubjectID,
		SubjectRelation: m.SubjectRelation,
		SyncStatus:      m.SyncStatus,
		CreatedByType:   m.CreatedByType,
		CreatedBy:       m.CreatedBy,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func namespaceShareModelFromBiz(s *biz.NamespaceShare) k8sNamespaceShareModel {
	return k8sNamespaceShareModel{
		ID:              s.ID,
		NamespaceID:     s.NamespaceID,
		Relation:        s.Relation,
		SubjectType:     s.SubjectType,
		SubjectID:       s.SubjectID,
		SubjectRelation: s.SubjectRelation,
		SyncStatus:      s.SyncStatus,
		CreatedByType:   s.CreatedByType,
		CreatedBy:       s.CreatedBy,
	}
}

var _ biz.NamespaceRepository = (*namespaceRepo)(nil)
