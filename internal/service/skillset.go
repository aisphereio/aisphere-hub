package service

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

type SkillSetService struct {
	uc *biz.SkillSetUsecase
}

func NewSkillSetService(uc *biz.SkillSetUsecase) *SkillSetService {
	return &SkillSetService{uc: uc}
}

func (s *SkillSetService) RegisterHTTPServer(srv *khttp.Server) {
	router := srv.Route("")
	router.GET("/v1/skillsets", s.list)
	router.POST("/v1/skillsets", s.create)
	router.GET("/v1/skillsets/{name}", s.get)
	router.PUT("/v1/skillsets/{name}", s.update)
	router.DELETE("/v1/skillsets/{name}", s.delete)
	router.GET("/v1/skills/{name}/skillsets", s.listBySkill)
}

type skillSetDTO struct {
	Name        string              `json:"name"`
	DisplayName string              `json:"displayName,omitempty"`
	Description string              `json:"description,omitempty"`
	Visibility  string              `json:"visibility"`
	Scope       string              `json:"scope"`
	Owner       string              `json:"owner,omitempty"`
	OwnerID     string              `json:"ownerId,omitempty"`
	OrgID       string              `json:"orgId,omitempty"`
	Labels      map[string]string   `json:"labels,omitempty"`
	Members     []skillSetMemberDTO `json:"members"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
}

type skillSetMemberDTO struct {
	SkillName string `json:"skillName"`
	Order     int    `json:"order"`
}

type createSkillSetRequest struct {
	Name        string              `json:"name"`
	DisplayName string              `json:"displayName"`
	Description string              `json:"description"`
	Visibility  string              `json:"visibility"`
	Scope       string              `json:"scope"`
	Labels      map[string]string   `json:"labels"`
	Members     []skillSetMemberDTO `json:"members"`
}

type updateSkillSetRequest struct {
	DisplayName *string              `json:"displayName"`
	Description *string              `json:"description"`
	Visibility  *string              `json:"visibility"`
	Scope       *string              `json:"scope"`
	Labels      *map[string]string   `json:"labels"`
	Members     *[]skillSetMemberDTO `json:"members"`
}

func (s *SkillSetService) create(ctx khttp.Context) error {
	var req createSkillSetRequest
	if err := ctx.Bind(&req); err != nil {
		return err
	}
	visibility := firstNonEmptySkillSetValue(req.Visibility, req.Scope)
	out, err := s.uc.Create(ctx, skillSetPrincipal(ctx), &biz.SkillSet{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Description: req.Description,
		Visibility:  visibility,
		Labels:      req.Labels,
		Members:     membersToDomain(req.Members),
	})
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, skillSetToDTO(out))
}

func (s *SkillSetService) update(ctx khttp.Context) error {
	name := ctx.Vars().Get("name")
	var req updateSkillSetRequest
	if err := ctx.Bind(&req); err != nil {
		return err
	}
	visibility := req.Visibility
	if visibility == nil && req.Scope != nil {
		visibility = req.Scope
	}
	patch := biz.SkillSetPatch{
		DisplayName: req.DisplayName,
		Description: req.Description,
		Visibility:  visibility,
		Labels:      req.Labels,
	}
	if req.Members != nil {
		members := membersToDomain(*req.Members)
		patch.Members = &members
	}
	out, err := s.uc.Update(ctx, skillSetPrincipal(ctx), name, patch)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, skillSetToDTO(out))
}

func (s *SkillSetService) get(ctx khttp.Context) error {
	out, err := s.uc.Get(ctx, skillSetPrincipal(ctx), ctx.Vars().Get("name"))
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, skillSetToDTO(out))
}

func (s *SkillSetService) list(ctx khttp.Context) error {
	pageSize := parsePositiveInt(ctx.Query().Get("pageSize"), 20)
	pageNo := parsePositiveInt(ctx.Query().Get("pageNo"), 1)
	out, err := s.uc.List(ctx, skillSetPrincipal(ctx), biz.SkillSetListOptions{
		Limit:  pageSize,
		Offset: (pageNo - 1) * pageSize,
		Query:  strings.TrimSpace(ctx.Query().Get("q")),
	})
	if err != nil {
		return err
	}
	items := make([]skillSetDTO, 0, len(out.Items))
	for _, item := range out.Items {
		items = append(items, skillSetToDTO(item))
	}
	return ctx.JSON(http.StatusOK, map[string]any{
		"items":    items,
		"total":    out.Total,
		"pageNo":   pageNo,
		"pageSize": pageSize,
	})
}

func (s *SkillSetService) delete(ctx khttp.Context) error {
	if err := s.uc.Delete(ctx, skillSetPrincipal(ctx), ctx.Vars().Get("name")); err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, map[string]any{})
}

func (s *SkillSetService) listBySkill(ctx khttp.Context) error {
	names, err := s.uc.ListNamesBySkill(ctx, skillSetPrincipal(ctx), ctx.Vars().Get("name"))
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, map[string]any{"skillsets": names})
}

func skillSetPrincipal(ctx khttp.Context) authn.Principal {
	principal, _ := authn.PrincipalFromContext(ctx)
	return principal
}

func skillSetToDTO(set *biz.SkillSet) skillSetDTO {
	members := make([]skillSetMemberDTO, 0, len(set.Members))
	for _, member := range set.Members {
		members = append(members, skillSetMemberDTO{SkillName: member.SkillName, Order: member.Order})
	}
	return skillSetDTO{
		Name:        set.Name,
		DisplayName: set.DisplayName,
		Description: set.Description,
		Visibility:  set.Visibility,
		Scope:       set.Visibility,
		Owner:       set.OwnerID,
		OwnerID:     set.OwnerID,
		OrgID:       set.OrgID,
		Labels:      set.Labels,
		Members:     members,
		CreatedAt:   set.CreateTime,
		UpdatedAt:   set.UpdateTime,
	}
}

func membersToDomain(in []skillSetMemberDTO) []biz.SkillSetMember {
	out := make([]biz.SkillSetMember, 0, len(in))
	for _, member := range in {
		out = append(out, biz.SkillSetMember{SkillName: member.SkillName, Order: member.Order})
	}
	return out
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func firstNonEmptySkillSetValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
