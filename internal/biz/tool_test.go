package biz

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeToolRepo is an in-memory ToolRepository for unit tests.
type fakeToolRepo struct {
	tools map[string]*Tool
}

func newFakeToolRepo() *fakeToolRepo {
	return &fakeToolRepo{tools: map[string]*Tool{}}
}

func (r *fakeToolRepo) List(ctx context.Context, opts ToolListOptions) ([]*Tool, string, error) {
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out, "", nil
}
func (r *fakeToolRepo) Get(ctx context.Context, id, version string) (*Tool, error) {
	t, ok := r.tools[id]
	if !ok {
		return nil, ErrToolNotFound
	}
	return t, nil
}
func (r *fakeToolRepo) Create(ctx context.Context, t *Tool) (*Tool, error) {
	if _, exists := r.tools[t.ID]; exists {
		return nil, ErrToolAlreadyExists
	}
	cp := *t
	r.tools[t.ID] = &cp
	return &cp, nil
}
func (r *fakeToolRepo) Update(ctx context.Context, t *Tool) (*Tool, error) {
	if _, ok := r.tools[t.ID]; !ok {
		return nil, ErrToolNotFound
	}
	cp := *t
	r.tools[t.ID] = &cp
	return &cp, nil
}
func (r *fakeToolRepo) Delete(ctx context.Context, id string) error {
	delete(r.tools, id)
	return nil
}
func (r *fakeToolRepo) UpsertBuiltin(ctx context.Context, t *Tool) (*Tool, error) {
	cp := *t
	r.tools[t.ID] = &cp
	return &cp, nil
}

func TestSeedBuiltinTools_Idempotent(t *testing.T) {
	repo := newFakeToolRepo()
	ctx := context.Background()
	if err := SeedBuiltinTools(ctx, repo); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	want := len(builtinToolSeeds)
	if got := len(repo.tools); got != want {
		t.Fatalf("after first seed: got %d tools, want %d", got, want)
	}
	// Second seed should be idempotent (no error, same count).
	if err := SeedBuiltinTools(ctx, repo); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if got := len(repo.tools); got != want {
		t.Fatalf("after second seed: got %d tools, want %d", got, want)
	}
}

func TestSeedBuiltinTools_PrivilegedToolsHaveCapability(t *testing.T) {
	repo := newFakeToolRepo()
	if err := SeedBuiltinTools(context.Background(), repo); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cases := []struct {
		id        string
		wantCap   string // expected capability, "" for non-privileged
		wantPlace string
	}{
		{"workspace.read", "", "sandbox"},
		{"skill.fetch", "skill:view", "runtime"},
		{"skill.publish", "skill:publish", "runtime"},
		{"git.pull", "skill:view", "runtime"},
		{"git.push", "skill:edit", "runtime"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			tool, ok := repo.tools[c.id]
			if !ok {
				t.Fatalf("tool %q not seeded", c.id)
			}
			def := tool.Definition()
			if def.Execution == nil {
				t.Fatal("definition has no execution")
			}
			if def.Execution.Placement != c.wantPlace {
				t.Errorf("placement: got %q, want %q", def.Execution.Placement, c.wantPlace)
			}
			if c.wantCap == "" {
				if len(def.Execution.Capabilities) != 0 {
					t.Errorf("non-privileged tool has capabilities: %v", def.Execution.Capabilities)
				}
			} else {
				found := false
				for _, cap := range def.Execution.Capabilities {
					if cap == c.wantCap {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("capability: got %v, want to contain %q", def.Execution.Capabilities, c.wantCap)
				}
			}
		})
	}
}

func TestSeedBuiltinTools_InputSchemaValid(t *testing.T) {
	repo := newFakeToolRepo()
	if err := SeedBuiltinTools(context.Background(), repo); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, s := range builtinToolSeeds {
		tool, ok := repo.tools[s.id]
		if !ok {
			t.Errorf("tool %q not seeded", s.id)
			continue
		}
		def := tool.Definition()
		if len(def.InputSchema) == 0 {
			t.Errorf("tool %q has no input schema", s.id)
			continue
		}
		// Verify it's valid JSON.
		var v map[string]any
		if err := json.Unmarshal(def.InputSchema, &v); err != nil {
			t.Errorf("tool %q input schema is not valid JSON: %v", s.id, err)
		}
	}
}

func TestValidToolID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"workspace.read", true},
		{"skill.fetch", true},
		{"git_pull", true},
		{"a", true},
		{"", false},
		{".leading", false},
		{"trailing.", false},
		{"trailing-", false},
		{"has space", false},
		{"has/slash", false},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			if got := validToolID(c.id); got != c.want {
				t.Errorf("validToolID(%q): got %v, want %v", c.id, got, c.want)
			}
		})
	}
}
