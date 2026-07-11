package data

import (
	"context"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/errorx"
)

func TestAuthzRepoRejectsSchemaAdministration(t *testing.T) {
	repo := NewAuthzRepo(&Resources{})
	if _, err := repo.ReadSchema(context.Background()); errorx.CodeOf(err) != errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY") {
		t.Fatalf("ReadSchema error = %v", err)
	}
	if err := repo.WriteSchema(context.Background(), biz.AuthzSchema{Text: "definition user {}"}); errorx.CodeOf(err) != errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY") {
		t.Fatalf("WriteSchema error = %v", err)
	}
}
