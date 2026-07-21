package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/data"
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

func registerSkillSetHTTP(srv *khttp.Server, resources *data.Resources) {
	if srv == nil || resources == nil || resources.DB == nil {
		return
	}
	h := &skillSetHTTPHandler{resources: resources}
	srv.HandleFunc("/v1/skillsets", h.root)
	srv.HandleFunc("/v1/skillsets/", h.item)
	srv.HandleFunc("/v1/skills/", h.reverseLookup)
}

func (h *skillSetHTTPHandler) db(r *http.Request) *gorm.DB {
	return h.resources.DB.GORM(r.Context())
}

func (h *skillSetHTTPHandler) root(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.list(w, r)
	case http.MethodPost:
		h.create(w, r)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *skillSetHTTPHandler) item(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/skillsets/"), "/")
	if path == "" {
		h.root(w, r)
		return
	}
	parts := strings.Split(path, "/")
	name := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			h.get(w, r, name)
		case http.MethodPut:
			h.update(w, r, name)
		case http.MethodDelete:
			h.remove(w, r, name)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodDelete)
		}
		return
	}
	if len(parts) == 2 && parts[1] == "members" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		h.bind(w, r, name)
		return
	}
	if len(parts) == 3 && parts[1] == "members" {
		skillName := parts[2]
		switch r.Method {
		case http.MethodPut:
			h.updateMember(w, r, name, skillName)
		case http.MethodDelete:
			h.unbind(w, r, name, skillName)
		default:
			methodNotAllowed(w, http.MethodPut, http.MethodDelete)
		}
		return
	}
	http.NotFound(w, r)
}

func (h *skillSetHTTPHandler) reverseLookup(w http.ResponseWriter, r *http.Request) {
	const prefix = "/v1/skills/"
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "skillsets" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	principal, org := requestIdentity(r)
	var names []string
	err := h.db(r).Raw(`
		SELECT s.name
		FROM aihub_skillsets s
		JOIN aihub_skillset_items i ON i.skillset_name = s.name
		WHERE i.skill_name = ? AND s.deleted_at IS NULL
		  AND (s.visibility = 'public' OR s.owner_id = ? OR (s.visibility = 'internal' AND s.org_id <> '' AND s.org_id = ?))
		ORDER BY s.name`, parts[0], principal, org).Scan(&names).Error
	if err != nil {
		writeSkillSetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skillsets": names})
}

func (h *skillSetHTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	principal, org := requestIdentity(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	pageNo := positiveInt(r.URL.Query().Get("pageNo"), 1)
	pageSize := positiveInt(r.URL.Query().Get("pageSize"), 50)
	if pageSize > 200 {
		pageSize = 200
	}

	db := h.db(r).Model(&skillSetRow{}).
		Where("deleted_at IS NULL").
		Where("visibility = 'public' OR owner_id = ? OR (visibility = 'internal' AND org_id <> '' AND org_id = ?)", principal, org)
	if q != "" {
		like := "%" + q + "%"
		db = db.Where("name ILIKE ? OR display_name ILIKE ? OR description ILIKE ?", like, like, like)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		writeSkillSetError(w, err)
		return
	}
	var rows []skillSetRow
	if err := db.Order("updated_at DESC, name ASC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		writeSkillSetError(w, err)
		return
	}
	for i := range rows {
		rows[i].Members, _ = h.members(r, rows[i].Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": rows, "total": total, "pageNo": pageNo, "pageSize": pageSize,
	})
}

func (h *skillSetHTTPHandler) get(w http.ResponseWriter, r *http.Request, name string) {
	row, err := h.visibleSet(r, name)
	if err != nil {
		writeSkillSetError(w, err)
		return
	}
	row.Members, err = h.members(r, name)
	if err != nil {
		writeSkillSetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *skillSetHTTPHandler) create(w http.ResponseWriter, r *http.Request) {
	var req skillSetWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "SKILLSET_INVALID_ARGUMENT", "message": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !skillSetNameRE.MatchString(req.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "SKILLSET_INVALID_NAME", "message": "invalid skillset name"})
		return
	}
	principal, org := requestIdentity(r)
	if principal == "" || principal == "anonymous" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "UNAUTHENTICATED", "message": "authentication required"})
		return
	}
	visibility := normalizeVisibility(req.Scope)
	row := skillSetRow{Name: req.Name, DisplayName: strings.TrimSpace(req.DisplayName), Description: strings.TrimSpace(req.Description), Visibility: visibility, OwnerID: principal, OrgID: org}
	err := h.db(r).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return replaceSkillSetMembers(tx, row.Name, req.Members)
	})
	if err != nil {
		writeSkillSetError(w, err)
		return
	}
	row.Members, _ = h.members(r, row.Name)
	writeJSON(w, http.StatusCreated, row)
}

func (h *skillSetHTTPHandler) update(w http.ResponseWriter, r *http.Request, name string) {
	if err := h.requireOwner(r, name); err != nil {
		writeSkillSetError(w, err)
		return
	}
	var req skillSetWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "SKILLSET_INVALID_ARGUMENT", "message": err.Error()})
		return
	}
	updates := map[string]any{
		"display_name": strings.TrimSpace(req.DisplayName),
		"description":  strings.TrimSpace(req.Description),
		"updated_at":   time.Now(),
	}
	if strings.TrimSpace(req.Scope) != "" {
		updates["visibility"] = normalizeVisibility(req.Scope)
	}
	err := h.db(r).Transaction(func(tx *gorm.DB) error {
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
		writeSkillSetError(w, err)
		return
	}
	h.get(w, r, name)
}

func (h *skillSetHTTPHandler) remove(w http.ResponseWriter, r *http.Request, name string) {
	if err := h.requireOwner(r, name); err != nil {
		writeSkillSetError(w, err)
		return
	}
	res := h.db(r).Model(&skillSetRow{}).Where("name = ? AND deleted_at IS NULL", name).
		Updates(map[string]any{"deleted_at": time.Now(), "updated_at": time.Now()})
	if res.Error != nil {
		writeSkillSetError(w, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		writeSkillSetError(w, gorm.ErrRecordNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *skillSetHTTPHandler) bind(w http.ResponseWriter, r *http.Request, name string) {
	if err := h.requireOwner(r, name); err != nil {
		writeSkillSetError(w, err)
		return
	}
	var member skillSetMember
	if err := decodeJSON(r, &member); err != nil || !skillSetNameRE.MatchString(member.SkillName) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "SKILLSET_MEMBER_INVALID", "message": "valid skillName is required"})
		return
	}
	result := h.db(r).Exec(`
		INSERT INTO aihub_skillset_items(skillset_name, skill_name, sort_order)
		SELECT ?, r.name, ?
		FROM repos r
		JOIN hub_skill_profiles p ON p.repository_id = r.id
		WHERE r.name = ? AND p.lifecycle_status = 'active'
		ON CONFLICT (skillset_name, skill_name)
		DO UPDATE SET sort_order = EXCLUDED.sort_order, updated_at = NOW()`, name, member.Order, member.SkillName)
	if result.Error != nil {
		writeSkillSetError(w, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		writeSkillSetError(w, gorm.ErrRecordNotFound)
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func (h *skillSetHTTPHandler) updateMember(w http.ResponseWriter, r *http.Request, name, skillName string) {
	if err := h.requireOwner(r, name); err != nil {
		writeSkillSetError(w, err)
		return
	}
	var member skillSetMember
	if err := decodeJSON(r, &member); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "SKILLSET_MEMBER_INVALID", "message": err.Error()})
		return
	}
	res := h.db(r).Exec(`UPDATE aihub_skillset_items SET sort_order = ?, updated_at = NOW() WHERE skillset_name = ? AND skill_name = ?`, member.Order, name, skillName)
	if res.Error != nil {
		writeSkillSetError(w, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		writeSkillSetError(w, gorm.ErrRecordNotFound)
		return
	}
	member.SkillName = skillName
	writeJSON(w, http.StatusOK, member)
}

func (h *skillSetHTTPHandler) unbind(w http.ResponseWriter, r *http.Request, name, skillName string) {
	if err := h.requireOwner(r, name); err != nil {
		writeSkillSetError(w, err)
		return
	}
	res := h.db(r).Exec(`DELETE FROM aihub_skillset_items WHERE skillset_name = ? AND skill_name = ?`, name, skillName)
	if res.Error != nil {
		writeSkillSetError(w, res.Error)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *skillSetHTTPHandler) members(r *http.Request, name string) ([]skillSetMember, error) {
	var members []skillSetMember
	err := h.db(r).Raw(`
		SELECT i.skill_name, i.sort_order, '' AS version,
		       COALESCE(p.display_name, '') AS display_name
		FROM aihub_skillset_items i
		JOIN repos r ON r.name = i.skill_name
		JOIN hub_skill_profiles p ON p.repository_id = r.id
		WHERE i.skillset_name = ? AND p.lifecycle_status = 'active'
		ORDER BY i.sort_order ASC, i.skill_name ASC`, name).Scan(&members).Error
	return members, err
}

func (h *skillSetHTTPHandler) visibleSet(r *http.Request, name string) (*skillSetRow, error) {
	principal, org := requestIdentity(r)
	var row skillSetRow
	err := h.db(r).Where("name = ? AND deleted_at IS NULL", name).
		Where("visibility = 'public' OR owner_id = ? OR (visibility = 'internal' AND org_id <> '' AND org_id = ?)", principal, org).
		First(&row).Error
	return &row, err
}

func (h *skillSetHTTPHandler) requireOwner(r *http.Request, name string) error {
	principal, _ := requestIdentity(r)
	if principal == "" || principal == "anonymous" {
		return errSkillSetUnauthenticated
	}
	var count int64
	err := h.db(r).Model(&skillSetRow{}).Where("name = ? AND owner_id = ? AND deleted_at IS NULL", name, principal).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return errSkillSetForbidden
	}
	return nil
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

// decodeJSON decodes the request body into out. Unknown JSON fields are
// tolerated because the frontend SkillSet/SkillSetMember contracts carry
// response-only fields (createdAt, updatedAt, owner, labels, member.label,
// member.required) that are echoed back on the edit path; rejecting them
// would break create/update/bind for the dialog.
func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(out)
}

func requestIdentity(r *http.Request) (principal, org string) {
	for _, key := range []string{"X-Aisphere-Principal", "X-Principal-Id", "X-User-Id"} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			principal = value
			break
		}
	}
	if principal == "" {
		principal = "anonymous"
	}
	for _, key := range []string{"X-Aisphere-Org", "X-Org-Id"} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			org = value
			break
		}
	}
	return principal, org
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

var (
	errSkillSetUnauthenticated = errors.New("skillset unauthenticated")
	errSkillSetForbidden       = errors.New("skillset forbidden")
)

func writeSkillSetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "SKILLSET_NOT_FOUND", "message": "skillset not found"})
	case errors.Is(err, errSkillSetUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "UNAUTHENTICATED", "message": "authentication required"})
	case errors.Is(err, errSkillSetForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "SKILLSET_PERMISSION_DENIED", "message": "only the owner can modify this skillset"})
	case strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(err.Error(), "23505"):
		writeJSON(w, http.StatusConflict, map[string]string{"code": "SKILLSET_ALREADY_EXISTS", "message": "skillset already exists"})
	case strings.Contains(err.Error(), "23503") || strings.Contains(strings.ToLower(err.Error()), "foreign key"):
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "SKILLSET_SKILL_NOT_FOUND", "message": "one or more referenced skills do not exist"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "SKILLSET_INTERNAL", "message": "skillset operation failed"})
	}
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "METHOD_NOT_ALLOWED", "message": "method not allowed"})
}
