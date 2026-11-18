package llm

import (
	"fmt"
	"slices"
)

// SelectModel uses the configured fallback only when the preferred model is absent.
func SelectModel(available []string, preferred, fallback string) (string, error) {
	for _, name := range []string{preferred, fallback} {
		if name != "" && slices.Contains(available, name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("model %q is unavailable (fallback %q); installed models: %v. Pull the model with ollama pull or update model.preferred", preferred, fallback, available)
}
