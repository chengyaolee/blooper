package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBrowserDetection(t *testing.T){
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