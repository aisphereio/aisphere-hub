package data

import (
	"context"
	"errors"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSkillRepositoryDefaultsMainAndRejectsDuplicateName(t *testing.T) {
	db := openGitNativeTestDB(t)
	repo := newSkillRepoForDB(db)

	created, err := repo.CreateSkill(context.Background(), &biz.GitSkill{
		Name:      "search-tools",
		OwnerID:   "user-1",
		ProjectID: "project-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.DefaultBranch != "main" {
		t.Fatalf("default branch = %q, want main", created.DefaultBranch)
	}
	if created.Status != biz.SkillStatusProvisioning {
		t.Fatalf("status = %q, want provisioning", created.Status)
	}
	if created.Visibility != biz.SkillVisibilityPrivate {
		t.Fatalf("visibility = %q, want private", created.Visibility)
	}

	_, err = repo.CreateSkill(context.Background(), &biz.GitSkill{Name: "search-tools", OwnerID: "user-2"})
	if !errors.Is(err, biz.ErrSkillAlreadyExists) {
		t.Fatalf("duplicate error = %v, want ErrSkillAlreadyExists", err)
	}
}

func openGitNativeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&skillModel{}, &skillPullRequestModel{}, &skillPullRequestReviewModel{}); err != nil {
		t.Fatal(err)
	}
	return db
}
