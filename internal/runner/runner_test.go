package runner

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chengyaolee/blooper/internal/remote"
)

func TestComputerInputRequiresObservation(t *testing.T) {
	r := &Runner{}
	status, output, img := r.useComputer(context.Background(), "job", json.RawMessage(`{"action":"click","x":0,"y":0}`))
	if status != remote.ActionFailed || output != "take a screenshot in this job before sending computer input" || img != nil {
		t.Fatalf("input before screenshot: %s %s", status, output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, _, _ = r.useComputer(ctx, "job", json.RawMessage(`{"action":"screenshot"}`))
	if status != remote.ActionCancelled {
		t.Fatal("cancelled computer action was allowed")
	}
}

func TestCheckCoordinatorURL(t *testing.T) {
	ok := map[string]bool{
		"https://studio.tailnet.ts.net:8080": true,
		"http://127.0.0.1:8080":              true,
		"http://localhost:8080":              true,
		"http://192.168.1.20:8080":           false,
		"http://studio.local:8080":           false,
		"ftp://studio:8080":                  false,
		"not a url":                          false,
	}
	for raw, want := range ok {
		if got := checkCoordinatorURL(raw, false) == nil; got != want {
			t.Errorf("%s: allowed=%v, want %v", raw, got, want)
		}
	}
	if err := checkCoordinatorURL("http://192.168.1.20:8080", true); err != nil {
		t.Errorf("allow_insecure should permit plain HTTP: %v", err)
	}
}
