package biz

import (
	"encoding/json"
	"testing"
)

func TestMergeSandboxSkills(t *testing.T) {
	tmpl := []SandboxSkillRef{{Name: "a", Version: "v1.0.0"}, {Name: "b", Version: "v2.0.0"}}
	inline := []SandboxSkillRef{{Name: "b", Version: "v2.1.0"}, {Name: "c", Version: "v3.0.0"}}

	got := mergeSandboxSkills(tmpl, inline)

	want := []SandboxSkillRef{{Name: "a", Version: "v1.0.0"}, {Name: "b", Version: "v2.1.0"}, {Name: "c", Version: "v3.0.0"}}
	if len(got) != len(want) {
		t.Fatalf("got %d skills, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMergeSandboxSkills_EmptyAndNil(t *testing.T) {
	if got := mergeSandboxSkills(nil, nil); got != nil {
		t.Errorf("nil+nil = %+v, want nil", got)
	}
	if got := mergeSandboxSkills(nil, []SandboxSkillRef{{Name: "x", Version: "v1"}}); len(got) != 1 || got[0].Name != "x" {
		t.Errorf("nil+inline = %+v, want [x]", got)
	}
	if got := mergeSandboxSkills([]SandboxSkillRef{{Name: "x", Version: "v1"}}, nil); len(got) != 1 || got[0].Name != "x" {
		t.Errorf("template+nil = %+v, want [x]", got)
	}
}

func TestMergeSandboxSkills_DedupByName(t *testing.T) {
	// Duplicate within template keeps first occurrence.
	tmpl := []SandboxSkillRef{{Name: "a", Version: "v1"}, {Name: "a", Version: "v2"}}
	got := mergeSandboxSkills(tmpl, nil)
	if len(got) != 1 || got[0].Version != "v1" {
		t.Errorf("dedup template = %+v, want [a/v1]", got)
	}
}

func TestMergeSandboxSkills_BlankNameIgnored(t *testing.T) {
	got := mergeSandboxSkills([]SandboxSkillRef{{Name: "", Version: "v1"}}, []SandboxSkillRef{{Name: "a", Version: "v1"}})
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("blank name not ignored: %+v", got)
	}
}

func TestSandboxSkillAnnotations(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if got := sandboxSkillAnnotations(nil); got != nil {
			t.Errorf("nil = %+v, want nil", got)
		}
	})
	t.Run("empty returns nil", func(t *testing.T) {
		if got := sandboxSkillAnnotations([]SandboxSkillRef{}); got != nil {
			t.Errorf("empty = %+v, want nil", got)
		}
	})
	t.Run("renders JSON array under aisphere.io/skills", func(t *testing.T) {
		skills := []SandboxSkillRef{{Name: "ttt1", Version: "v1.4.2"}, {Name: "foo", Version: "v0.2.0"}}
		got := sandboxSkillAnnotations(skills)
		if got == nil {
			t.Fatal("expected non-nil annotations")
		}
		val, ok := got["aisphere.io/skills"]
		if !ok {
			t.Fatal("missing aisphere.io/skills key")
		}
		var decoded []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal([]byte(val), &decoded); err != nil {
			t.Fatalf("annotation value not valid JSON: %v", err)
		}
		if len(decoded) != 2 || decoded[0].Name != "ttt1" || decoded[0].Version != "v1.4.2" {
			t.Errorf("decoded = %+v, want [ttt1/v1.4.2, foo/v0.2.0]", decoded)
		}
	})
}
