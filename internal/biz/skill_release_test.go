package biz

import (
	"context"
	"errors"
	"testing"

	"github.com/aisphereio/kernel/authn"
)

type releaseCapableFake struct {
	*fakeSkillGitEngine
	created  []CreateSkillRelease
	releases map[string]*SkillRelease
}

func newReleaseCapableFake() *releaseCapableFake {
	return &releaseCapableFake{
		fakeSkillGitEngine: &fakeSkillGitEngine{refs: map[string]string{}},
		releases:           map[string]*SkillRelease{},
	}
}

func (f *releaseCapableFake) CreateRelease(_ context.Context, in CreateSkillRelease) (*SkillRelease, error) {
	key := in.SkillName + ":" + in.Version
	if _, ok := f.releases[key]; ok {
		return nil, ErrSkillReleaseAlreadyExists
	}
	f.created = append(f.created, in)
	out := &SkillRelease{Tag: in.Version, CommitSHA: in.ExpectedCommitSHA, CreateTime: in.CreateTime}
	f.releases[key] = out
	copy := *out
	return &copy, nil
}

func (f *releaseCapableFake) GetRelease(_ context.Context, skill, version string) (*SkillRelease, error) {
	item, ok := f.releases[skill+":"+version]
	if !ok {
		return nil, ErrSkillReleaseNotFound
	}
	copy := *item
	return &copy, nil
}

func TestNormalizeReleaseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "1.2.3", want: "v1.2.3", ok: true},
		{input: "v1.2.3", want: "v1.2.3", ok: true},
		{input: "1.2.3-rc.1", want: "v1.2.3-rc.1", ok: true},
		{input: "latest", ok: false},
		{input: "main", ok: false},
		{input: "01.2.3", ok: false},
	}
	for _, tt := range tests {
		got, ok := NormalizeReleaseVersion(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("NormalizeReleaseVersion(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCreateReleasePinsExpectedCommitAndNormalizesTag(t *testing.T) {
	git := newReleaseCapableFake()
	git.refs["search:refs/heads/main"] = "commit-1"
	uc := NewSkillUsecase(newMemoryGitSkillRepo(), newMemoryPullRequestRepo(), git, &fakeSkillRelationships{})
	principal := authn.Principal{SubjectID: "publisher-1", SubjectType: authn.SubjectTypeUser, Name: "Publisher"}

	release, err := uc.CreateRelease(context.Background(), principal, CreateSkillRelease{
		SkillName:         "search",
		Version:           "1.2.3",
		ExpectedCommitSHA: "commit-1",
		ReleaseNotes:      "stable release",
	})
	if err != nil {
		t.Fatal(err)
	}
	if release.Tag != "v1.2.3" || release.CommitSHA != "commit-1" {
		t.Fatalf("release = %+v", release)
	}
	if len(git.created) != 1 || git.created[0].SourceRef != "refs/heads/main" || git.created[0].ActorID != "publisher-1" {
		t.Fatalf("create input = %+v", git.created)
	}
}

func TestCreateReleaseRejectsStaleSource(t *testing.T) {
	git := newReleaseCapableFake()
	git.refs["search:refs/heads/main"] = "commit-2"
	uc := NewSkillUsecase(newMemoryGitSkillRepo(), newMemoryPullRequestRepo(), git, &fakeSkillRelationships{})
	principal := authn.Principal{SubjectID: "publisher-1", SubjectType: authn.SubjectTypeUser}

	_, err := uc.CreateRelease(context.Background(), principal, CreateSkillRelease{
		SkillName:         "search",
		Version:           "1.2.3",
		ExpectedCommitSHA: "commit-1",
	})
	if !errors.Is(err, ErrSkillReleaseStale) {
		t.Fatalf("CreateRelease error = %v, want ErrSkillReleaseStale", err)
	}
	if len(git.created) != 0 {
		t.Fatalf("release engine called for stale source: %+v", git.created)
	}
}

func TestCreateReleaseRejectsFloatingVersion(t *testing.T) {
	git := newReleaseCapableFake()
	git.refs["search:refs/heads/main"] = "commit-1"
	uc := NewSkillUsecase(newMemoryGitSkillRepo(), newMemoryPullRequestRepo(), git, &fakeSkillRelationships{})
	principal := authn.Principal{SubjectID: "publisher-1", SubjectType: authn.SubjectTypeUser}

	_, err := uc.CreateRelease(context.Background(), principal, CreateSkillRelease{
		SkillName:         "search",
		Version:           "latest",
		ExpectedCommitSHA: "commit-1",
	})
	if !errors.Is(err, ErrSkillInvalidArgument) {
		t.Fatalf("CreateRelease error = %v, want ErrSkillInvalidArgument", err)
	}
}
