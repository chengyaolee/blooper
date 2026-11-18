package remote

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"slices"
	"strings"

	"github.com/chengyaolee/blooper/internal/llm"
)

type ComputerAction struct {
	Action    string   `json:"action"`
	X         *int     `json:"x,omitempty"`
	Y         *int     `json:"y,omitempty"`
	Text      string   `json:"text,omitempty"`
	Keys      []string `json:"keys,omitempty"`
	Direction string   `json:"direction,omitempty"`
	Amount    int      `json:"amount,omitempty"`
}

func ComputerSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"action"},
		"properties": map[string]any{
			"action":    map[string]any{"type": "string", "enum": []string{"screenshot", "click", "double_click", "right_click", "type", "key", "scroll"}},
			"x":         map[string]any{"type": "integer", "minimum": 0, "maximum": 1599, "description": "Horizontal pixel in the latest screenshot; clicks only"},
			"y":         map[string]any{"type": "integer", "minimum": 0, "maximum": 1599, "description": "Vertical pixel in the latest screenshot; clicks only"},
			"text":      map[string]any{"type": "string", "maxLength": 2000, "description": "Literal text to type; type only"},
			"keys":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 5, "description": "key only: modifiers CMD, CTRL, ALT, SHIFT then one A-Z, 0-9, ENTER, TAB, ESC, SPACE, BACKSPACE, DELETE, LEFT, RIGHT, UP, DOWN, HOME, END, PAGEUP or PAGEDOWN"},
			"direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}},
			"amount":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "description": "Scroll distance in pixels; scroll only"},
		},
	}
}

// Validate at both coordinator and runner boundaries. No scripts, commands,
// arbitrary keycodes, or ignored arguments cross into the native helper.
func ParseComputerAction(data json.RawMessage) (ComputerAction, error) {
	var a ComputerAction
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return a, fmt.Errorf("invalid computer action: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return a, fmt.Errorf("invalid trailing computer arguments")
	}
	allowed := []string{"action"}
	valid := false
	switch a.Action {
	case "screenshot":
		valid = true
	case "click", "double_click", "right_click":
		allowed = append(allowed, "x", "y")
		valid = a.X != nil && a.Y != nil && *a.X >= 0 && *a.X < 1600 && *a.Y >= 0 && *a.Y < 1600
	case "type":
		allowed = append(allowed, "text")
		valid = len([]rune(a.Text)) > 0 && len([]rune(a.Text)) <= 2000
	case "key":
		allowed = append(allowed, "keys")
		valid = len(a.Keys) > 0 && len(a.Keys) <= 5
		seen := map[string]bool{}
		for i, key := range a.Keys {
			modifier := slices.Contains([]string{"CMD", "CTRL", "ALT", "SHIFT"}, key)
			valid = valid && !seen[key]
			seen[key] = true
			if i < len(a.Keys)-1 {
				valid = valid && modifier
				continue
			}
			letter := len(key) == 1 && strings.Contains("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", key)
			named := slices.Contains([]string{"ENTER", "TAB", "ESC", "SPACE", "BACKSPACE", "DELETE", "LEFT", "RIGHT", "UP", "DOWN", "HOME", "END", "PAGEUP", "PAGEDOWN"}, key)
			valid = valid && !modifier && (letter || named)
		}
	case "scroll":
		allowed = append(allowed, "direction", "amount")
		valid = a.Amount >= 1 && a.Amount <= 1000 && slices.Contains([]string{"up", "down", "left", "right"}, a.Direction)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for field := range fields {
		valid = valid && slices.Contains(allowed, field)
	}
	if !valid {
		return a, fmt.Errorf("invalid arguments for computer action %q; use only the fields required by that action", a.Action)
	}
	return a, nil
}

func ValidateScreen(img *llm.Image) error {
	if len(img.Data) > 700<<10 || img.Width < 1 || img.Height < 1 || img.Width > 1600 || img.Height > 1600 {
		return fmt.Errorf("invalid screenshot size")
	}
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return fmt.Errorf("invalid screenshot encoding")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != img.Width || cfg.Height != img.Height {
		return fmt.Errorf("invalid screenshot dimensions or JPEG")
	}
	return nil
}
