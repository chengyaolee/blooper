package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/chengyaolee/blooper/internal/llm"
)

// Hierarchical working memory. The orchestrator, not the model, decides what
// enters the prompt. Each request is assembled from layers:
//
//	user rules  - persistent standing instructions from MEMORY.md (see rules.go)
//	task state  - the current goal and progress counters (small)
//	long-term   - structured summaries of compacted steps
//	pointers    - artifact IDs for raw tool output kept outside the context
//	scratchpad  - the last K steps, word for word
//
// Large tool outputs never enter the context. They go to the artifact store and
// the model gets a digest: size, a few sample lines, and lines that look like
// errors. It can ask for more with read_artifact (by pattern or line range),
// and every read is capped.

// ReadArtifactTool is the tool name the orchestrator answers itself.
const ReadArtifactTool = "read_artifact"

// Limits bound each memory layer. Sizes are in characters (~4 per token).
type Limits struct {
	InlineChars   int // tool outputs up to this size stay inline
	KeepSteps     int // scratchpad keeps at most this many recent steps verbatim
	ScratchChars  int // ...and compacts the oldest steps once it grows past this
	LongTermChars int // long-term layer is re-compacted once it grows past this
	SampleLines   int // head lines shown in a digest
	MatchLines    int // matching lines shown in a digest
	ReadLines     int // max lines returned by one read_artifact call
	ReadChars     int // max chars returned by one read_artifact call
	LineChars     int // stored lines longer than this are wrapped, so one-line blobs stay addressable
}

var DefaultLimits = Limits{
	InlineChars:   2000,
	KeepSteps:     6,
	ScratchChars:  16000,
	LongTermChars: 4000,
	SampleLines:   5,
	MatchLines:    10,
	ReadLines:     50,
	ReadChars:     6000,
	LineChars:     500,
}

// Artifact is raw tool output stored outside the context window.
type Artifact struct {
	ID     string
	Source string // tool call that produced it, e.g. file_read({"path":"app.log"})
	Lines  []string
	Bytes  int
}

// Record is a structured summary of steps evicted from the scratchpad.
type Record struct {
	Findings     []string `json:"findings"`
	QueriesTried []string `json:"queries_tried"`
	RuledOut     []string `json:"ruled_out"`
	ArtifactIDs  []string `json:"artifact_ids"`
}

// Orchestrator owns an agent's context window.
type Orchestrator struct {
	Engine llm.InferenceEngine
	Model  string // used for summarization
	Limits Limits
	Rules  *Rules // persistent user rules (MEMORY.md); nil disables them

	goal      string
	userText  strings.Builder // everything the user typed this session, for grounding remember_rule
	stepCount int
	steps     [][]llm.ChatMessage // scratchpad; a step is one turn plus its tool results
	longTerm  []Record
	artifacts map[string]*Artifact
	order     []string // artifact IDs in creation order
}

func NewOrchestrator(engine llm.InferenceEngine, model string) *Orchestrator {
	return &Orchestrator{Engine: engine, Model: model, Limits: DefaultLimits, Rules: &Rules{Dir: "data"}, artifacts: map[string]*Artifact{}}
}

// Begin starts a new user request. It returns any instructions that were
// promoted to MEMORY.md because the user has now repeated them.
func (o *Orchestrator) Begin(userMessage string) []string {
	o.goal = userMessage
	o.userText.WriteString(userMessage + "\n")
	o.AddStep(llm.ChatMessage{Role: "user", Content: userMessage})
	if o.Rules == nil {
		return nil
	}
	promoted, err := o.Rules.Observe(userMessage)
	if err != nil {
		log.Printf("⚠️  user rules: %v", err)
	}
	return promoted
}

// AddStep appends one step to the scratchpad. Steps are kept whole so that an
// assistant tool call is never separated from its results.
func (o *Orchestrator) AddStep(msgs ...llm.ChatMessage) {
	o.steps = append(o.steps, msgs)
	o.stepCount++
}

// Messages builds the layered prompt for the next model call.
func (o *Orchestrator) Messages(systemPrompt string) []llm.ChatMessage {
	var sb strings.Builder
	sb.WriteString(systemPrompt)

	if o.Rules != nil {
		if rules := o.Rules.List(); len(rules) > 0 {
			sb.WriteString("\n\n## User rules (always follow these)\n")
			for _, r := range rules {
				sb.WriteString("- " + r + "\n")
			}
		}
	}
	if len(o.longTerm) > 0 {
		sb.WriteString("\n\n## Long-term memory (summaries of earlier steps)\n")
		sb.WriteString(renderRecords(o.longTerm))
	}
	if len(o.order) > 0 {
		sb.WriteString("\n\n## Artifacts (raw data stored outside context; use read_artifact)\n")
		for _, id := range o.order {
			a := o.artifacts[id]
			fmt.Fprintf(&sb, "- %s: %s (%d lines, %d bytes)\n", a.ID, a.Source, len(a.Lines), a.Bytes)
		}
	}
	fmt.Fprintf(&sb, "\n\n## Task state\nGoal: %s\nSteps so far: %d (%d in scratchpad, older ones summarized above)\n",
		o.goal, o.stepCount, len(o.steps))

	msgs := []llm.ChatMessage{{Role: "system", Content: sb.String()}}
	for _, s := range o.steps {
		msgs = append(msgs, s...)
	}
	return msgs
}

// ToolResult turns raw tool output into the message the model sees. Output
// over the inline limit is stored as an artifact and replaced by a digest.
func (o *Orchestrator) ToolResult(callID, source, output string) llm.ChatMessage {
	msg := llm.ChatMessage{Role: "tool", ToolCallID: callID, Content: output}
	if len(output) <= o.Limits.InlineChars {
		return msg
	}
	a := o.store(source, output)
	msg.Content = o.digest(a)
	return msg
}

func (o *Orchestrator) store(source, output string) *Artifact {
	a := &Artifact{ID: fmt.Sprintf("art-%d", len(o.order)+1), Source: truncate(source, 200), Bytes: len(output)}
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		for len(line) > o.Limits.LineChars {
			a.Lines = append(a.Lines, line[:o.Limits.LineChars])
			line = line[o.Limits.LineChars:]
		}
		a.Lines = append(a.Lines, line)
	}
	o.artifacts[a.ID] = a
	o.order = append(o.order, a.ID)
	return a
}

var notableLine = regexp.MustCompile(`(?i)\b(error|err|fail(ed|ure)?|fatal|panic|exception|warn(ing)?|timeout|denied)\b`)

func (o *Orchestrator) digest(a *Artifact) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] Output of %s was %d lines / %d bytes, stored outside context.\n", a.ID, a.Source, len(a.Lines), a.Bytes)

	fmt.Fprintf(&sb, "First lines:\n")
	for i := 0; i < len(a.Lines) && i < o.Limits.SampleLines; i++ {
		fmt.Fprintf(&sb, "%d: %s\n", i+1, truncate(a.Lines[i], 200))
	}

	var matches []int
	for i, l := range a.Lines {
		if notableLine.MatchString(l) {
			matches = append(matches, i)
		}
	}
	if len(matches) > 0 {
		fmt.Fprintf(&sb, "Lines mentioning errors/warnings (%d total, showing up to %d):\n", len(matches), o.Limits.MatchLines)
		for _, i := range matches[:min(len(matches), o.Limits.MatchLines)] {
			fmt.Fprintf(&sb, "%d: %s\n", i+1, truncate(a.Lines[i], 200))
		}
	}
	fmt.Fprintf(&sb, `To see more, call %s with {"id":"%s","pattern":"<regex>"} or {"id":"%s","start":N,"end":M}.`, ReadArtifactTool, a.ID, a.ID)
	return sb.String()
}

// Tools are the tool definitions the orchestrator answers itself; the agent
// advertises them alongside its registry tools.
func (o *Orchestrator) Tools() []llm.ToolDef {
	defs := []llm.ToolDef{readArtifactDef()}
	if o.Rules != nil {
		defs = append(defs, llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name:        RememberRuleTool,
			Description: "Save a standing rule the user wants followed in all future conversations (e.g. 'never add co-author lines to commits'). Call this when the user asks you to remember something, or says always/never/from now on. Use the user's own wording.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"rule": map[string]interface{}{"type": "string", "description": "The rule, short and imperative, in the user's words"},
				},
				"required": []string{"rule"},
			},
		}})
	}
	return defs
}

// HandleTool answers calls to the orchestrator's own tools. ok is false for
// any other tool name.
func (o *Orchestrator) HandleTool(name, args string) (result string, ok bool) {
	switch {
	case name == ReadArtifactTool:
		return o.ReadArtifact(args), true
	case name == RememberRuleTool && o.Rules != nil:
		return o.rememberRule(args), true
	}
	return "", false
}

func (o *Orchestrator) rememberRule(args string) string {
	var p struct {
		Rule string `json:"rule"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Rule) == "" {
		return "error: invalid arguments"
	}
	if !groundedIn(p.Rule, o.userText.String()) {
		return "error: rule not saved; it must restate something the user said in this conversation, in their words"
	}
	added, err := o.Rules.Add(p.Rule)
	switch {
	case err != nil:
		return fmt.Sprintf("error: %v", err)
	case !added:
		return "Already remembered: an equivalent rule is in MEMORY.md."
	}
	return "Saved to MEMORY.md: " + p.Rule
}

func readArtifactDef() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        ReadArtifactTool,
		Description: "Read part of a stored artifact (large tool output kept outside context). Pass a regex pattern to get matching lines, or a 1-based start/end line range. Results are capped, so narrow your request instead of paging through everything.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id":      map[string]interface{}{"type": "string", "description": "Artifact ID, e.g. art-1"},
				"pattern": map[string]interface{}{"type": "string", "description": "Regex; returns matching lines with line numbers"},
				"start":   map[string]interface{}{"type": "integer", "description": "First line (1-based)"},
				"end":     map[string]interface{}{"type": "integer", "description": "Last line (inclusive)"},
			},
			"required": []string{"id"},
		},
	}}
}

// ReadArtifact answers a read_artifact call. The result is always capped.
func (o *Orchestrator) ReadArtifact(args string) string {
	var p struct {
		ID         string `json:"id"`
		Pattern    string `json:"pattern"`
		Start, End int
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "error: invalid arguments"
	}
	a, ok := o.artifacts[p.ID]
	if !ok {
		return fmt.Sprintf("error: unknown artifact %q", p.ID)
	}

	var idx []int
	header := ""
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return fmt.Sprintf("error: bad pattern: %v", err)
		}
		for i, l := range a.Lines {
			if re.MatchString(l) {
				idx = append(idx, i)
			}
		}
		header = fmt.Sprintf("[%s] %d lines match %q", a.ID, len(idx), p.Pattern)
	} else {
		start, end := max(p.Start, 1), p.End
		if end <= 0 || end > len(a.Lines) {
			end = len(a.Lines)
		}
		for i := start - 1; i < end; i++ {
			idx = append(idx, i)
		}
		header = fmt.Sprintf("[%s] lines %d-%d of %d", a.ID, start, end, len(a.Lines))
	}

	var sb strings.Builder
	sb.WriteString(header + "\n")
	for n, i := range idx {
		line := fmt.Sprintf("%d: %s\n", i+1, a.Lines[i])
		if n >= o.Limits.ReadLines || sb.Len()+len(line) > o.Limits.ReadChars {
			fmt.Fprintf(&sb, "... truncated: %d more lines (next is line %d). Narrow the pattern or range.", len(idx)-n, i+1)
			break
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// Compact moves the oldest scratchpad steps into long-term memory once the
// scratchpad passes its step or size limit, then re-compacts long-term memory
// if that has grown too large. The newest step always stays verbatim.
func (o *Orchestrator) Compact(ctx context.Context) {
	n := 0
	for size := stepsChars(o.steps); n < len(o.steps)-1; n++ {
		if len(o.steps)-n <= o.Limits.KeepSteps && size <= o.Limits.ScratchChars {
			break
		}
		size -= stepsChars(o.steps[n : n+1])
	}
	if n > 0 {
		evicted := o.steps[:n]
		o.steps = append([][]llm.ChatMessage(nil), o.steps[n:]...)
		o.longTerm = append(o.longTerm, o.summarize(ctx, renderSteps(evicted), stepQueries(evicted)))
	}

	// ponytail: re-summarizes the oldest half of long-term records into one. If the
	// summarizer keeps failing, records concatenate and stay over budget; add a hard
	// drop of the oldest findings if that ever matters.
	for i := 0; i < 3 && len(o.longTerm) > 1 && len(renderRecords(o.longTerm)) > o.Limits.LongTermChars; i++ {
		half := len(o.longTerm) / 2
		old := o.longTerm[:half]
		var queries []string
		for _, r := range old {
			queries = append(queries, r.QueriesTried...)
		}
		merged := o.summarize(ctx, renderRecords(old), queries)
		o.longTerm = append([]Record{merged}, o.longTerm[half:]...)
	}
}

const summarizePrompt = `You compress an agent's working memory. Read the material and reply with ONLY a JSON object:
{"findings": [...], "queries_tried": [...], "ruled_out": [...], "artifact_ids": [...]}
- findings: concrete facts learned (values, counts, error messages, root causes). Be specific and terse.
- queries_tried: tool calls / SQL / searches already run, so they are not repeated.
- ruled_out: hypotheses that were checked and rejected, with the reason.
- artifact_ids: every artifact ID (art-N) worth revisiting.
No prose outside the JSON.`

var artifactID = regexp.MustCompile(`\bart-\d+\b`)

// summarize asks the model for a Record. If the call or the JSON parse fails, it
// falls back to a deterministic record so tool calls and artifact pointers are
// never lost.
func (o *Orchestrator) summarize(ctx context.Context, material string, queries []string) Record {
	var rec Record
	resp, err := o.Engine.Generate(ctx, &llm.ChatRequest{
		Model: o.Model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: summarizePrompt},
			{Role: "user", Content: material},
		},
	})
	if err == nil {
		s := resp.Message.Content
		if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
			err = json.Unmarshal([]byte(s[i:j+1]), &rec)
		} else {
			err = fmt.Errorf("no JSON in summary")
		}
	}
	if err != nil {
		rec = Record{Findings: []string{"(summarization failed: " + truncate(err.Error(), 100) + ")"}, QueriesTried: queries}
	}
	rec.ArtifactIDs = union(rec.ArtifactIDs, artifactID.FindAllString(material, -1))
	return rec
}

func renderSteps(steps [][]llm.ChatMessage) string {
	var sb strings.Builder
	for _, s := range steps {
		for _, m := range s {
			if m.Content != "" {
				fmt.Fprintf(&sb, "[%s] %s\n", m.Role, m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "[%s call] %s(%s)\n", m.Role, tc.Function.Name, tc.Function.Arguments)
			}
		}
	}
	return sb.String()
}

func stepQueries(steps [][]llm.ChatMessage) []string {
	var q []string
	for _, s := range steps {
		for _, m := range s {
			for _, tc := range m.ToolCalls {
				q = append(q, truncate(tc.Function.Name+"("+tc.Function.Arguments+")", 200))
			}
		}
	}
	return q
}

func renderRecords(recs []Record) string {
	var sb strings.Builder
	for i, r := range recs {
		fmt.Fprintf(&sb, "[%d]", i+1)
		for _, f := range []struct {
			label string
			items []string
		}{{"findings", r.Findings}, {"tried", r.QueriesTried}, {"ruled out", r.RuledOut}, {"artifacts", r.ArtifactIDs}} {
			if len(f.items) > 0 {
				fmt.Fprintf(&sb, " %s: %s;", f.label, strings.Join(f.items, " | "))
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func stepsChars(steps [][]llm.ChatMessage) int {
	n := 0
	for _, s := range steps {
		for _, m := range s {
			n += len(m.Content)
			for _, tc := range m.ToolCalls {
				n += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
	}
	return n
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(a, b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
