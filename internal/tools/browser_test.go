package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBrowserDetection(t *testing.T){
	if _, err := scraplingBin(); err != nil {
		t.Skip(err)
	}

	// 1. Create a browser toll instance in headless mode
	tool := NewBrowserTool(true)	
	defer tool.Close() // closes the tool after completing

	// 2. Prepare the JSON request asking for the borwser to screenshot Sannysoft
	payload, err := json.Marshal(map[string]string{
		"action": "screenshot",
		"url":    "https://bot.sannysoft.com",
	})
	if err != nil {
		t.Fatalf("Failed to create JSON payload: %v", err)
	}

	// 3. Set a timeout for network load and rendering (45s to be generous)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Log("Launching browser, navigating to https://bot.sannysoft.com...")

	// 4. Execute the request and get the response
	response, err := tool.Execute(ctx, payload)
	if err != nil {
		t.Fatalf("Failed to execute request: %v", err)
	}
	if response.Error != "" {
		t.Fatalf("Browser tool error: %s", response.Error)
	}

	// 5. Output resulting message (with screenshot file path)
	t.Logf("Success! %s", response.Output)
}

// TestBrowserRecovers kills scrapling-mcp mid-session; the next call must start a fresh one.
func TestBrowserRecovers(t *testing.T) {
	if _, err := scraplingBin(); err != nil {
		t.Skip(err)
	}
	if _, err := exec.LookPath("pkill"); err != nil {
		t.Skip("pkill not available")
	}
	tool := NewBrowserTool(true)
	defer tool.Close()

	nav := func() *ToolResult {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		res, err := tool.Execute(ctx, []byte(`{"action":"navigate","url":"https://example.com"}`))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := nav(); res.Error != "" || !strings.Contains(res.Output, "Example Domain") {
		t.Fatalf("first navigate failed: err=%q out=%q", res.Error, res.Output)
	}

	// -P: only children of this test process, never someone else's scrapling-mcp.
	if err := exec.Command("pkill", "-9", "-P", strconv.Itoa(os.Getpid()), "-f", "scrapling-mcp").Run(); err != nil {
		t.Fatalf("could not kill scrapling-mcp child: %v", err)
	}

	nav() // may fail: this is the call that discovers the dead process
	if res := nav(); res.Error != "" || !strings.Contains(res.Output, "Example Domain") {
		t.Fatalf("did not recover after kill: err=%q out=%q", res.Error, res.Output)
	}
}
