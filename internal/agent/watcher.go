package agent

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors agent directories for changes and hot-reloads YAML configurations.
type Watcher struct {
	registry *AgentRegistry
	dirs     []string
	watcher  *fsnotify.Watcher
	mu       sync.Mutex
}

func NewWatcher(registry *AgentRegistry, dirs []string) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	return &Watcher{
		registry: registry,
		dirs:     dirs,
		watcher:  w,
	}, nil
}

// Start begins watching the directories in the background.
func (w *Watcher) Start(ctx context.Context) error {
	for _, dir := range w.dirs {
		if err := w.watcher.Add(dir); err != nil {
			log.Printf("⚠️  Watcher warning for dir %s: %v", dir, err)
		}
	}

	go func() {
		defer w.watcher.Close()

		var debounceTimer *time.Timer
		debounceDuration := 200 * time.Millisecond

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-w.watcher.Events:
				if !ok {
					return
				}
				// Only reload on YAML changes
				if !strings.HasSuffix(event.Name, ".yaml") && !strings.HasSuffix(event.Name, ".yml") {
					continue
				}

				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Remove) {
					w.mu.Lock()
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					debounceTimer = time.AfterFunc(debounceDuration, func() {
						dir := filepath.Dir(event.Name)
						if err := w.registry.LoadDir(dir); err == nil {
							log.Printf("🔄 [Hot-Reload] Reloaded agent configurations from %s", dir)
						}
					})
					w.mu.Unlock()
				}

			case err, ok := <-w.watcher.Errors:
				if !ok {
					return
				}
				log.Printf("⚠️  Watcher error: %v", err)
			}
		}
	}()

	return nil
}
