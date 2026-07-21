package data

import (
	"context"
	"errors"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSkillRepositoryReadsCanonicalRepositoryAndProfile(t *testing.T) {
	db := openGitNativeTestDB(t)
	repo := newSkillRepoForDB(db)
	seedSkill(t, db, softRepoModel{Name: "search-tools", Description: "Search", Private: true}, skillProfileModel{
		DisplayName: "Search Tools", OrgID: "org-1", ProjectID: "project-1",
		CreatedByType: "user", CreatedByID: "user-1", Visibility: biz.SkillVisibilityPrivate,
		Status: biz.SkillStatusProvisioning, DefaultBranch: biz.SkillDefaultBranch,
	})

	created, err := repo.GetSkill(context.Background(), "search-tools")
	if err != nil {
		t.Fatal(err)
	}
	if created.RepositoryID == 0 {
		t.Fatal("repository id was not loaded")
	}
	if created.DefaultBranch != biz.SkillDefaultBranch || created.Status != biz.SkillStatusProvisioning {
		t.Fatalf("skill = status %q branch %q", created.Status, created.DefaultBranch)
	}
	if created.OwnerID != "user-1" || created.OwnerType != "user" || created.Description != "Search" {
		t.Fatalf("skill projection = %+v", created)
	}
}

func TestSkillRepositoryUpdatesProfileAndRepositoryTogether(t *testing.T) {
	db := openGitNativeTestDB(t)
	repo := newSkillRepoForDB(db)
	seedSkill(t, db, softRepoModel{Name: "search-tools", Description: "old", Private: true}, skillProfileModel{
		DisplayName: "Old", OrgID: "org-1", CreatedByType: "user", CreatedByID: "user-1",
		Visibility: biz.SkillVisibilityPrivate, Status: biz.SkillStatusProvisioning, DefaultBranch: biz.SkillDefaultBranch,
	})

	updated, err := repo.UpdateSkill(context.Background(), &biz.GitSkill{Name: "search-tools", DisplayName: "New", Description: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "New" || updated.Description != "new" {
		t.Fatalf("updated skill = %+v", updated)
	}

	updated, err = repo.UpdateSkillVisibility(context.Background(), "search-tools", biz.SkillVisibilityPublic)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Visibility != biz.SkillVisibilityPublic {
		t.Fatalf("visibility = %q", updated.Visibility)
	}
	var canonical softRepoModel
	if err := db.Where("name = ?", "search-tools").First(&canonical).Error; err != nil {
		t.Fatal(err)
	}
	if canonical.Private {
		t.Fatal("public Skill must project repos.private=false")
	}

	active, err := repo.UpdateSkillStatus(context.Background(), "search-tools", biz.SkillStatusProvisioning, biz.SkillStatusActive)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != biz.SkillStatusActive {
		t.Fatalf("status = %q", active.Status)
	}
	if _, err := repo.UpdateSkillStatus(context.Background(), "search-tools", biz.SkillStatusProvisioning, biz.SkillStatusDeleting); !errors.Is(err, biz.ErrSkillNotFound) {
		t.Fatalf("stale status transition error = %v, want ErrSkillNotFound", err)
	}
}

func seedSkill(t *testing.T, db *gorm.DB, canonical softRepoModel, profile skillProfileModel) {
	t.Helper()
	if err := db.Create(&canonical).Error; err != nil {
		t.Fatal(err)
	}
	profile.RepositoryID = canonical.ID
	if err := db.Create(&profile).Error; err != nil {
		t.Fatal(err)
	}
}

func openGitNativeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&softRepoModel{}, &skillProfileModel{}, &skillPullRequestModel{}, &skillPullRequestReviewModel{}); err != nil {
		t.Fatal(err)
	}
	return db
}
