package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// User rules persist across conversations in MEMORY.md and are injected into
// every prompt. A rule gets there two ways:
//   - explicitly: the user asks for it to be remembered and the model calls remember_rule
//   - by repetition: the same standing instruction ("do not co-author") shows up
//     in RepeatThreshold separate user messages
//
// Counts for not-yet-promoted instructions live in instructions.json beside MEMORY.md.

const RememberRuleTool = "remember_rule"

// RepeatThreshold is how many separate messages must repeat an instruction before it becomes a rule.
const RepeatThreshold = 2

// ponytail: process-wide lock; multiple blooper processes sharing a data dir would need file locking.
var rulesMu sync.Mutex

// Rules is the MEMORY.md rule file plus its repetition counts.
type Rules struct {
	Dir string
}

func (r *Rules) memoryPath() string { return filepath.Join(r.Dir, "MEMORY.md") }
func (r *Rules) countsPath() string { return filepath.Join(r.Dir, "instructions.json") }

// List returns the rules in MEMORY.md (its "- " bullet lines). Hand edits are fine.
func (r *Rules) List() []string {
	data, err := os.ReadFile(r.memoryPath())
	if err != nil {
		return nil
	}
	var rules []string
	for _, line := range strings.Split(string(data), "\n") {
		if rule, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok && rule != "" {
			rules = append(rules, rule)
		}
	}
	return rules
}

// Add appends a rule to MEMORY.md unless an equivalent one is already there.
func (r *Rules) Add(rule string) (added bool, err error) {
	rulesMu.Lock()
	defer rulesMu.Unlock()
	return r.add(rule)
}

func (r *Rules) add(rule string) (bool, error) {
	rule = strings.TrimSpace(strings.ReplaceAll(rule, "\n", " "))
	if rule == "" {
		return false, fmt.Errorf("empty rule")
	}
	for _, existing := range r.List() {
		if similar(existing, rule) {
			return false, nil
		}
	}
	if err := os.MkdirAll(r.Dir, 0755); err != nil {
		return false, err
	}
	header := ""
	if _, err := os.Stat(r.memoryPath()); os.IsNotExist(err) {
		header = "# User rules\n\nStanding instructions the agent follows in every conversation.\n\n"
	}
	f, err := os.OpenFile(r.memoryPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s- %s\n", header, rule)
	return err == nil, err
}

type instruction struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

const maxTracked = 500

// Observe counts the standing instructions in a user message and promotes any
// that reach RepeatThreshold into MEMORY.md. It returns the newly added rules.
func (r *Rules) Observe(userMessage string) ([]string, error) {
	found := extractInstructions(userMessage)
	if len(found) == 0 {
		return nil, nil
	}

	rulesMu.Lock()
	defer rulesMu.Unlock()

	var tracked []instruction
	if data, err := os.ReadFile(r.countsPath()); err == nil {
		if err := json.Unmarshal(data, &tracked); err != nil {
			return nil, fmt.Errorf("corrupt %s: %w", r.countsPath(), err)
		}
	}

	var promoted []string
	for _, text := range found {
		i := 0
		for i < len(tracked) && !similar(tracked[i].Text, text) {
			i++
		}
		if i == len(tracked) {
			tracked = append(tracked, instruction{Text: text})
		}
		tracked[i].Count++
		if tracked[i].Count < RepeatThreshold {
			continue
		}
		added, err := r.add(tracked[i].Text)
		if err != nil {
			return promoted, err
		}
		if added {
			promoted = append(promoted, tracked[i].Text)
		}
		tracked = append(tracked[:i], tracked[i+1:]...) // it's a rule now; stop counting
	}
	if len(tracked) > maxTracked {
		tracked = tracked[len(tracked)-maxTracked:] // forget the oldest one-offs
	}

	data, _ := json.MarshalIndent(tracked, "", "  ")
	if err := os.MkdirAll(r.Dir, 0755); err != nil {
		return promoted, err
	}
	return promoted, os.WriteFile(r.countsPath(), data, 0644)
}

var (
	sentenceSplit = regexp.MustCompile(`[.!?;\n]+`)
	// Markers of a standing instruction, as opposed to a one-off task request.
	directive = regexp.MustCompile(`^(please |and |also )*(always|never|do not|don't|dont|stop|avoid|make sure|from now on|remember)\b`)
	nonWord   = regexp.MustCompile(`[^a-z0-9 ]+`)
)

// extractInstructions returns the directive sentences in a message, deduplicated.
func extractInstructions(msg string) []string {
	var out []string
	for _, s := range sentenceSplit.Split(msg, -1) {
		s = strings.Trim(strings.TrimSpace(s), `"'`+"`")
		words := len(strings.Fields(s))
		if words < 2 || words > 25 || !directive.MatchString(strings.ToLower(s)) {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || similar(o, s)
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}

func wordSet(s string) map[string]bool {
	s = strings.ToLower(s)
	s = strings.NewReplacer("don't", "do not", "dont", "do not", "-", "").Replace(s)
	set := map[string]bool{}
	for _, w := range strings.Fields(nonWord.ReplaceAllString(s, " ")) {
		if w != "please" {
			set[w] = true
		}
	}
	return set
}

// similar reports whether two instructions say the same thing (Jaccard >= 0.7 on words).
// ponytail: lexical match; "don't add co-author lines" vs "skip the Co-Authored-By trailer"
// won't merge. Swap in embeddings if paraphrases matter.
func similar(a, b string) bool {
	wa, wb := wordSet(a), wordSet(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	inter := 0
	for w := range wa {
		if wb[w] {
			inter++
		}
	}
	return float64(inter)/float64(len(wa)+len(wb)-inter) >= 0.7
}

// groundedIn reports whether most of the rule's words appear in what the user
// actually typed. remember_rule requires this so a web page or log read by a
// tool can't plant a permanent rule via prompt injection.
func groundedIn(rule string, userText string) bool {
	rw, uw := wordSet(rule), wordSet(userText)
	if len(rw) == 0 {
		return false
	}
	hit := 0
	for w := range rw {
		if uw[w] {
			hit++
		}
	}
	return float64(hit)/float64(len(rw)) >= 0.6
}
