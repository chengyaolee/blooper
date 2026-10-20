package core

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
)

// RunChecks evaluates a task's acceptance checks against its result. Checks are deterministic,
// so a failure is an objective rejection that needs no LLM judgment. file_exists paths are
// confined to outputDir.
func RunChecks(checks []memory.Check, result, outputDir string) []memory.CheckResult {
	out := make([]memory.CheckResult, 0, len(checks))
	for _, c := range checks {
		r := memory.CheckResult{Check: c}
		switch c.Kind {
		case "contains":
			r.Passed = strings.Contains(strings.ToLower(result), strings.ToLower(c.Arg))
		case "regex":
			re, err := regexp.Compile(c.Arg)
			r.Passed = err == nil && re.MatchString(result)
		case "min_length", "max_length":
			n, _ := strconv.Atoi(c.Arg)
			got := utf8.RuneCountInString(result)
			r.Passed = (c.Kind == "min_length" && got >= n) || (c.Kind == "max_length" && got <= n)
			r.Detail = fmt.Sprintf("%d characters", got)
		case "json":
			r.Passed = json.Valid([]byte(strings.TrimSpace(result)))
		case "file_exists":
			path, err := tools.SanitizePath(outputDir, c.Arg)
			if err != nil {
				r.Detail = err.Error()
				break
			}
			info, err := os.Stat(path)
			r.Passed = err == nil && !info.IsDir() && info.Size() > 0
			if !r.Passed {
				r.Detail = "missing or empty"
			}
		default:
			r.Detail = "unknown check kind"
		}
		out = append(out, r)
	}
	return out
}

// failedChecks describes failed checks for verifier feedback, or "" if all passed.
func failedChecks(results []memory.CheckResult) string {
	var failed []string
	for _, r := range results {
		if !r.Passed {
			s := r.Kind
			if r.Arg != "" {
				s += " " + strconv.Quote(r.Arg)
			}
			if r.Detail != "" {
				s += " (" + r.Detail + ")"
			}
			failed = append(failed, s)
		}
	}
	return strings.Join(failed, "; ")
}
