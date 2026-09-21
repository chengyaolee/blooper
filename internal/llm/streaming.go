package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// StreamSSE bridges a StreamChunk channel to an HTTP Server-Sent Events response.
func StreamSSE(w http.ResponseWriter, req *http.Request, chunks <-chan StreamChunk) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	ctx := req.Context()

	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-chunks:
			if !ok {
				return
			}
			
			if chunk.Error != nil {
				// Format error as JSON event and send
				errJSON, _ := json.Marshal(map[string]string{"error": chunk.Error.Error()})
				fmt.Fprintf(w, "data: %s\n\n", string(errJSON))
				flusher.Flush()
				return
			}

			data, err := json.Marshal(chunk)
			if err != nil {
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
			
			if chunk.Done {
				return
			}
		}
	}
}
