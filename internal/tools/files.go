package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chengyaolee/blooper/internal/llm"
)

// sanitizePath ensures that requested paths stay within allowed directories,
// preventing directory traversal attacks (e.g. ../../../etc/passwd).
func sanitizePath(baseDir, requestedPath string) (string, error) {
	cleanBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("invalid base directory: %w", err)
	}

	// Clean the requested path
	cleanRequested := filepath.Clean(requestedPath)
	// If relative, join with baseDir
	var target string
	if filepath.IsAbs(cleanRequested) {
		target = cleanRequested
	} else {
		target = filepath.Join(cleanBase, cleanRequested)
	}

	cleanTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}

	// Verify that cleanTarget starts with cleanBase
	relative, relErr := filepath.Rel(cleanBase, cleanTarget)
	if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path escapes permitted directory %s", baseDir)
	}

	return cleanTarget, nil
}

// FileWriteTool allows agents to save research reports or drafts to disk safely.
type FileWriteTool struct {
	BaseDir string
}

func NewFileWriteTool(baseDir string) *FileWriteTool {
	if baseDir == "" {
		baseDir = "data/outputs"
	}
	_ = os.MkdirAll(baseDir, 0755)
	return &FileWriteTool{BaseDir: baseDir}
}

func (t *FileWriteTool) Name() string {
	return "file_write"
}

func (t *FileWriteTool) Description() string {
	return "Safely writes text content to a file within the allowed outputs directory."
}

func (t *FileWriteTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"filename": map[string]interface{}{
					"type":        "string",
					"description": "Relative filename to write (e.g. report.md, email_draft.txt)",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Text content to write",
				},
			},
			"required": []string{"filename", "content"},
		},
	}
}

func (t *FileWriteTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var params struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}

	safePath, err := sanitizePath(t.BaseDir, params.Filename)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("security violation: %v", err)}, nil
	}

	// Ensure parent dir exists
	parentDir := filepath.Dir(safePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return &ToolResult{Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}

	if err := os.WriteFile(safePath, []byte(params.Content), 0644); err != nil {
		return &ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}

	return &ToolResult{
		Output: fmt.Sprintf("File successfully written to %s (%d bytes)", safePath, len(params.Content)),
	}, nil
}

func (t *FileWriteTool) RequiresApproval() bool {
	return false
}

// FileReadTool allows agents to read local research or draft files safely.
type FileReadTool struct {
	AllowedDirs []string
}

func NewFileReadTool(allowedDirs ...string) *FileReadTool {
	if len(allowedDirs) == 0 {
		allowedDirs = []string{"data/outputs", "agents", "user_agents"}
	}
	return &FileReadTool{AllowedDirs: allowedDirs}
}

func (t *FileReadTool) Name() string {
	return "file_read"
}

func (t *FileReadTool) Description() string {
	return "Reads the text content of a file within permitted directories (e.g. data/outputs, agents)."
}

func (t *FileReadTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Path to the file to read",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *FileReadTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}

	// Check if path is valid in at least one allowed directory
	var safePath string
	var err error
	var allowed bool

	for _, dir := range t.AllowedDirs {
		sp, sErr := sanitizePath(dir, params.Path)
		if sErr == nil {
			safePath = sp
			allowed = true
			break
		}
	}

	if !allowed {
		return &ToolResult{Error: fmt.Sprintf("security violation: path %q is outside permitted directories", params.Path)}, nil
	}

	// Limit read size to 1MB to prevent OOM
	fi, err := os.Stat(safePath)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("file not found: %v", err)}, nil
	}
	if fi.Size() > 1024*1024 {
		return &ToolResult{Error: "file too large (max 1MB)"}, nil
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}

	return &ToolResult{
		Output: string(data),
	}, nil
}

func (t *FileReadTool) RequiresApproval() bool {
	return false
}
