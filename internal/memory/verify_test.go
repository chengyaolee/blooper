package memory

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

var testAgents = []string{"a", "b", "c"}

// randomOp throws an arbitrary, often illegal, action at the store. The store must reject
// every illegal one; CheckTrace then proves nothing illegal slipped into the log.
func randomOp(store *Store, goal string, rng *rand.Rand) {
	tasks, _ := store.Tasks(goal)
	id := fmt.Sprintf("1.%d", rng.IntN(4)) // sometimes a task that doesn't exist
	if len(tasks) > 0 && rng.IntN(5) > 0 {
		id = tasks[rng.IntN(len(tasks))].ID
	}
	who := act(testAgents[rng.IntN(len(testAgents))])
	switch rng.IntN(8) {
	case 0:
		by := who
		if rng.IntN(2) == 0 {
			by = boss
		}
		var subs []SubtaskSpec
		for i := 0; i <= rng.IntN(3); i++ {
			sub := SubtaskSpec{Title: "t"}
			if rng.IntN(3) == 0 {
				sub.Assignee = testAgents[rng.IntN(len(testAgents))]
			}
			subs = append(subs, sub)
		}
		_, _ = store.Decompose(goal, id, by, subs)
	case 1, 2:
		_ = store.Claim(goal, id, who)
	case 3:
		_ = store.Complete(goal, id, who, "r")
	case 4:
		_ = store.Verify(goal, id, boss, rng.IntN(2) == 0, "f")
	case 5:
		_ = store.Verify(goal, id, System, true, "auto")
	case 6:
		_, _ = store.ReleaseClaims(goal, who)
	case 7:
		_ = store.RecordChecks(goal, id, []CheckResult{{Check: Check{Kind: "json"}, Passed: rng.IntN(2) == 0}})
	}
}

func assertSafe(t *testing.T, store *Store, goal, label string) {
	t.Helper()
	rep, err := store.VerifyTrace(goal)
	if err != nil {
		t.Fatalf("%s: verify: %v", label, err)
	}
	if len(rep.Safety) > 0 || len(rep.Projection) > 0 {
		b, _ := json.MarshalIndent(rep, "", "  ")
		t.Fatalf("%s: protocol violated:\n%s", label, b)
	}
}

// Sequential random interleavings: a bounded, randomized model check of the store against
// the protocol.
func TestProtocolHoldsUnderRandomInterleavings(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "fuzz.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	for seed := uint64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewPCG(seed, 7))
		goal, _ := store.CreateGoal("g", boss, testAgents, nil)
		for i := 0; i < 40; i++ {
			randomOp(store, goal, rng)
		}
		if _, err := store.FinishGoal(goal, ""); err != nil {
			t.Fatalf("finish: %v", err)
		}
		assertSafe(t, store, goal, fmt.Sprintf("seed %d", seed))
	}
}

// Concurrent agents racing on one goal: atomic claims and transactions must keep the log legal.
func TestProtocolHoldsUnderConcurrency(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "race.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	for round := uint64(0); round < 10; round++ {
		goal, _ := store.CreateGoal("g", boss, testAgents, nil)
		_, _ = store.Decompose(goal, RootTaskID, boss, []SubtaskSpec{{Title: "x"}, {Title: "y"}, {Title: "z"}})
		var wg sync.WaitGroup
		for g := uint64(0); g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(round, g))
				for i := 0; i < 50; i++ {
					randomOp(store, goal, rng)
				}
			}()
		}
		wg.Wait()
		assertSafe(t, store, goal, fmt.Sprintf("round %d", round))
	}
}

// Each tampering must be caught by the rule it breaks.
func TestCheckTraceCatchesTampering(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "tamper.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	goal, _ := store.CreateGoal("g", boss, nil, nil)
	_, _ = store.Decompose(goal, RootTaskID, boss, []SubtaskSpec{{Title: "a"}, {Title: "b"}})
	_ = store.Claim(goal, "1.1", act("w"))
	_ = store.Complete(goal, "1.1", act("w"), "ok")
	_ = store.Verify(goal, "1.1", boss, true, "")
	_ = store.Claim(goal, "1.2", act("w"))
	_ = store.Complete(goal, "1.2", act("w"), "ok")
	_ = store.Verify(goal, "1.2", boss, true, "")
	_, _ = store.FinishGoal(goal, "done")

	events, _ := store.Events(goal)
	tasks, _ := store.Tasks(goal)
	if rep := CheckTrace(events, tasks); !rep.OK {
		t.Fatalf("clean trace should verify: %+v", rep)
	}

	find := func(kind, dst string) int {
		return slices.IndexFunc(events, func(e Event) bool { return e.Kind == kind && e.Dst == dst })
	}
	rules := func(rep *TraceReport) []string {
		var out []string
		for _, v := range append(append(rep.Safety, rep.Liveness...), rep.Projection...) {
			out = append(out, v.Rule)
		}
		return out
	}

	cases := []struct {
		name   string
		mutate func(ev []Event, ts []Task) ([]Event, []Task)
		rule   string
	}{
		{"verified by a worker", func(ev []Event, ts []Task) ([]Event, []Task) {
			ev[find("verified", "1.1")].Src = "w"
			return ev, ts
		}, "verify_authority"},
		{"completed by someone else", func(ev []Event, ts []Task) ([]Event, []Task) {
			ev[find("completed", "1.2")].Src = "intruder"
			return ev, ts
		}, "complete_precondition"},
		{"reward on a claim", func(ev []Event, ts []Task) ([]Event, []Task) {
			r := 5.0
			ev[find("claimed", "1.1")].Reward = &r
			return ev, ts
		}, "reward_placement"},
		{"inflated episode reward", func(ev []Event, ts []Task) ([]Event, []Task) {
			r := 2.0
			ev[len(ev)-1].Reward = &r
			return ev, ts
		}, "episode_reward"},
		{"forged action space", func(ev []Event, ts []Task) ([]Event, []Task) {
			ev[find("claimed", "1.1")].Payload = json.RawMessage(`{"options":["1.1"]}`)
			return ev, ts
		}, "claim_options"},
		{"dropped roll-up", func(ev []Event, ts []Task) ([]Event, []Task) {
			i := find("rolled_up", RootTaskID)
			return append(ev[:i:i], ev[i+1:]...), ts
		}, "missing_rollup"},
		{"table edited behind the log", func(ev []Event, ts []Task) ([]Event, []Task) {
			ts[1].Status = StatusOpen
			return ev, ts
		}, "projection"},
		{"time ran backwards", func(ev []Event, ts []Task) ([]Event, []Task) {
			ev[3].TS = ev[2].TS
			return ev, ts
		}, "time_monotonic"},
	}
	for _, c := range cases {
		ev, ts := c.mutate(slices.Clone(events), slices.Clone(tasks))
		if got := rules(CheckTrace(ev, ts)); !slices.Contains(got, c.rule) {
			t.Errorf("%s: want rule %s, got %v", c.name, c.rule, got)
		}
	}
}

func TestCheckTraceLiveness(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	goal, _ := store.CreateGoal("g", boss, nil, nil)
	_, _ = store.Decompose(goal, RootTaskID, boss, []SubtaskSpec{{Title: "a"}, {Title: "b"}})
	_ = store.Claim(goal, "1.1", act("w"))
	_, _ = store.FinishGoal(goal, "gave up")

	rep, _ := store.VerifyTrace(goal)
	if rep.OK || len(rep.Liveness) != 2 || len(rep.Safety) != 0 {
		t.Fatalf("want exactly 2 liveness violations (1.1 claimed, 1.2 open), got %+v", rep)
	}
}
