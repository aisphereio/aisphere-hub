package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"gorm.io/gorm"
)

var skillSetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type skillSetHTTPHandler struct {
	resources *data.Resources
}

type skillSetRow struct {
	ID          int64            `gorm:"column:id" json:"-"`
	Name        string           `gorm:"column:name" json:"name"`
	DisplayName string           `gorm:"column:display_name" json:"displayName,omitempty"`
	Description string           `gorm:"column:description" json:"description,omitempty"`
	Visibility  string           `gorm:"column:visibility" json:"scope"`
	OwnerID     string           `gorm:"column:owner_id" json:"owner,omitempty"`
	OrgID       string           `gorm:"column:org_id" json:"orgId,omitempty"`
	CreatedAt   time.Time        `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt   time.Time        `gorm:"column:updated_at" json:"updatedAt"`
	DeletedAt   *time.Time       `gorm:"column:deleted_at" json:"-"`
	Members     []skillSetMember `gorm:"-" json:"members,omitempty"`
}

func (skillSetRow) TableName() string { return "aihub_skillsets" }

type skillSetMember struct {
	SkillName   string `gorm:"column:skill_name" json:"skillName"`
	Order       int    `gorm:"column:sort_order" json:"order"`
	Version     string `gorm:"column:version" json:"version,omitempty"`
	DisplayName string `gorm:"column:display_name" json:"displayName,omitempty"`
}

type skillSetWriteRequest struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"displayName"`
	Description string           `json:"description"`
	Scope       string           `json:"scope"`
	Members     []skillSetMember `json:"members"`
}

// registerSecuredSkillSetHTTP keeps SkillSet as a lightweight PostgreSQL-only
// Hub resource while putting its handwritten HTTP handlers on the same trusted
// principal and IAM authorization path as generated services.
//
// Routes are registered through srv.Route() + r.Handle() so that each handler
// runs inside the Kernel middleware chain via ctx.Middleware() (see
// transportx/http/context.go). This is what makes authn (gateway_trusted claim
// headers → PrincipalFromContext) actually populate the request context. The
// previous srv.HandleFunc() registration bypassed the middleware matcher
// entirely, so PrincipalFromContext always returned anonymous and every
// authenticated skillset write failed with UNAUTHENTICATED.
func registerSecuredSkillSetHTTP(srv *khttp.Server, resources *data.Resources) {
	if srv == nil || resources == nil || resources.DB == nil {
		return
	}
	h := &skillSetHTTPHandler{resources: resources}
	r := srv.Route("/")
	r.Handle(http.MethodGet, "/v1/skillsets", h.listEndpoint)
	r.Handle(http.MethodPost, "/v1/skillsets", h.createEndpoint)
	r.Handle(http.MethodGet, "/v1/skillsets/{name}", h.getEndpoint)
	r.Handle(http.MethodPut, "/v1/skillsets/{name}", h.updateEndpoint)
	r.Handle(http.MethodDelete, "/v1/skillsets/{name}", h.removeEndpoint)
	r.Handle(http.MethodPost, "/v1/skillsets/{name}/members", h.bindEndpoint)
	r.Handle(http.MethodPut, "/v1/skillsets/{name}/members/{skill}", h.updateMemberEndpoint)
	r.Handle(http.MethodDelete, "/v1/skillsets/{name}/members/{skill}", h.unbindEndpoint)
	r.Handle(http.MethodGet, "/v1/skills/{name}/skillsets", h.reverseLookupEndpoint)
}

func (h *skillSetHTTPHandler) db(ctx context.Context) *gorm.DB {
	return h.resources.DB.GORM(ctx)
}

// ─── Helpers ──────────────────────────────────────────────────────────────

// principalFromCtx reads the Kernel-authenticated principal from the request
// context. Returns anonymous + false when authn middleware did not authenticate
// the caller (e.g. missing gateway claim headers).
func principalFromCtx(ctx context.Context) (authn.Principal, bool) {
	return authn.PrincipalFromContext(ctx)
}

// requireOwnerPrincipal ensures the caller is authenticated and owns the named
// skillset. It mirrors the previous requireOwner but reads identity from the
// context principal instead of client-supplied headers.
func (h *skillSetHTTPHandler) requireOwnerPrincipal(ctx context.Context, name string) error {
	principal, ok := principalFromCtx(ctx)
	if !ok {
		return errSkillSetUnauthenticated
	}
	var count int64
	err := h.db(ctx).Model(&skillSetRow{}).Where("name = ? AND owner_id = ? AND deleted_at IS NULL", name, principal.SubjectID).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return errSkillSetForbidden
	}
	return nil
}

// allowCreate enforces Zone membership for skillset creation: the authenticated
// principal must hold the create_skill permission on its zone. This preserves
// the authorization contract previously enforced by withSkillSetSecurity.
func (h *skillSetHTTPHandler) allowCreate(ctx context.Context, principal authn.Principal) bool {
	if strings.TrimSpace(principal.OrgID) == "" {
		return false
	}
	if h.resources.Authz == nil {
		return false
	}
	subjectType := principal.SubjectType
	if strings.TrimSpace(subjectType) == "" {
		subjectType = authz.SubjectTypeUser
	}
	decision, err := h.resources.Authz.Check(ctx, authz.CheckRequest{
		Subject: authz.SubjectRef{
			Type: subjectType,
			ID:   principal.SubjectID,
		},
		Resource: authz.ObjectRef{
			Type: "zone",
			ID:   principal.OrgID,
		},
		Permission: "create_skill",
		OrgID:      principal.OrgID,
	})
	if err != nil || !decision.IsAllowed() {
		return false
	}
	return true
}

func (h *skillSetHTTPHandler) members(ctx context.Context, name string) ([]skillSetMember, error) {
	var members []skillSetMember
	err := h.db(ctx).Raw(`
		SELECT i.skill_name, i.sort_order, '' AS version,
		       COALESCE(p.display_name, '') AS display_name
		FROM aihub_skillset_items i
		JOIN repos r ON r.name = i.skill_name
		JOIN hub_skill_profiles p ON p.repository_id = r.id
		WHERE i.skillset_name = ? AND p.lifecycle_status = 'active'
		ORDER BY i.sort_order ASC, i.skill_name ASC`, name).Scan(&members).Error
	return members, err
}

func (h *skillSetHTTPHandler) visibleSet(ctx context.Context, name string) (*skillSetRow, error) {
	principal, _ := principalFromCtx(ctx)
	var row skillSetRow
	err := h.db(ctx).Where("name = ? AND deleted_at IS NULL", name).
		Where("visibility = 'public' OR owner_id = ? OR (visibility = 'internal' AND org_id <> '' AND org_id = ?)", principal.SubjectID, principal.OrgID).
		First(&row).Error
	return &row, err
}

func replaceSkillSetMembers(tx *gorm.DB, name string, members []skillSetMember) error {
	if err := tx.Exec("DELETE FROM aihub_skillset_items WHERE skillset_name = ?", name).Error; err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(members))
	for index, member := range members {
		member.SkillName = strings.TrimSpace(member.SkillName)
		if !skillSetNameRE.MatchString(member.SkillName) {
			return errors.New("invalid skillName in members")
		}
		if _, ok := seen[member.SkillName]; ok {
			continue
		}
		seen[member.SkillName] = struct{}{}
		order := member.Order
		if order == 0 && index > 0 {
			order = index
		}
		result := tx.Exec(`
			INSERT INTO aihub_skillset_items(skillset_name, skill_name, sort_order)
			SELECT ?, r.name, ?
			FROM repos r
			JOIN hub_skill_profiles p ON p.repository_id = r.id
			WHERE r.name = ? AND p.lifecycle_status = 'active'`, name, order, member.SkillName)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	return nil
}

func normalizeVisibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "public":
		return "public"
	case "internal":
		return "internal"
	default:
		return "private"
	}
}

func positiveInt(value string, fallback int) int {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// ─── Error sentinels (mapped to errorx for transport encoding) ────────────

var (
	errSkillSetUnauthenticated = errorx.Unauthorized("UNAUTHENTICATED", "authentication required")
	errSkillSetForbidden       = errorx.Forbidden("SKILLSET_PERMISSION_DENIED", "zone membership is required to create a skillset")
	errSkillSetZoneRequired    = errorx.BadRequest("SKILLSET_ZONE_REQUIRED", "authenticated principal has no zone")
	errSkillSetAuthzUnavailable = errorx.New("SKILLSET_AUTHZ_UNAVAILABLE", errorx.WithHTTPStatus(http.StatusServiceUnavailable), errorx.WithMessage("authorization service is unavailable"))
	errSkillSetInvalidName     = errorx.BadRequest("SKILLSET_INVALID_NAME", "invalid skillset name")
	errSkillSetMemberInvalid   = errorx.BadRequest("SKILLSET_MEMBER_INVALID", "valid skillName is required")
)

// skillSetDecodeErr wraps a JSON decode failure as a SKILLSET_INVALID_ARGUMENT
// error so the frontend receives the same business code as before.
func skillSetDecodeErr(err error) error {
	return errorx.BadRequest("SKILLSET_INVALID_ARGUMENT", err.Error(), errorx.WithCause(err))
}

// skillSetDBErr translates a gorm/database error into the skillset business
// error codes the frontend expects (not found, duplicate, foreign key, ...).
func skillSetDBErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errorx.NotFound("SKILLSET_NOT_FOUND", "skillset not found")
	case errors.Is(err, errSkillSetUnauthenticated):
		return err
	case errors.Is(err, errSkillSetForbidden):
		return err
	case strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(err.Error(), "23505"):
		return errorx.Conflict("SKILLSET_ALREADY_EXISTS", "skillset already exists")
	case strings.Contains(err.Error(), "23503") || strings.Contains(strings.ToLower(err.Error()), "foreign key"):
		return errorx.BadRequest("SKILLSET_SKILL_NOT_FOUND", "one or more referenced skills do not exist")
	default:
		return errorx.Internal("SKILLSET_INTERNAL", "skillset operation failed", errorx.WithCause(err))
	}
}

// ─── Endpoints ────────────────────────────────────────────────────────────

// withSkillSetAuthn wraps a skillset handler in the Kernel middleware chain
// (authn + access guard). The inner handler receives a context.Context that
// already carries the authenticated principal, so it can call
// authn.PrincipalFromContext directly. The request value is threaded through
// unchanged so handlers can bind it themselves before invoking this helper.
func (h *skillSetHTTPHandler) withSkillSetAuthn(ctx khttp.Context, operation string, req any, fn func(context.Context, any) (any, error)) (any, error) {
	khttp.SetOperation(ctx, operation)
	chain := ctx.Middleware(func(c context.Context, r any) (any, error) {
		return fn(c, r)
	})
	return chain(ctx, req)
}

func (h *skillSetHTTPHandler) listEndpoint(ctx khttp.Context) error {
	q := strings.TrimSpace(ctx.Query().Get("q"))
	pageNo := positiveInt(ctx.Query().Get("pageNo"), 1)
	pageSize := positiveInt(ctx.Query().Get("pageSize"), 50)
	if pageSize > 200 {
		pageSize = 200
	}
	out, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.ListSkillSets", nil, func(c context.Context, _ any) (any, error) {
		principal, _ := principalFromCtx(c)
		db := h.db(c).Model(&skillSetRow{}).
			Where("deleted_at IS NULL").
			Where("visibility = 'public' OR owner_id = ? OR (visibility = 'internal' AND org_id <> '' AND org_id = ?)", principal.SubjectID, principal.OrgID)
		if q != "" {
			like := "%" + q + "%"
			db = db.Where("name ILIKE ? OR display_name ILIKE ? OR description ILIKE ?", like, like, like)
		}
		var total int64
		if err := db.Count(&total).Error; err != nil {
			return nil, skillSetDBErr(err)
		}
		var rows []skillSetRow
		if err := db.Order("updated_at DESC, name ASC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
			return nil, skillSetDBErr(err)
		}
		for i := range rows {
			rows[i].Members, _ = h.members(c, rows[i].Name)
		}
		return map[string]any{"items": rows, "total": total, "pageNo": pageNo, "pageSize": pageSize}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *skillSetHTTPHandler) createEndpoint(ctx khttp.Context) error {
	var req skillSetWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return skillSetDecodeErr(err)
	}
	req.Name = strings.TrimSpace(req.Name)
	if !skillSetNameRE.MatchString(req.Name) {
		return errSkillSetInvalidName
	}
	out, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.CreateSkillSet", &req, func(c context.Context, _ any) (any, error) {
		principal, authenticated := principalFromCtx(c)
		if !authenticated {
			return nil, errSkillSetUnauthenticated
		}
		if strings.TrimSpace(principal.OrgID) == "" {
			return nil, errSkillSetZoneRequired
		}
		if h.resources.Authz == nil {
			return nil, errSkillSetAuthzUnavailable
		}
		if !h.allowCreate(c, principal) {
			return nil, errSkillSetForbidden
		}
		visibility := normalizeVisibility(req.Scope)
		row := skillSetRow{Name: req.Name, DisplayName: strings.TrimSpace(req.DisplayName), Description: strings.TrimSpace(req.Description), Visibility: visibility, OwnerID: principal.SubjectID, OrgID: principal.OrgID}
		err := h.db(c).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			return replaceSkillSetMembers(tx, row.Name, req.Members)
		})
		if err != nil {
			return nil, skillSetDBErr(err)
		}
		row.Members, _ = h.members(c, row.Name)
		return row, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, out)
}

func (h *skillSetHTTPHandler) getEndpoint(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	out, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.GetSkillSet", nil, func(c context.Context, _ any) (any, error) {
		row, err := h.visibleSet(c, name)
		if err != nil {
			return nil, skillSetDBErr(err)
		}
		row.Members, err = h.members(c, name)
		if err != nil {
			return nil, skillSetDBErr(err)
		}
		return row, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *skillSetHTTPHandler) updateEndpoint(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	var req skillSetWriteRequest
	if err := ctx.Bind(&req); err != nil {
		return skillSetDecodeErr(err)
	}
	_, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.UpdateSkillSet", &req, func(c context.Context, _ any) (any, error) {
		if err := h.requireOwnerPrincipal(c, name); err != nil {
			return nil, err
		}
		updates := map[string]any{
			"display_name": strings.TrimSpace(req.DisplayName),
			"description":  strings.TrimSpace(req.Description),
			"updated_at":   time.Now(),
		}
		if strings.TrimSpace(req.Scope) != "" {
			updates["visibility"] = normalizeVisibility(req.Scope)
		}
		err := h.db(c).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&skillSetRow{}).Where("name = ? AND deleted_at IS NULL", name).Updates(updates)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			if req.Members != nil {
				return replaceSkillSetMembers(tx, name, req.Members)
			}
			return nil
		})
		if err != nil {
			return nil, skillSetDBErr(err)
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	// Re-fetch and return the updated row.
	return h.getEndpoint(ctx)
}

func (h *skillSetHTTPHandler) removeEndpoint(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	_, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.DeleteSkillSet", nil, func(c context.Context, _ any) (any, error) {
		if err := h.requireOwnerPrincipal(c, name); err != nil {
			return nil, err
		}
		res := h.db(c).Model(&skillSetRow{}).Where("name = ? AND deleted_at IS NULL", name).
			Updates(map[string]any{"deleted_at": time.Now(), "updated_at": time.Now()})
		if res.Error != nil {
			return nil, skillSetDBErr(res.Error)
		}
		if res.RowsAffected == 0 {
			return nil, skillSetDBErr(gorm.ErrRecordNotFound)
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusNoContent, nil)
}

func (h *skillSetHTTPHandler) bindEndpoint(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	var member skillSetMember
	if err := ctx.Bind(&member); err != nil || !skillSetNameRE.MatchString(member.SkillName) {
		return errSkillSetMemberInvalid
	}
	out, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.BindSkill", &member, func(c context.Context, _ any) (any, error) {
		if err := h.requireOwnerPrincipal(c, name); err != nil {
			return nil, err
		}
		result := h.db(c).Exec(`
			INSERT INTO aihub_skillset_items(skillset_name, skill_name, sort_order)
			SELECT ?, r.name, ?
			FROM repos r
			JOIN hub_skill_profiles p ON p.repository_id = r.id
			WHERE r.name = ? AND p.lifecycle_status = 'active'
			ON CONFLICT (skillset_name, skill_name)
			DO UPDATE SET sort_order = EXCLUDED.sort_order, updated_at = NOW()`, name, member.Order, member.SkillName)
		if result.Error != nil {
			return nil, skillSetDBErr(result.Error)
		}
		if result.RowsAffected == 0 {
			return nil, skillSetDBErr(gorm.ErrRecordNotFound)
		}
		return member, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *skillSetHTTPHandler) updateMemberEndpoint(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	skillName := ctx.Vars().Get("skill")
	var member skillSetMember
	if err := ctx.Bind(&member); err != nil {
		return skillSetDecodeErr(err)
	}
	out, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.UpdateSkillSetMember", &member, func(c context.Context, _ any) (any, error) {
		if err := h.requireOwnerPrincipal(c, name); err != nil {
			return nil, err
		}
		res := h.db(c).Exec(`UPDATE aihub_skillset_items SET sort_order = ?, updated_at = NOW() WHERE skillset_name = ? AND skill_name = ?`, member.Order, name, skillName)
		if res.Error != nil {
			return nil, skillSetDBErr(res.Error)
		}
		if res.RowsAffected == 0 {
			return nil, skillSetDBErr(gorm.ErrRecordNotFound)
		}
		member.SkillName = skillName
		return member, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}

func (h *skillSetHTTPHandler) unbindEndpoint(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	skillName := ctx.Vars().Get("skill")
	_, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.UnbindSkill", nil, func(c context.Context, _ any) (any, error) {
		if err := h.requireOwnerPrincipal(c, name); err != nil {
			return nil, err
		}
		res := h.db(c).Exec(`DELETE FROM aihub_skillset_items WHERE skillset_name = ? AND skill_name = ?`, name, skillName)
		if res.Error != nil {
			return nil, skillSetDBErr(res.Error)
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusNoContent, nil)
}

func (h *skillSetHTTPHandler) reverseLookupEndpoint(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	out, err := h.withSkillSetAuthn(ctx, "aisphere.hub.skillset.v1.ReverseLookup", nil, func(c context.Context, _ any) (any, error) {
		principal, _ := principalFromCtx(c)
		var names []string
		err := h.db(c).Raw(`
			SELECT s.name
			FROM aihub_skillsets s
			JOIN aihub_skillset_items i ON i.skillset_name = s.name
			WHERE i.skill_name = ? AND s.deleted_at IS NULL
			  AND (s.visibility = 'public' OR s.owner_id = ? OR (s.visibility = 'internal' AND s.org_id <> '' AND s.org_id = ?))
			ORDER BY s.name`, name, principal.SubjectID, principal.OrgID).Scan(&names).Error
		if err != nil {
			return nil, skillSetDBErr(err)
		}
		return map[string]any{"skillsets": names}, nil
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, out)
}
