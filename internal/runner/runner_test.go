package runner

import "testing"

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
