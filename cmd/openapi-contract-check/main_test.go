package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedOpenAPIConsumerContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi", "aisphere-hub.swagger.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertConsumerContract(t, raw)
}

func TestNormalizeDocumentProducesConsumerContract(t *testing.T) {
	document := testDocument()
	normalizeDocument(document, "Aisphere Hub API", "v1")
	if err := validateDocument(document, "Aisphere Hub API"); err != nil {
		t.Fatal(err)
	}
	raw, err := marshalDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	assertConsumerContract(t, raw)

	second, err := marshalDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(second) {
		t.Fatal("normalized OpenAPI encoding is not deterministic")
	}
}

func TestValidateDocumentRejectsMissingOperationMetadata(t *testing.T) {
	document := testDocument()
	normalizeDocument(document, "Aisphere Hub API", "v1")
	operation := document["paths"].(map[string]any)["/v1/things"].(map[string]any)["get"].(map[string]any)
	delete(operation, "operationId")
	delete(operation, "tags")

	err := validateDocument(document, "Aisphere Hub API")
	if err == nil || !strings.Contains(err.Error(), "has no operationId") || !strings.Contains(err.Error(), "has no tags") {
		t.Fatalf("validateDocument() error = %v", err)
	}
}

func testDocument() map[string]any {
	return map[string]any{
		"swagger": "2.0",
		"info":    map[string]any{"title": "generated", "version": "version not set"},
		"paths": map[string]any{
			"/v1/things": map[string]any{
				"get": map[string]any{
					"operationId": "ThingService_GetThing",
					"tags":        []any{"ThingService"},
					"responses": map[string]any{
						"200":     map[string]any{"description": "OK"},
						"default": map[string]any{"description": "Error", "schema": map[string]any{"$ref": "#/definitions/rpcStatus"}},
					},
				},
			},
		},
		"definitions": map[string]any{},
	}
}

func assertConsumerContract(t *testing.T, raw []byte) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}

	info, _ := document["info"].(map[string]any)
	if got := stringValue(info["title"]); got != "Aisphere Hub API" {
		t.Errorf("info.title = %q, want %q", got, "Aisphere Hub API")
	}
	if got := stringValue(info["version"]); got == "" || got == "version not set" {
		t.Errorf("info.version must be set, got %q", got)
	}

	seen := map[string]struct{}{}
	paths, _ := document["paths"].(map[string]any)
	for path, pathValue := range paths {
		pathItem, _ := pathValue.(map[string]any)
		for method, operationValue := range pathItem {
			if !isHTTPMethod(method) {
				continue
			}
			key := strings.ToUpper(method) + " " + path
			if _, ok := seen[key]; ok {
				t.Errorf("duplicate operation %s", key)
			}
			seen[key] = struct{}{}
			operation, _ := operationValue.(map[string]any)
			if stringValue(operation["operationId"]) == "" {
				t.Errorf("%s has no operationId", key)
			}
			if tags, ok := operation["tags"].([]any); !ok || len(tags) == 0 {
				t.Errorf("%s has no tags", key)
			}
			responses, _ := operation["responses"].(map[string]any)
			defaultResponse, _ := responses["default"].(map[string]any)
			schema, _ := defaultResponse["schema"].(map[string]any)
			if got := stringValue(schema["$ref"]); got != "#/definitions/KernelErrorResponse" {
				t.Errorf("%s default error ref = %q", key, got)
			}
		}
	}

	definitions, _ := document["definitions"].(map[string]any)
	errorDefinition, _ := definitions["KernelErrorResponse"].(map[string]any)
	properties, _ := errorDefinition["properties"].(map[string]any)
	for _, field := range []string{"code", "message", "request_id", "trace_id", "metadata"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("KernelErrorResponse has no %s field", field)
		}
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func isHTTPMethod(value string) bool {
	switch strings.ToLower(value) {
	case "get", "put", "post", "delete", "patch", "head", "options", "trace":
		return true
	default:
		return false
	}
}
