package remote

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveDeepgram(t *testing.T) {
	if os.Getenv("BLOOPER_LIVE_HOSTED") != "1" {
		t.Skip("set BLOOPER_LIVE_HOSTED=1 and DEEPGRAM_API_KEY")
	}
	key := os.Getenv("DEEPGRAM_API_KEY")
	if key == "" {
		t.Fatal("DEEPGRAM_API_KEY is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Deepgram's public speech sample, from its official prerecorded-audio guide.
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://dpgr.am/spacewalk.wav", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("audio sample: %s", resp.Status)
	}
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DeepgramKey: key}
	cfg.defaults()
	text, err := transcribe(ctx, cfg, "audio/wav", audio)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("Deepgram returned an empty transcript for the speech sample")
	}
	t.Logf("Deepgram %s: real audio upload returned %d characters", cfg.DeepgramModel, len(text))
}
