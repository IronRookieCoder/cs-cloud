package localserver

import (
	"encoding/json"
	"strings"
	"testing"

	"cs-cloud/internal/localserver/apidocs"
)

func TestGeneratedSpecIsValidJSON(t *testing.T) {
	doc := apidocs.SwaggerInfo.ReadDoc()
	t.Logf("spec length: %d bytes", len(doc))
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("generated spec is not valid JSON: %v", err)
	}
}

func TestGeneratedSpecContainsPaths(t *testing.T) {
	doc := apidocs.SwaggerInfo.ReadDoc()
	var spec map[string]any
	if err := json.Unmarshal([]byte(doc), &spec); err != nil {
		t.Fatal(err)
	}
	paths, _ := spec["paths"].(map[string]any)
	if len(paths) == 0 {
		t.Error("no paths defined in generated spec")
	}
	defs, _ := spec["definitions"].(map[string]any)
	if len(defs) == 0 {
		t.Error("no definitions in generated spec")
	}
}

func TestGeneratedSpecAllEndpointsPresent(t *testing.T) {
	expectedPaths := []string{
		"/runtime/health",
		"/agents/config",
		"/agents/models/config",
		"/runtime/config",
		"/runtime/files",
		"/runtime/files/content",
		"/runtime/find/file",
		"/runtime/path",
		"/runtime/vcs",
		"/runtime/diff",
		"/runtime/diff/content",
		"/runtime/dispose",
		"/agents",
		"/agents/health",
		"/terminal",
	}
	doc := apidocs.SwaggerInfo.ReadDoc()
	var spec map[string]any
	json.Unmarshal([]byte(doc), &spec)
	paths, _ := spec["paths"].(map[string]any)
	for _, p := range expectedPaths {
		if _, ok := paths[p]; !ok {
			t.Errorf("missing path: %s", p)
		}
	}
}

func TestSwaggerUIHTMLIsValid(t *testing.T) {
	if !strings.Contains(swaggerUIHTML, "swagger-ui") {
		t.Error("swagger UI HTML missing swagger-ui reference")
	}
	if !strings.Contains(swaggerUIHTML, "openapi.json") {
		t.Error("swagger UI HTML missing openapi.json reference")
	}
}
