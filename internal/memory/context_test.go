package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chengyaolee/blooper/internal/llm"
)

// fakeEngine returns a canned reply and records what it was asked to summarize.
type fakeEngine struct {
	reply string
	err   error
	seen  []string
}

func (f *fakeEngine) Generate(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	f.seen = append(f.seen, req.Messages[len(req.Messages)-1].Content)
	if f.err != nil {
		return nil, f.err
	}
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: f.reply}}, nil
}
func (f *fakeEngine) Stream(context.Context, *llm.ChatRequest, chan<- llm.StreamChunk) error {
	return nil
}
func (f *fakeEngine) KVStats() (*llm.KVMetrics, error)             { return nil, nil }
func (f *fakeEngine) ListModels(context.Context) ([]string, error) { return nil, nil }

func bigLog(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		if i == 4200 {
			sb.WriteString("2026-10-20 ERROR db: connection refused host=pg-7\n")
			continue
		}
		fmt.Fprintf(&sb, "2026-10-20 INFO request id=%d ok\n", i)
	}
	return sb.String()
}

func toolStep(o *Orchestrator, id, args, output string) {
	call := llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Function: llm.ToolCallFunction{Name: "file_read", Arguments: args}}}}
	o.AddStep(call, o.ToolResult(id, "file_read("+args+")", output))
}

func TestLargeOutputBecomesArtifact(t *testing.T) {
	o := NewOrchestrator(&fakeEngine{}, "m")
	o.Begin("why is the app failing?")
	log := bigLog(10000)
	toolStep(o, "c1", `{"path":"app.log"}`, log)

	var prompt strings.Builder
	for _, m := range o.Messages("persona") {
		prompt.WriteString(m.Content)
	}
	p := prompt.String()
	if len(p) > 3000 {
		t.Fatalf("prompt is %d chars; raw log leaked into context", len(p))
	}
	for _, want := range []string{"art-1", "10000 lines", "4200: 2026-10-20 ERROR db: connection refused", "Goal: why is the app failing?", "read_artifact"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}

	// Pattern read returns only matches.
	got := o.ReadArtifact(`{"id":"art-1","pattern":"id=77\\b"}`)
	if !strings.Contains(got, "1 lines match") || !strings.Contains(got, "77: 2026-10-20 INFO request id=77 ok") {
		t.Errorf("pattern read: %s", got)
	}
	// Range reads are capped, never the whole file.
	got = o.ReadArtifact(`{"id":"art-1","start":1,"end":10000}`)
	if len(got) > o.Limits.ReadChars+200 || !strings.Contains(got, "truncated") {
		t.Errorf("range read not capped: %d chars", len(got))
	}
	if got := o.ReadArtifact(`{"id":"art-9"}`); !strings.HasPrefix(got, "error") {
		t.Errorf("unknown id: %s", got)
	}

	// Small output stays inline.
	if m := o.ToolResult("c2", "x", "short"); m.Content != "short" {
		t.Errorf("small output was rewritten: %q", m.Content)
	}
}

func TestLongLinesAreWrapped(t *testing.T) {
	o := NewOrchestrator(&fakeEngine{}, "m")
	o.ToolResult("c", "sql", strings.Repeat("x", 5000))
	if n := len(o.artifacts["art-1"].Lines); n != 10 {
		t.Fatalf("one 5000-char line should wrap into 10 lines, got %d", n)
	}
}

func TestCompactionEvictsOldestIntoRecord(t *testing.T) {
	eng := &fakeEngine{reply: `Sure: {"findings":["pg-7 refuses connections"],"queries_tried":["file_read app.log"],"ruled_out":["disk full: df shows 40%"],"artifact_ids":[]}`}
	o := NewOrchestrator(eng, "m")
	o.Limits.KeepSteps = 3
	o.Begin("debug outage")
	toolStep(o, "c1", `{"path":"app.log"}`, bigLog(10000))
	toolStep(o, "c2", `{"path":"b"}`, "small b")
	toolStep(o, "c3", `{"path":"c"}`, "small c")
	toolStep(o, "c4", `{"path":"d"}`, "small d")

	o.Compact(context.Background())

	if len(o.steps) != 3 {
		t.Fatalf("scratchpad has %d steps, want 3", len(o.steps))
	}
	if o.steps[0][0].ToolCalls[0].ID != "c2" {
		t.Errorf("oldest kept step should be c2, got %s", o.steps[0][0].ToolCalls[0].ID)
	}
	// Oldest steps were sent, in order, and nothing newer.
	sent := eng.seen[0]
	if !strings.Contains(sent, "debug outage") || !strings.Contains(sent, "art-1") || strings.Contains(sent, "small b") {
		t.Errorf("summarizer input wrong:\n%s", sent)
	}
	if strings.Index(sent, "debug outage") > strings.Index(sent, "art-1") {
		t.Error("steps not sent oldest first")
	}
	if len(o.longTerm) != 1 {
		t.Fatalf("want 1 long-term record, got %d", len(o.longTerm))
	}
	r := o.longTerm[0]
	if r.Findings[0] != "pg-7 refuses connections" || r.RuledOut[0] == "" || r.ArtifactIDs[0] != "art-1" {
		t.Errorf("bad record: %+v", r)
	}
	if p := o.Messages("persona")[0].Content; !strings.Contains(p, "ruled out: disk full") {
		t.Errorf("long-term layer not in prompt:\n%s", p)
	}
}

func TestCompactionFallbackKeepsPointers(t *testing.T) {
	o := NewOrchestrator(&fakeEngine{err: fmt.Errorf("model down")}, "m")
	o.Limits.KeepSteps = 1
	toolStep(o, "c1", `{"path":"app.log"}`, bigLog(3000))
	toolStep(o, "c2", `{"path":"b"}`, "b")
	o.Compact(context.Background())

	r := o.longTerm[0]
	if len(r.QueriesTried) != 1 || !strings.Contains(r.QueriesTried[0], "app.log") || r.ArtifactIDs[0] != "art-1" {
		t.Errorf("fallback lost data: %+v", r)
	}
}

func TestLongTermRecompacts(t *testing.T) {
	eng := &fakeEngine{reply: `{"findings":["merged"]}`}
	o := NewOrchestrator(eng, "m")
	o.Limits.LongTermChars = 300
	for i := 0; i < 6; i++ {
		o.longTerm = append(o.longTerm, Record{Findings: []string{strings.Repeat("f", 80)}, ArtifactIDs: []string{fmt.Sprintf("art-%d", i+1)}})
	}
	o.Compact(context.Background())

	if len(renderRecords(o.longTerm)) > 300 {
		t.Fatalf("long-term still %d chars", len(renderRecords(o.longTerm)))
	}
	if o.longTerm[0].Findings[0] != "merged" || len(o.longTerm[0].ArtifactIDs) < 3 {
		t.Errorf("merged record lost artifact pointers: %+v", o.longTerm[0])
	}
}
