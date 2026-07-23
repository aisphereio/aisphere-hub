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

// sandboxRepo implements biz.SandboxRepository against the Agent Sandbox
// control-plane tables k8s_sandbox_templates, k8s_sandboxes,
// k8s_sandbox_warm_pools and k8s_sandbox_claims (design §11 / migration
// 202607230001). CAS / status-machine patterns mirror namespaceRepo and
// clusterRepo. Each table has one JSONB column (labels_json) mapped to
// json.RawMessage; nullable timestamps (last_sync_at, deleted_at) map to
// *time.Time. container_command is TEXT holding a JSON-encoded string array
// and is passed through as an opaque string at this layer.
type sandboxRepo struct {
	db    func(context.Context) *gorm.DB
	newID func() string
	now   func() time.Time
}

// NewSandboxRepo builds a biz.SandboxRepository from Resources.
func NewSandboxRepo(resources *Resources) biz.SandboxRepository {
	return &sandboxRepo{
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

func newSandboxRepoForDB(db *gorm.DB) *sandboxRepo {
	return &sandboxRepo{
		db:    func(ctx context.Context) *gorm.DB { return db.WithContext(ctx) },
		newID: func() string { return uuid.NewString() },
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// k8sSandboxTemplateModel maps to k8s_sandbox_templates (migration §11.1).
type k8sSandboxTemplateModel struct {
	ID                  string          `gorm:"primaryKey;column:id;size:36"`
	ClusterID           string          `gorm:"column:cluster_id;size:36;not null"`
	OrgID               string          `gorm:"column:org_id;size:128;not null"`
	Name                string          `gorm:"column:name;size:128;not null"`
	DisplayName         string          `gorm:"column:display_name;size:256;not null;default:''"`
	Description         string          `gorm:"column:description;type:text;not null;default:''"`
	KubernetesName      string          `gorm:"column:kubernetes_name;size:128;not null"`
	KubernetesNamespace string          `gorm:"column:kubernetes_namespace;size:128;not null;default:'agent-sandbox-system'"`
	KubernetesUID       string          `gorm:"column:kubernetes_uid;size:128;not null;default:''"`
	ResourceVersion     string          `gorm:"column:resource_version;size:128;not null;default:''"`
	Image               string          `gorm:"column:image;type:text;not null"`
	ContainerCommand    string          `gorm:"column:container_command;type:text;not null;default:'[]'"`
	LabelsJSON          json.RawMessage `gorm:"column:labels_json;type:jsonb;not null;default:'{}'::jsonb"`
	Status              string          `gorm:"column:status;size:32;not null;default:'CREATING'"`
	HealthMessage       string          `gorm:"column:health_message;type:text;not null;default:''"`
	OwnerType           string          `gorm:"column:owner_type;size:32;not null"`
	OwnerID             string          `gorm:"column:owner_id;size:128;not null"`
	CreatedByType       string          `gorm:"column:created_by_type;size:32;not null"`
	CreatedBy           string          `gorm:"column:created_by;size:128;not null"`
	CreatedAt           time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt           time.Time       `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt           *time.Time      `gorm:"column:deleted_at"`
	Revision            int64           `gorm:"column:revision;not null;default:1"`
}

func (k8sSandboxTemplateModel) TableName() string { return "k8s_sandbox_templates" }

// k8sSandboxModel maps to k8s_sandboxes (migration §11.2).
type k8sSandboxModel struct {
	ID              string          `gorm:"primaryKey;column:id;size:36"`
	NamespaceID     string          `gorm:"column:namespace_id;size:36;not null"`
	ClusterID       string          `gorm:"column:cluster_id;size:36;not null"`
	OrgID           string          `gorm:"column:org_id;size:128;not null"`
	Name            string          `gorm:"column:name;size:128;not null"`
	KubernetesName  string          `gorm:"column:kubernetes_name;size:128;not null"`
	KubernetesUID   string          `gorm:"column:kubernetes_uid;size:128;not null;default:''"`
	ResourceVersion string          `gorm:"column:resource_version;size:128;not null;default:''"`
	TemplateID      *string         `gorm:"column:template_id;size:36"`
	WarmPoolID      *string         `gorm:"column:warm_pool_id;size:36"`
	ClaimID         *string         `gorm:"column:claim_id;size:36"`
	Lifecycle       string          `gorm:"column:lifecycle;size:32;not null;default:'CREATING'"`
	OperatingMode   string          `gorm:"column:operating_mode;size:32;not null;default:'RUNNING'"`
	PodName         string          `gorm:"column:pod_name;size:256;not null;default:''"`
	PodIP           string          `gorm:"column:pod_ip;size:64;not null;default:''"`
	NodeName        string          `gorm:"column:node_name;size:256;not null;default:''"`
	Image           string          `gorm:"column:image;type:text;not null;default:''"`
	WorkspacePVC    string          `gorm:"column:workspace_pvc;size:256;not null;default:''"`
	NetworkMode     string          `gorm:"column:network_mode;size:32;not null;default:'OFFLINE'"`
	LabelsJSON      json.RawMessage `gorm:"column:labels_json;type:jsonb;not null;default:'{}'::jsonb"`
	HealthMessage   string          `gorm:"column:health_message;type:text;not null;default:''"`
	LastSyncAt      *time.Time      `gorm:"column:last_sync_at"`
	OwnerType       string          `gorm:"column:owner_type;size:32;not null"`
	OwnerID         string          `gorm:"column:owner_id;size:128;not null"`
	CreatedByType   string          `gorm:"column:created_by_type;size:32;not null"`
	CreatedBy       string          `gorm:"column:created_by;size:128;not null"`
	CreatedAt       time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt       time.Time       `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt       *time.Time      `gorm:"column:deleted_at"`
	Revision        int64           `gorm:"column:revision;not null;default:1"`
}

func (k8sSandboxModel) TableName() string { return "k8s_sandboxes" }

// k8sSandboxWarmPoolModel maps to k8s_sandbox_warm_pools (migration §11.3).
type k8sSandboxWarmPoolModel struct {
	ID              string          `gorm:"primaryKey;column:id;size:36"`
	NamespaceID     string          `gorm:"column:namespace_id;size:36;not null"`
	ClusterID       string          `gorm:"column:cluster_id;size:36;not null"`
	OrgID           string          `gorm:"column:org_id;size:128;not null"`
	Name            string          `gorm:"column:name;size:128;not null"`
	KubernetesName  string          `gorm:"column:kubernetes_name;size:128;not null"`
	KubernetesUID   string          `gorm:"column:kubernetes_uid;size:128;not null;default:''"`
	ResourceVersion string          `gorm:"column:resource_version;size:128;not null;default:''"`
	TemplateID      string          `gorm:"column:template_id;size:36;not null"`
	Replicas        int32           `gorm:"column:replicas;not null;default:1"`
	ReadyReplicas   int32           `gorm:"column:ready_replicas;not null;default:0"`
	Status          string          `gorm:"column:status;size:32;not null;default:'CREATING'"`
	HealthMessage   string          `gorm:"column:health_message;type:text;not null;default:''"`
	LastSyncAt      *time.Time      `gorm:"column:last_sync_at"`
	OwnerType       string          `gorm:"column:owner_type;size:32;not null"`
	OwnerID         string          `gorm:"column:owner_id;size:128;not null"`
	CreatedByType   string          `gorm:"column:created_by_type;size:32;not null"`
	CreatedBy       string          `gorm:"column:created_by;size:128;not null"`
	CreatedAt       time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt       time.Time       `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt       *time.Time      `gorm:"column:deleted_at"`
	Revision        int64           `gorm:"column:revision;not null;default:1"`
}

func (k8sSandboxWarmPoolModel) TableName() string { return "k8s_sandbox_warm_pools" }

// k8sSandboxClaimModel maps to k8s_sandbox_claims (migration §11.4).
type k8sSandboxClaimModel struct {
	ID              string     `gorm:"primaryKey;column:id;size:36"`
	NamespaceID     string     `gorm:"column:namespace_id;size:36;not null"`
	ClusterID       string     `gorm:"column:cluster_id;size:36;not null"`
	OrgID           string     `gorm:"column:org_id;size:128;not null"`
	Name            string     `gorm:"column:name;size:128;not null"`
	KubernetesName  string     `gorm:"column:kubernetes_name;size:128;not null"`
	KubernetesUID   string     `gorm:"column:kubernetes_uid;size:128;not null;default:''"`
	ResourceVersion string     `gorm:"column:resource_version;size:128;not null;default:''"`
	WarmPoolID      string     `gorm:"column:warm_pool_id;size:36;not null"`
	SandboxID       *string    `gorm:"column:sandbox_id;size:36"`
	SandboxKubeName string     `gorm:"column:sandbox_kube_name;size:128;not null;default:''"`
	SandboxPodIP    string     `gorm:"column:sandbox_pod_ip;size:64;not null;default:''"`
	Status          string     `gorm:"column:status;size:32;not null;default:'PENDING'"`
	HealthMessage   string     `gorm:"column:health_message;type:text;not null;default:''"`
	LastSyncAt      *time.Time `gorm:"column:last_sync_at"`
	OwnerType       string     `gorm:"column:owner_type;size:32;not null"`
	OwnerID         string     `gorm:"column:owner_id;size:128;not null"`
	CreatedByType   string     `gorm:"column:created_by_type;size:32;not null"`
	CreatedBy       string     `gorm:"column:created_by;size:128;not null"`
	CreatedAt       time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt       *time.Time `gorm:"column:deleted_at"`
	Revision        int64      `gorm:"column:revision;not null;default:1"`
}

func (k8sSandboxClaimModel) TableName() string { return "k8s_sandbox_claims" }

// --- SandboxTemplate CRUD ---

func (r *sandboxRepo) CreateSandboxTemplate(ctx context.Context, t *biz.SandboxTemplate) (*biz.SandboxTemplate, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	if t == nil {
		return nil, errors.New("sandbox repo: nil sandbox template")
	}
	if t.ID == "" {
		t.ID = r.newID()
	}
	if t.Status == "" {
		t.Status = "CREATING"
	}
	if t.KubernetesNamespace == "" {
		t.KubernetesNamespace = "agent-sandbox-system"
	}
	if t.Revision == 0 {
		t.Revision = 1
	}
	row, err := sandboxTemplateModelFromBiz(t)
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: create template %s: %w", t.ID, err)
	}
	return sandboxTemplateModelToBiz(row), nil
}

func (r *sandboxRepo) GetSandboxTemplate(ctx context.Context, id string) (*biz.SandboxTemplate, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var row k8sSandboxTemplateModel
	err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrSandboxTemplateNotFound
		}
		return nil, fmt.Errorf("sandbox repo: get template %s: %w", id, err)
	}
	return sandboxTemplateModelToBiz(row), nil
}

func (r *sandboxRepo) ListSandboxTemplatesByCluster(ctx context.Context, clusterID string) ([]*biz.SandboxTemplate, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var rows []k8sSandboxTemplateModel
	if err := db.WithContext(ctx).
		Where("cluster_id = ? AND deleted_at IS NULL", clusterID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: list templates by cluster: %w", err)
	}
	out := make([]*biz.SandboxTemplate, len(rows))
	for i, row := range rows {
		out[i] = sandboxTemplateModelToBiz(row)
	}
	return out, nil
}

// DeleteSandboxTemplate soft-deletes (sets deleted_at + status=DELETED) guarded
// by expected_revision (design §11). RowsAffected==0 disambiguates NotFound vs
// RevisionConflict by re-reading the row.
func (r *sandboxRepo) DeleteSandboxTemplate(ctx context.Context, id string, expectedRevision int64) (*biz.SandboxTemplate, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	now := r.now()
	var deleted *biz.SandboxTemplate
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxTemplateModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(map[string]any{
				"deleted_at":  now,
				"status":      "DELETED",
				"updated_at":  now,
				"revision":    gorm.Expr("revision + 1"),
			})
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: delete template %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return r.classifySandboxTemplateConflict(tx, id)
		}
		var row k8sSandboxTemplateModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		deleted = sandboxTemplateModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// UpdateSandboxTemplateStatus stamps status + health_message + revision++.
func (r *sandboxRepo) UpdateSandboxTemplateStatus(ctx context.Context, id, status, healthMessage string) (*biz.SandboxTemplate, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var updated *biz.SandboxTemplate
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxTemplateModel{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(map[string]any{
				"status":         status,
				"health_message": healthMessage,
				"updated_at":     r.now(),
				"revision":       gorm.Expr("revision + 1"),
			})
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: update template status %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrSandboxTemplateNotFound
		}
		var row k8sSandboxTemplateModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = sandboxTemplateModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// --- Sandbox CRUD ---

func (r *sandboxRepo) CreateSandbox(ctx context.Context, s *biz.Sandbox) (*biz.Sandbox, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	if s == nil {
		return nil, errors.New("sandbox repo: nil sandbox")
	}
	if s.ID == "" {
		s.ID = r.newID()
	}
	if s.Lifecycle == "" {
		s.Lifecycle = "CREATING"
	}
	if s.OperatingMode == "" {
		s.OperatingMode = "RUNNING"
	}
	if s.NetworkMode == "" {
		s.NetworkMode = "OFFLINE"
	}
	if s.Revision == 0 {
		s.Revision = 1
	}
	row, err := sandboxModelFromBiz(s)
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: create sandbox %s: %w", s.ID, err)
	}
	return sandboxModelToBiz(row), nil
}

func (r *sandboxRepo) GetSandbox(ctx context.Context, id string) (*biz.Sandbox, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var row k8sSandboxModel
	err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrSandboxNotFound
		}
		return nil, fmt.Errorf("sandbox repo: get sandbox %s: %w", id, err)
	}
	return sandboxModelToBiz(row), nil
}

func (r *sandboxRepo) ListSandboxesByNamespace(ctx context.Context, namespaceID string) ([]*biz.Sandbox, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var rows []k8sSandboxModel
	if err := db.WithContext(ctx).
		Where("namespace_id = ? AND deleted_at IS NULL", namespaceID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: list sandboxes by namespace: %w", err)
	}
	out := make([]*biz.Sandbox, len(rows))
	for i, row := range rows {
		out[i] = sandboxModelToBiz(row)
	}
	return out, nil
}

func (r *sandboxRepo) ListSandboxesByCluster(ctx context.Context, clusterID string) ([]*biz.Sandbox, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var rows []k8sSandboxModel
	if err := db.WithContext(ctx).
		Where("cluster_id = ? AND deleted_at IS NULL", clusterID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: list sandboxes by cluster: %w", err)
	}
	out := make([]*biz.Sandbox, len(rows))
	for i, row := range rows {
		out[i] = sandboxModelToBiz(row)
	}
	return out, nil
}

// DeleteSandbox soft-deletes guarded by expected_revision (design §11).
func (r *sandboxRepo) DeleteSandbox(ctx context.Context, id string, expectedRevision int64) (*biz.Sandbox, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	now := r.now()
	var deleted *biz.Sandbox
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(map[string]any{
				"deleted_at": now,
				"lifecycle":  "DELETED",
				"updated_at": now,
				"revision":   gorm.Expr("revision + 1"),
			})
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: delete sandbox %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return r.classifySandboxConflict(tx, id)
		}
		var row k8sSandboxModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		deleted = sandboxModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// UpdateSandboxStatus is the lifecycle state-machine write (design §11): sets
// lifecycle + health_message plus caller-supplied observed fields, revision++.
// RowsAffected==0 → ErrSandboxNotFound.
func (r *sandboxRepo) UpdateSandboxStatus(ctx context.Context, id, lifecycle, healthMessage string, fields map[string]any) (*biz.Sandbox, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	updates := map[string]any{
		"lifecycle":      lifecycle,
		"health_message": healthMessage,
		"updated_at":     r.now(),
		"revision":       gorm.Expr("revision + 1"),
	}
	if err := applySandboxFieldUpdates(updates, fields); err != nil {
		return nil, err
	}
	var updated *biz.Sandbox
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxModel{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: update sandbox status %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrSandboxNotFound
		}
		var row k8sSandboxModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = sandboxModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateSandboxSync stamps reconciler-observed fields (kubernetes_uid,
// resource_version, pod_*, last_sync_at, labels, …) + revision++ without
// changing lifecycle. RowsAffected==0 → ErrSandboxNotFound.
func (r *sandboxRepo) UpdateSandboxSync(ctx context.Context, id string, fields map[string]any) (*biz.Sandbox, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	if len(fields) == 0 {
		return r.GetSandbox(ctx, id)
	}
	updates := map[string]any{
		"updated_at": r.now(),
		"revision":   gorm.Expr("revision + 1"),
	}
	if err := applySandboxFieldUpdates(updates, fields); err != nil {
		return nil, err
	}
	var updated *biz.Sandbox
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxModel{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: update sandbox sync %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrSandboxNotFound
		}
		var row k8sSandboxModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = sandboxModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// --- WarmPool CRUD ---

func (r *sandboxRepo) CreateWarmPool(ctx context.Context, w *biz.WarmPool) (*biz.WarmPool, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	if w == nil {
		return nil, errors.New("sandbox repo: nil warm pool")
	}
	if w.ID == "" {
		w.ID = r.newID()
	}
	if w.Status == "" {
		w.Status = "CREATING"
	}
	if w.Replicas < 0 {
		w.Replicas = 1
	}
	if w.Revision == 0 {
		w.Revision = 1
	}
	row := warmPoolModelFromBiz(w)
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: create warm pool %s: %w", w.ID, err)
	}
	return warmPoolModelToBiz(row), nil
}

func (r *sandboxRepo) GetWarmPool(ctx context.Context, id string) (*biz.WarmPool, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var row k8sSandboxWarmPoolModel
	err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrWarmPoolNotFound
		}
		return nil, fmt.Errorf("sandbox repo: get warm pool %s: %w", id, err)
	}
	return warmPoolModelToBiz(row), nil
}

func (r *sandboxRepo) ListWarmPoolsByNamespace(ctx context.Context, namespaceID string) ([]*biz.WarmPool, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var rows []k8sSandboxWarmPoolModel
	if err := db.WithContext(ctx).
		Where("namespace_id = ? AND deleted_at IS NULL", namespaceID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: list warm pools by namespace: %w", err)
	}
	out := make([]*biz.WarmPool, len(rows))
	for i, row := range rows {
		out[i] = warmPoolModelToBiz(row)
	}
	return out, nil
}

func (r *sandboxRepo) ListWarmPoolsByCluster(ctx context.Context, clusterID string) ([]*biz.WarmPool, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var rows []k8sSandboxWarmPoolModel
	if err := db.WithContext(ctx).
		Where("cluster_id = ? AND deleted_at IS NULL", clusterID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: list warm pools by cluster: %w", err)
	}
	out := make([]*biz.WarmPool, len(rows))
	for i, row := range rows {
		out[i] = warmPoolModelToBiz(row)
	}
	return out, nil
}

// DeleteWarmPool soft-deletes guarded by expected_revision (design §11).
func (r *sandboxRepo) DeleteWarmPool(ctx context.Context, id string, expectedRevision int64) (*biz.WarmPool, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	now := r.now()
	var deleted *biz.WarmPool
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxWarmPoolModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(map[string]any{
				"deleted_at": now,
				"status":     "DELETED",
				"updated_at": now,
				"revision":   gorm.Expr("revision + 1"),
			})
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: delete warm pool %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return r.classifyWarmPoolConflict(tx, id)
		}
		var row k8sSandboxWarmPoolModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		deleted = warmPoolModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// UpdateWarmPoolStatus stamps status plus caller-supplied observed fields
// (replicas, ready_replicas, last_sync_at, …) + revision++.
func (r *sandboxRepo) UpdateWarmPoolStatus(ctx context.Context, id, status string, fields map[string]any) (*biz.WarmPool, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	updates := map[string]any{
		"status":     status,
		"updated_at": r.now(),
		"revision":   gorm.Expr("revision + 1"),
	}
	if err := applySandboxFieldUpdates(updates, fields); err != nil {
		return nil, err
	}
	var updated *biz.WarmPool
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxWarmPoolModel{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: update warm pool status %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrWarmPoolNotFound
		}
		var row k8sSandboxWarmPoolModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = warmPoolModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateWarmPoolSync stamps reconciler-observed fields (kubernetes_uid,
// resource_version, ready_replicas, health_message, last_sync_at, labels, …)
// + revision++ without changing status. RowsAffected==0 → ErrWarmPoolNotFound.
func (r *sandboxRepo) UpdateWarmPoolSync(ctx context.Context, id string, fields map[string]any) (*biz.WarmPool, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	if len(fields) == 0 {
		return r.GetWarmPool(ctx, id)
	}
	updates := map[string]any{
		"updated_at": r.now(),
		"revision":   gorm.Expr("revision + 1"),
	}
	if err := applySandboxFieldUpdates(updates, fields); err != nil {
		return nil, err
	}
	var updated *biz.WarmPool
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxWarmPoolModel{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: update warm pool sync %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrWarmPoolNotFound
		}
		var row k8sSandboxWarmPoolModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = warmPoolModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// --- SandboxClaim CRUD ---

func (r *sandboxRepo) CreateSandboxClaim(ctx context.Context, c *biz.SandboxClaim) (*biz.SandboxClaim, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	if c == nil {
		return nil, errors.New("sandbox repo: nil sandbox claim")
	}
	if c.ID == "" {
		c.ID = r.newID()
	}
	if c.Status == "" {
		c.Status = "PENDING"
	}
	if c.Revision == 0 {
		c.Revision = 1
	}
	row := sandboxClaimModelFromBiz(c)
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: create sandbox claim %s: %w", c.ID, err)
	}
	return sandboxClaimModelToBiz(row), nil
}

func (r *sandboxRepo) GetSandboxClaim(ctx context.Context, id string) (*biz.SandboxClaim, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var row k8sSandboxClaimModel
	err := db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrSandboxClaimNotFound
		}
		return nil, fmt.Errorf("sandbox repo: get sandbox claim %s: %w", id, err)
	}
	return sandboxClaimModelToBiz(row), nil
}

func (r *sandboxRepo) ListSandboxClaimsByNamespace(ctx context.Context, namespaceID string) ([]*biz.SandboxClaim, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var rows []k8sSandboxClaimModel
	if err := db.WithContext(ctx).
		Where("namespace_id = ? AND deleted_at IS NULL", namespaceID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: list sandbox claims by namespace: %w", err)
	}
	out := make([]*biz.SandboxClaim, len(rows))
	for i, row := range rows {
		out[i] = sandboxClaimModelToBiz(row)
	}
	return out, nil
}

func (r *sandboxRepo) ListSandboxClaimsByCluster(ctx context.Context, clusterID string) ([]*biz.SandboxClaim, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	var rows []k8sSandboxClaimModel
	if err := db.WithContext(ctx).
		Where("cluster_id = ? AND deleted_at IS NULL", clusterID).
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("sandbox repo: list sandbox claims by cluster: %w", err)
	}
	out := make([]*biz.SandboxClaim, len(rows))
	for i, row := range rows {
		out[i] = sandboxClaimModelToBiz(row)
	}
	return out, nil
}

// DeleteSandboxClaim soft-deletes guarded by expected_revision (design §11).
func (r *sandboxRepo) DeleteSandboxClaim(ctx context.Context, id string, expectedRevision int64) (*biz.SandboxClaim, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	now := r.now()
	var deleted *biz.SandboxClaim
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxClaimModel{}).
			Where("id = ? AND revision = ? AND deleted_at IS NULL", id, expectedRevision).
			Updates(map[string]any{
				"deleted_at": now,
				"status":     "DELETED",
				"updated_at": now,
				"revision":   gorm.Expr("revision + 1"),
			})
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: delete sandbox claim %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return r.classifySandboxClaimConflict(tx, id)
		}
		var row k8sSandboxClaimModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		deleted = sandboxClaimModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// UpdateSandboxClaimStatus stamps status plus caller-supplied observed fields
// (sandbox_id, sandbox_kube_name, sandbox_pod_ip, last_sync_at, …) + revision++.
func (r *sandboxRepo) UpdateSandboxClaimStatus(ctx context.Context, id, status string, fields map[string]any) (*biz.SandboxClaim, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	updates := map[string]any{
		"status":     status,
		"updated_at": r.now(),
		"revision":   gorm.Expr("revision + 1"),
	}
	if err := applySandboxFieldUpdates(updates, fields); err != nil {
		return nil, err
	}
	var updated *biz.SandboxClaim
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxClaimModel{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: update sandbox claim status %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrSandboxClaimNotFound
		}
		var row k8sSandboxClaimModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = sandboxClaimModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateSandboxClaimSync stamps reconciler-observed fields (kubernetes_uid,
// resource_version, sandbox_kube_name, sandbox_pod_ip, sandbox_id, status,
// last_sync_at, …) + revision++ without the status-machine guard.
// RowsAffected==0 → ErrSandboxClaimNotFound.
func (r *sandboxRepo) UpdateSandboxClaimSync(ctx context.Context, id string, fields map[string]any) (*biz.SandboxClaim, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("sandbox repo: database not configured")
	}
	if len(fields) == 0 {
		return r.GetSandboxClaim(ctx, id)
	}
	updates := map[string]any{
		"updated_at": r.now(),
		"revision":   gorm.Expr("revision + 1"),
	}
	if err := applySandboxFieldUpdates(updates, fields); err != nil {
		return nil, err
	}
	var updated *biz.SandboxClaim
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&k8sSandboxClaimModel{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("sandbox repo: update sandbox claim sync %s: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrSandboxClaimNotFound
		}
		var row k8sSandboxClaimModel
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		updated = sandboxClaimModelToBiz(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// --- conflict classification helpers ---
//
// When a CAS delete/update matches zero rows the cause is either (a) the row
// is missing/already-deleted → NotFound, or (b) the revision changed →
// RevisionConflict. Each helper re-reads the row ignoring revision to decide.

func (r *sandboxRepo) classifySandboxTemplateConflict(tx *gorm.DB, id string) error {
	var exist k8sSandboxTemplateModel
	err := tx.Where("id = ?", id).First(&exist).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return biz.ErrSandboxTemplateNotFound
		}
		return err
	}
	if exist.DeletedAt != nil {
		return biz.ErrSandboxTemplateNotFound
	}
	return biz.ErrClusterRevisionConflict
}

func (r *sandboxRepo) classifySandboxConflict(tx *gorm.DB, id string) error {
	var exist k8sSandboxModel
	err := tx.Where("id = ?", id).First(&exist).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return biz.ErrSandboxNotFound
		}
		return err
	}
	if exist.DeletedAt != nil {
		return biz.ErrSandboxNotFound
	}
	return biz.ErrClusterRevisionConflict
}

func (r *sandboxRepo) classifyWarmPoolConflict(tx *gorm.DB, id string) error {
	var exist k8sSandboxWarmPoolModel
	err := tx.Where("id = ?", id).First(&exist).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return biz.ErrWarmPoolNotFound
		}
		return err
	}
	if exist.DeletedAt != nil {
		return biz.ErrWarmPoolNotFound
	}
	return biz.ErrClusterRevisionConflict
}

func (r *sandboxRepo) classifySandboxClaimConflict(tx *gorm.DB, id string) error {
	var exist k8sSandboxClaimModel
	err := tx.Where("id = ?", id).First(&exist).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return biz.ErrSandboxClaimNotFound
		}
		return err
	}
	if exist.DeletedAt != nil {
		return biz.ErrSandboxClaimNotFound
	}
	return biz.ErrClusterRevisionConflict
}

// applySandboxFieldUpdates merges caller-supplied observed fields into the
// GORM updates map, translating the two special-cased keys:
//   - "labels" (map[string]string) → marshaled to labels_json jsonb;
//   - "last_sync_at" (time.Time / *time.Time) → nil when zero, else UTC.
//
// All other keys are treated as raw column names and passed through.
func applySandboxFieldUpdates(updates map[string]any, fields map[string]any) error {
	for k, v := range fields {
		switch k {
		case "labels":
			if m, ok := v.(map[string]string); ok {
				b, err := json.Marshal(m)
				if err != nil {
					return fmt.Errorf("sandbox repo: marshal labels: %w", err)
				}
				updates["labels_json"] = b
				continue
			}
			// A pre-marshaled json.RawMessage / []byte is accepted as-is.
			if _, ok := v.(json.RawMessage); ok {
				updates["labels_json"] = v
				continue
			}
			updates["labels_json"] = v
			continue
		case "last_sync_at":
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
			updates[k] = v
			continue
		}
		updates[k] = v
	}
	return nil
}

// --- model <-> biz ---

func sandboxTemplateModelToBiz(m k8sSandboxTemplateModel) *biz.SandboxTemplate {
	var labels map[string]string
	if len(m.LabelsJSON) > 0 {
		_ = json.Unmarshal(m.LabelsJSON, &labels)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return &biz.SandboxTemplate{
		ID:                  m.ID,
		ClusterID:           m.ClusterID,
		OrgID:               m.OrgID,
		Name:                m.Name,
		DisplayName:         m.DisplayName,
		Description:         m.Description,
		KubernetesName:      m.KubernetesName,
		KubernetesNamespace: m.KubernetesNamespace,
		KubernetesUID:       m.KubernetesUID,
		ResourceVersion:     m.ResourceVersion,
		Image:               m.Image,
		ContainerCommand:    m.ContainerCommand,
		Labels:              labels,
		Status:              m.Status,
		HealthMessage:       m.HealthMessage,
		OwnerType:           m.OwnerType,
		OwnerID:             m.OwnerID,
		CreatedByType:       m.CreatedByType,
		CreatedBy:           m.CreatedBy,
		CreatedAt:           m.CreatedAt,
		UpdatedAt:           m.UpdatedAt,
		Revision:            m.Revision,
	}
}

func sandboxTemplateModelFromBiz(t *biz.SandboxTemplate) (k8sSandboxTemplateModel, error) {
	labels := t.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return k8sSandboxTemplateModel{}, fmt.Errorf("marshal labels: %w", err)
	}
	return k8sSandboxTemplateModel{
		ID:                  t.ID,
		ClusterID:           t.ClusterID,
		OrgID:               t.OrgID,
		Name:                t.Name,
		DisplayName:         t.DisplayName,
		Description:         t.Description,
		KubernetesName:      t.KubernetesName,
		KubernetesNamespace: t.KubernetesNamespace,
		KubernetesUID:       t.KubernetesUID,
		ResourceVersion:     t.ResourceVersion,
		Image:               t.Image,
		ContainerCommand:    t.ContainerCommand,
		LabelsJSON:          labelsJSON,
		Status:              t.Status,
		HealthMessage:       t.HealthMessage,
		OwnerType:           t.OwnerType,
		OwnerID:             t.OwnerID,
		CreatedByType:       t.CreatedByType,
		CreatedBy:           t.CreatedBy,
		Revision:            t.Revision,
	}, nil
}

func sandboxModelToBiz(m k8sSandboxModel) *biz.Sandbox {
	var labels map[string]string
	if len(m.LabelsJSON) > 0 {
		_ = json.Unmarshal(m.LabelsJSON, &labels)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	var lastSync time.Time
	if m.LastSyncAt != nil {
		lastSync = *m.LastSyncAt
	}
	s := &biz.Sandbox{
		ID:              m.ID,
		NamespaceID:     m.NamespaceID,
		ClusterID:       m.ClusterID,
		OrgID:           m.OrgID,
		Name:            m.Name,
		KubernetesName:  m.KubernetesName,
		KubernetesUID:   m.KubernetesUID,
		ResourceVersion: m.ResourceVersion,
		Lifecycle:       m.Lifecycle,
		OperatingMode:   m.OperatingMode,
		PodName:         m.PodName,
		PodIP:           m.PodIP,
		NodeName:        m.NodeName,
		Image:           m.Image,
		WorkspacePVC:    m.WorkspacePVC,
		NetworkMode:     m.NetworkMode,
		Labels:          labels,
		HealthMessage:   m.HealthMessage,
		LastSyncAt:      lastSync,
		OwnerType:       m.OwnerType,
		OwnerID:         m.OwnerID,
		CreatedByType:   m.CreatedByType,
		CreatedBy:       m.CreatedBy,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
		Revision:        m.Revision,
	}
	if m.TemplateID != nil {
		s.TemplateID = *m.TemplateID
	}
	if m.WarmPoolID != nil {
		s.WarmPoolID = *m.WarmPoolID
	}
	if m.ClaimID != nil {
		s.ClaimID = *m.ClaimID
	}
	return s
}

func sandboxModelFromBiz(s *biz.Sandbox) (k8sSandboxModel, error) {
	labels := s.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return k8sSandboxModel{}, fmt.Errorf("marshal labels: %w", err)
	}
	var lastSync *time.Time
	if !s.LastSyncAt.IsZero() {
		t := s.LastSyncAt.UTC()
		lastSync = &t
	}
	row := k8sSandboxModel{
		ID:              s.ID,
		NamespaceID:     s.NamespaceID,
		ClusterID:       s.ClusterID,
		OrgID:           s.OrgID,
		Name:            s.Name,
		KubernetesName:  s.KubernetesName,
		KubernetesUID:   s.KubernetesUID,
		ResourceVersion: s.ResourceVersion,
		Lifecycle:       s.Lifecycle,
		OperatingMode:   s.OperatingMode,
		PodName:         s.PodName,
		PodIP:           s.PodIP,
		NodeName:        s.NodeName,
		Image:           s.Image,
		WorkspacePVC:    s.WorkspacePVC,
		NetworkMode:     s.NetworkMode,
		LabelsJSON:      labelsJSON,
		HealthMessage:   s.HealthMessage,
		LastSyncAt:      lastSync,
		OwnerType:       s.OwnerType,
		OwnerID:         s.OwnerID,
		CreatedByType:   s.CreatedByType,
		CreatedBy:       s.CreatedBy,
		Revision:        s.Revision,
	}
	if s.TemplateID != "" {
		row.TemplateID = &s.TemplateID
	}
	if s.WarmPoolID != "" {
		row.WarmPoolID = &s.WarmPoolID
	}
	if s.ClaimID != "" {
		row.ClaimID = &s.ClaimID
	}
	return row, nil
}

func warmPoolModelToBiz(m k8sSandboxWarmPoolModel) *biz.WarmPool {
	var lastSync time.Time
	if m.LastSyncAt != nil {
		lastSync = *m.LastSyncAt
	}
	return &biz.WarmPool{
		ID:              m.ID,
		NamespaceID:     m.NamespaceID,
		ClusterID:       m.ClusterID,
		OrgID:           m.OrgID,
		Name:            m.Name,
		KubernetesName:  m.KubernetesName,
		KubernetesUID:   m.KubernetesUID,
		ResourceVersion: m.ResourceVersion,
		TemplateID:      m.TemplateID,
		Replicas:        m.Replicas,
		ReadyReplicas:   m.ReadyReplicas,
		Status:          m.Status,
		HealthMessage:   m.HealthMessage,
		LastSyncAt:      lastSync,
		OwnerType:       m.OwnerType,
		OwnerID:         m.OwnerID,
		CreatedByType:   m.CreatedByType,
		CreatedBy:       m.CreatedBy,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
		Revision:        m.Revision,
	}
}

func warmPoolModelFromBiz(w *biz.WarmPool) k8sSandboxWarmPoolModel {
	var lastSync *time.Time
	if !w.LastSyncAt.IsZero() {
		t := w.LastSyncAt.UTC()
		lastSync = &t
	}
	return k8sSandboxWarmPoolModel{
		ID:              w.ID,
		NamespaceID:     w.NamespaceID,
		ClusterID:       w.ClusterID,
		OrgID:           w.OrgID,
		Name:            w.Name,
		KubernetesName:  w.KubernetesName,
		KubernetesUID:   w.KubernetesUID,
		ResourceVersion: w.ResourceVersion,
		TemplateID:      w.TemplateID,
		Replicas:        w.Replicas,
		ReadyReplicas:   w.ReadyReplicas,
		Status:          w.Status,
		HealthMessage:   w.HealthMessage,
		LastSyncAt:      lastSync,
		OwnerType:       w.OwnerType,
		OwnerID:         w.OwnerID,
		CreatedByType:   w.CreatedByType,
		CreatedBy:       w.CreatedBy,
		Revision:        w.Revision,
	}
}

func sandboxClaimModelToBiz(m k8sSandboxClaimModel) *biz.SandboxClaim {
	var lastSync time.Time
	if m.LastSyncAt != nil {
		lastSync = *m.LastSyncAt
	}
	c := &biz.SandboxClaim{
		ID:              m.ID,
		NamespaceID:     m.NamespaceID,
		ClusterID:       m.ClusterID,
		OrgID:           m.OrgID,
		Name:            m.Name,
		KubernetesName:  m.KubernetesName,
		KubernetesUID:   m.KubernetesUID,
		ResourceVersion: m.ResourceVersion,
		WarmPoolID:      m.WarmPoolID,
		SandboxKubeName: m.SandboxKubeName,
		SandboxPodIP:    m.SandboxPodIP,
		Status:          m.Status,
		HealthMessage:   m.HealthMessage,
		LastSyncAt:      lastSync,
		OwnerType:       m.OwnerType,
		OwnerID:         m.OwnerID,
		CreatedByType:   m.CreatedByType,
		CreatedBy:       m.CreatedBy,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
		Revision:        m.Revision,
	}
	if m.SandboxID != nil {
		c.SandboxID = *m.SandboxID
	}
	return c
}

func sandboxClaimModelFromBiz(c *biz.SandboxClaim) k8sSandboxClaimModel {
	var lastSync *time.Time
	if !c.LastSyncAt.IsZero() {
		t := c.LastSyncAt.UTC()
		lastSync = &t
	}
	row := k8sSandboxClaimModel{
		ID:              c.ID,
		NamespaceID:     c.NamespaceID,
		ClusterID:       c.ClusterID,
		OrgID:           c.OrgID,
		Name:            c.Name,
		KubernetesName:  c.KubernetesName,
		KubernetesUID:   c.KubernetesUID,
		ResourceVersion: c.ResourceVersion,
		WarmPoolID:      c.WarmPoolID,
		SandboxKubeName: c.SandboxKubeName,
		SandboxPodIP:    c.SandboxPodIP,
		Status:          c.Status,
		HealthMessage:   c.HealthMessage,
		LastSyncAt:      lastSync,
		OwnerType:       c.OwnerType,
		OwnerID:         c.OwnerID,
		CreatedByType:   c.CreatedByType,
		CreatedBy:       c.CreatedBy,
		Revision:        c.Revision,
	}
	if c.SandboxID != "" {
		row.SandboxID = &c.SandboxID
	}
	return row
}

var _ biz.SandboxRepository = (*sandboxRepo)(nil)
