// Package biz skillzip_bridge.go — bridges internal/skillzip to biz.
//
// This file is the ONLY place the biz package imports internal/skillzip.
// Keeping the import isolated here lets biz tests stub skillzipParse
// without pulling archive/zip into their dependencies.

package biz

import "github.com/aisphereio/aisphere-hub/internal/skillzip"

func init() {
	skillzipParse = parseSkillZip
}

func parseSkillZip(b []byte) (*skillzipBridgeResult, error) {
	parsed, err := skillzip.ParseSkillFromZip(b)
	if err != nil {
		return nil, err
	}
	resources := make([]skillzipBridgeResource, 0, len(parsed.Resources))
	for _, r := range parsed.Resources {
		resources = append(resources, skillzipBridgeResource{
			Path:    r.Path,
			Name:    r.Name,
			Type:    r.Type,
			Content: r.Content,
			Size:    r.Size,
			Binary:  r.Binary,
		})
	}
	return &skillzipBridgeResult{
		Name:        parsed.Name,
		Description: parsed.Description,
		Version:     parsed.Version,
		SkillMD:     parsed.SkillMD,
		Metadata:    parsed.Metadata,
		Resources:   resources,
	}, nil
}
