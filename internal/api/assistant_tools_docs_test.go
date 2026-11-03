package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTheUserGuideListsEveryAssistantTool(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "ui", "src", "pages", "docs", "content.ts"))
	if err != nil {
		t.Fatal(err)
	}
	guide := string(content)
	start := strings.Index(guide, "id: 'mcp'")
	if start < 0 {
		t.Fatal("the MCP guide is missing from the user guide")
	}
	table := guide[start:]
	toolsTopic := strings.Index(table, "id: 'tools'")
	if toolsTopic < 0 {
		t.Fatal("the MCP guide has no tools topic")
	}
	table = table[toolsTopic:]
	if end := strings.Index(table, "warn:"); end > 0 {
		table = table[:end]
	}
	for _, tool := range buildMockServerWithK8s().toolRegistry() {
		if !regexp.MustCompile(`(^|[^a-z_])` + regexp.QuoteMeta(tool.Name) + `([^a-z_]|$)`).MatchString(table) {
			t.Errorf("the MCP tools table in ui/src/pages/docs/content.ts does not list %s", tool.Name)
		}
	}
}
