package memory

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// Trace tables record every agent run as a trajectory for later retraining / RL:
//
//	runs   - one agent invocation (chat turn, plan, work, verify, synthesize, delegate),
//	         nested via parent_id so a chat turn that spawns a goal owns the whole tree.
//	steps  - every LLM call (exact request the model saw + response + tokens + latency),
//	         every tool call (args, result, error, latency) and every human approval decision.
//	blobs  - content-addressed store for large repeated values (agent configs, tool schemas),
//	         referenced by hash so each step stays small.
const traceSchema = `
	CREATE TABLE IF NOT EXISTS runs (
		id TEXT PRIMARY KEY,
		parent_id TEXT NOT NULL DEFAULT '',
		goal_id TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL,
		role TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		config_hash TEXT NOT NULL DEFAULT '',
		input TEXT NOT NULL DEFAULT '',
		output TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		ended_at INTEGER
	);
	CREATE INDEX IF NOT EXISTS runs_parent ON runs(parent_id);
	CREATE INDEX IF NOT EXISTS runs_goal ON runs(goal_id);
	CREATE TABLE IF NOT EXISTS steps (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		name TEXT NOT NULL,
		input TEXT NOT NULL DEFAULT '{}',
		output TEXT NOT NULL DEFAULT '{}',
		error TEXT NOT NULL DEFAULT '',
		latency_ms INTEGER NOT NULL DEFAULT 0,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		ts INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS steps_run ON steps(run_id, seq);
	CREATE TABLE IF NOT EXISTS blobs (
		hash TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE VIEW IF NOT EXISTS v_tool_stats AS
		SELECT name AS tool, COUNT(*) AS calls, SUM(error != '') AS errors,
			ROUND(AVG(latency_ms)) AS avg_latency_ms
		FROM steps WHERE kind = 'tool' GROUP BY name;
	CREATE VIEW IF NOT EXISTS v_llm_usage AS
		SELECT r.agent, r.role, s.name AS model, COUNT(*) AS calls, SUM(s.error != '') AS errors,
			SUM(s.prompt_tokens) AS prompt_tokens, SUM(s.completion_tokens) AS completion_tokens,
			ROUND(AVG(s.latency_ms)) AS avg_latency_ms
		FROM steps s JOIN runs r ON r.id = s.run_id WHERE s.kind = 'llm' GROUP BY r.agent, r.role, s.name;
	DROP VIEW IF EXISTS v_worker_outcomes;
	CREATE VIEW v_worker_outcomes AS
		SELECT json_extract(payload, '$.worker') AS worker,
			SUM(reward > 0) AS approved, SUM(reward < 0) AS rejected,
			SUM(reward > 0 AND json_extract(payload, '$.system_verdict')) AS auto_approved,
			SUM(reward < 0 AND json_extract(payload, '$.system_verdict')) AS failed_checks, COUNT(*) AS verdicts
		FROM task_events WHERE kind = 'verified' GROUP BY worker;
	CREATE VIEW IF NOT EXISTS v_goal_outcomes AS
		SELECT g.id, g.goal, g.controller, g.status, g.score,
			json_extract(g.verification, '$.ok') AS trace_ok,
			(g.ended_at - g.started_at) / 1000 AS duration_ms,
			(SELECT COUNT(*) FROM tasks t WHERE t.goal_id = g.id) AS tasks,
			(SELECT COUNT(*) FROM runs r WHERE r.goal_id = g.id) AS runs
		FROM goals g;
`

// migrate upgrades databases created before a column existed.
func migrate(db *sql.DB) error {
	for _, c := range []struct{ table, column, def string }{
		{"task_events", "run_id", "TEXT NOT NULL DEFAULT ''"},
		{"tasks", "checks", "TEXT NOT NULL DEFAULT '[]'"},
		{"tasks", "verified_by", "TEXT NOT NULL DEFAULT ''"},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, c.table, c.column, c.def)); err != nil {
				return fmt.Errorf("failed to add %s.%s: %w", c.table, c.column, err)
			}
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS task_events_run ON task_events(run_id)`)
	return err
}

// RunInfo describes an agent run when it starts.
type RunInfo struct {
	ID         string
	ParentID   string
	GoalID     string
	TaskID     string
	Agent      string
	Role       string // chat | plan | work | verify | synthesize | delegate
	Model      string
	ConfigHash string
	Input      string
}

// PutBlob stores a JSON-encodable value once and returns its content hash.
func (s *Store) PutBlob(kind string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	_, err = s.db.Exec(`INSERT OR IGNORE INTO blobs (hash, kind, content, created_at) VALUES (?, ?, ?, ?)`, hash, kind, string(b), now())
	return hash, err
}

// StartRun records the start of an agent run.
func (s *Store) StartRun(r RunInfo) error {
	_, err := s.db.Exec(`INSERT INTO runs (id, parent_id, goal_id, task_id, agent, role, model, config_hash, input, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?)`,
		r.ID, r.ParentID, r.GoalID, r.TaskID, r.Agent, r.Role, r.Model, r.ConfigHash, r.Input, now())
	return err
}

// EndRun records how a run finished.
func (s *Store) EndRun(runID, output string, runErr error) error {
	status, errText := "ok", ""
	if runErr != nil {
		status, errText = "error", runErr.Error()
	}
	_, err := s.db.Exec(`UPDATE runs SET output = ?, error = ?, status = ?, ended_at = ? WHERE id = ?`,
		output, errText, status, now(), runID)
	return err
}

// Step is one recorded action inside a run.
type Step struct {
	RunID            string `json:"-"`
	Seq              int    `json:"seq"`
	Kind             string `json:"kind"` // llm | tool | human_approval
	Name             string `json:"name"` // model or tool name
	Input            any    `json:"input"`
	Output           any    `json:"output"`
	Error            string `json:"error,omitempty"`
	LatencyMS        int64  `json:"latency_ms"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	TS               int64  `json:"ts"`
}

// LogStep appends a step to a run. seq is assigned atomically per run.
func (s *Store) LogStep(st Step) error {
	in, err := json.Marshal(st.Input)
	if err != nil {
		return err
	}
	out, err := json.Marshal(st.Output)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO steps (run_id, seq, kind, name, input, output, error, latency_ms, prompt_tokens, completion_tokens, ts)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM steps WHERE run_id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.RunID, st.RunID, st.Kind, st.Name, string(in), string(out), st.Error, st.LatencyMS, st.PromptTokens, st.CompletionTokens, now())
	return err
}

// SaveVerification stores the trace verification report on the goal.
func (s *Store) SaveVerification(goalID string, report *TraceReport) error {
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE goals SET verification = ? WHERE id = ?`, string(b), goalID)
	return err
}

// ExportJSONL writes one JSON line per root run (a chat turn, or a goal run without a parent)
// containing every descendant run with its steps, every goal those runs touched with its tasks
// and CTDG events, and the content-addressed blobs they reference. Each line is a
// self-contained episode for offline training.
func (s *Store) ExportJSONL(w io.Writer) (int, error) {
	roots, err := s.strings(`SELECT id FROM runs WHERE parent_id = '' ORDER BY started_at`)
	if err != nil {
		return 0, err
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, root := range roots {
		ep, err := s.episode(root)
		if err != nil {
			return 0, fmt.Errorf("export run %s: %w", root, err)
		}
		if err := enc.Encode(ep); err != nil {
			return 0, err
		}
	}
	return len(roots), bw.Flush()
}

func (s *Store) strings(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// rowsJSON runs a query and returns each row as a column->value map, decoding JSON text columns.
func (s *Store) rowsJSON(jsonCols map[string]bool, query string, args ...any) ([]map[string]any, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if str, ok := v.(string); ok && jsonCols[c] && str != "" {
				v = json.RawMessage(str)
			}
			m[c] = v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) episode(rootID string) (map[string]any, error) {
	runs, err := s.rowsJSON(nil, `WITH RECURSIVE tree(id) AS (
			SELECT ? UNION ALL SELECT r.id FROM runs r JOIN tree t ON r.parent_id = t.id)
		SELECT r.* FROM runs r JOIN tree t ON r.id = t.id ORDER BY r.started_at`, rootID)
	if err != nil {
		return nil, err
	}
	goalSet, hashSet := map[string]bool{}, map[string]bool{}
	for _, r := range runs {
		steps, err := s.rowsJSON(map[string]bool{"input": true, "output": true},
			`SELECT seq, kind, name, input, output, error, latency_ms, prompt_tokens, completion_tokens, ts FROM steps WHERE run_id = ? ORDER BY seq`, r["id"])
		if err != nil {
			return nil, err
		}
		r["steps"] = steps
		if g, _ := r["goal_id"].(string); g != "" {
			goalSet[g] = true
		}
		if h, _ := r["config_hash"].(string); h != "" {
			hashSet[h] = true
		}
		for _, st := range steps {
			if in, ok := st["input"].(json.RawMessage); ok {
				var probe struct {
					ToolsHash string `json:"tools_hash"`
				}
				if json.Unmarshal(in, &probe) == nil && probe.ToolsHash != "" {
					hashSet[probe.ToolsHash] = true
				}
			}
		}
	}

	var goals []map[string]any
	for g := range goalSet {
		rows, err := s.rowsJSON(map[string]bool{"workers": true, "settings": true, "verification": true}, `SELECT * FROM goals WHERE id = ?`, g)
		if err != nil {
			return nil, err
		}
		for _, goal := range rows {
			if goal["tasks"], err = s.rowsJSON(map[string]bool{"checks": true}, `SELECT * FROM tasks WHERE goal_id = ? ORDER BY rowid`, g); err != nil {
				return nil, err
			}
			if goal["events"], err = s.rowsJSON(map[string]bool{"payload": true}, `SELECT * FROM task_events WHERE goal_id = ? ORDER BY id`, g); err != nil {
				return nil, err
			}
			goals = append(goals, goal)
		}
	}

	blobs := map[string]json.RawMessage{}
	for h := range hashSet {
		var content string
		if err := s.db.QueryRow(`SELECT content FROM blobs WHERE hash = ?`, h).Scan(&content); err == nil {
			blobs[h] = json.RawMessage(content)
		}
	}
	return map[string]any{"root_run": rootID, "runs": runs, "goals": goals, "blobs": blobs}, nil
}
