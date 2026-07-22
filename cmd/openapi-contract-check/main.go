package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const kernelErrorReference = "#/definitions/KernelErrorResponse"

type options struct {
	input   string
	output  string
	title   string
	version string
}

func main() {
	var opts options
	flag.StringVar(&opts.input, "input", "", "input Swagger 2.0 JSON file")
	flag.StringVar(&opts.output, "output", "", "normalized Swagger 2.0 JSON file")
	flag.StringVar(&opts.title, "title", "", "API title")
	flag.StringVar(&opts.version, "version", "", "API contract version")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "openapi-contract-check:", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if strings.TrimSpace(opts.input) == "" {
		return errors.New("--input is required")
	}
	if strings.TrimSpace(opts.output) == "" {
		return errors.New("--output is required")
	}
	if strings.TrimSpace(opts.title) == "" {
		return errors.New("--title is required")
	}
	if strings.TrimSpace(opts.version) == "" {
		return errors.New("--version is required")
	}

	raw, err := os.ReadFile(opts.input)
	if err != nil {
		return fmt.Errorf("read %s: %w", opts.input, err)
	}
	document, err := decodeDocument(raw)
	if err != nil {
		return err
	}
	normalizeDocument(document, opts.title, opts.version)
	if err := validateDocument(document, opts.title); err != nil {
		return err
	}
	normalized, err := marshalDocument(document)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(opts.output, normalized, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", opts.output, err)
	}
	return nil
}

func decodeDocument(raw []byte) (map[string]any, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode Swagger JSON: %w", err)
	}
	if document == nil {
		return nil, errors.New("Swagger document is empty")
	}
	if swagger, _ := document["swagger"].(string); swagger != "2.0" {
		return nil, fmt.Errorf("swagger = %q, want %q", swagger, "2.0")
	}
	return document, nil
}

func normalizeDocument(document map[string]any, title, version string) {
	info := object(document, "info")
	info["title"] = strings.TrimSpace(title)
	info["version"] = strings.TrimSpace(version)

	definitions := object(document, "definitions")
	definitions["KernelErrorResponse"] = map[string]any{
		"type":     "object",
		"required": []any{"code", "message"},
		"properties": map[string]any{
			"code":       map[string]any{"type": "string", "description": "Stable business error code."},
			"message":    map[string]any{"type": "string", "description": "Safe error message."},
			"request_id": map[string]any{"type": "string", "description": "Request correlation identifier."},
			"trace_id":   map[string]any{"type": "string", "description": "Distributed trace identifier."},
			"metadata": map[string]any{
				"type":                 "object",
				"description":          "Safe structured error metadata.",
				"additionalProperties": map[string]any{},
			},
		},
	}

	forEachOperation(document, func(_ string, _ string, operation map[string]any) {
		responses, ok := operation["responses"].(map[string]any)
		if !ok {
			return
		}
		defaultResponse, ok := responses["default"].(map[string]any)
		if !ok {
			return
		}
		defaultResponse["schema"] = map[string]any{"$ref": kernelErrorReference}
	})
}

func validateDocument(document map[string]any, expectedTitle string) error {
	var problems []string
	info, _ := document["info"].(map[string]any)
	if title := text(info["title"]); title != expectedTitle {
		problems = append(problems, fmt.Sprintf("info.title = %q, want %q", title, expectedTitle))
	}
	if version := text(info["version"]); version == "" || version == "version not set" {
		problems = append(problems, fmt.Sprintf("info.version must be set, got %q", version))
	}

	seen := map[string]struct{}{}
	forEachOperation(document, func(path, method string, operation map[string]any) {
		key := strings.ToUpper(method) + " " + path
		if _, ok := seen[key]; ok {
			problems = append(problems, "duplicate operation "+key)
		}
		seen[key] = struct{}{}
		if text(operation["operationId"]) == "" {
			problems = append(problems, key+" has no operationId")
		}
		if tags, ok := operation["tags"].([]any); !ok || len(tags) == 0 {
			problems = append(problems, key+" has no tags")
		}
		responses, _ := operation["responses"].(map[string]any)
		defaultResponse, _ := responses["default"].(map[string]any)
		schema, _ := defaultResponse["schema"].(map[string]any)
		if reference := text(schema["$ref"]); reference != kernelErrorReference {
			problems = append(problems, fmt.Sprintf("%s default error ref = %q", key, reference))
		}
	})

	definitions, _ := document["definitions"].(map[string]any)
	errorDefinition, _ := definitions["KernelErrorResponse"].(map[string]any)
	properties, _ := errorDefinition["properties"].(map[string]any)
	for _, field := range []string{"code", "message", "request_id", "trace_id", "metadata"} {
		if _, ok := properties[field]; !ok {
			problems = append(problems, "KernelErrorResponse has no "+field+" field")
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New(strings.Join(problems, "\n"))
}

func forEachOperation(document map[string]any, visit func(path, method string, operation map[string]any)) {
	paths, _ := document["paths"].(map[string]any)
	for path, pathValue := range paths {
		pathItem, _ := pathValue.(map[string]any)
		for method, operationValue := range pathItem {
			if !operationMethod(method) {
				continue
			}
			operation, ok := operationValue.(map[string]any)
			if ok {
				visit(path, method, operation)
			}
		}
	}
}

func object(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	parent[key] = value
	return value
}

func text(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}

func operationMethod(value string) bool {
	switch strings.ToLower(value) {
	case "get", "put", "post", "delete", "patch", "head", "options", "trace":
		return true
	default:
		return false
	}
}

func marshalDocument(document map[string]any) ([]byte, error) {
	result, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode normalized Swagger JSON: %w", err)
	}
	return append(result, '\n'), nil
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".openapi-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryPath, path)
}
