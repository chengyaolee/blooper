package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathTraversalPrevention(t *testing.T) {
	tmpBase := t.TempDir()

	writeTool := NewFileWriteTool(tmpBase)

	// Attempt path traversal attack
	maliciousPayload, _ := json.Marshal(map[string]string{
		"filename": "../../../escaped.txt",
		"content":  "hacked",
	})

	res, err := writeTool.Execute(context.Background(), maliciousPayload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Error, "security violation") {
		t.Errorf("expected security violation error, got: %v", res)
	}

	// Normal safe write
	safePayload, _ := json.Marshal(map[string]string{
		"filename": "subfolder/safe.txt",
		"content":  "legitimate content",
	})
	res, err = writeTool.Execute(context.Background(), safePayload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error != "" {
		t.Errorf("expected clean write, got error: %s", res.Error)
	}

	// Verify file was written inside tmpBase
	expectedFile := filepath.Join(tmpBase, "subfolder", "safe.txt")
	content, err := os.ReadFile(expectedFile)
	if err != nil || string(content) != "legitimate content" {
		t.Errorf("file was not written correctly to %s", expectedFile)
	}

	// Test FileReadTool
	readTool := NewFileReadTool(tmpBase)

	// Safe read
	readPayload, _ := json.Marshal(map[string]string{
		"path": "subfolder/safe.txt",
	})
	readRes, err := readTool.Execute(context.Background(), readPayload)
	if err != nil || readRes.Error != "" || readRes.Output != "legitimate content" {
		t.Errorf("expected legitimate content, got: %v, err: %v", readRes, err)
	}

	// Traversal read
	traversalRead, _ := json.Marshal(map[string]string{
		"path": "../../../etc/passwd",
	})
	readRes, err = readTool.Execute(context.Background(), traversalRead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(readRes.Error, "security violation") {
		t.Errorf("expected security violation for traversal read, got: %v", readRes)
	}
}

func TestSSRFPrevention(t *testing.T) {
	forbiddenURLs := []string{
		"http://localhost:8080/secret",
		"http://127.0.0.1:11434/api/tags",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"ftp://evil.com/file",
	}

	for _, rawURL := range forbiddenURLs {
		err := isSafeURL(rawURL)
		if err == nil {
			t.Errorf("expected isSafeURL to reject %s, but it passed", rawURL)
		}
	}

	safeURLs := []string{
		"https://example.com",
		"https://duckduckgo.com/about",
	}

	for _, rawURL := range safeURLs {
		err := isSafeURL(rawURL)
		if err != nil {
			t.Errorf("expected isSafeURL to allow %s, got error: %v", rawURL, err)
		}
	}
}
