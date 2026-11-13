package tunnel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Trace is a JSONL connection-event log. It is deliberately NOT FCP's FEC
// controller trace: kcp-go has no controller to trace. A nil *Trace is a
// no-op, which keeps the call sites free of "if trace != nil" noise.
type Trace struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

func NewTrace(prefix, role string) (*Trace, error) {
	if prefix == "" {
		return nil, nil
	}
	path := fmt.Sprintf("%s-%s-%d.jsonl", prefix, role, os.Getpid())
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("trace dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("trace file: %w", err)
	}
	return &Trace{f: f, path: path}, nil
}

func (t *Trace) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}

func (t *Trace) Event(kind string, fields map[string]any) {
	if t == nil {
		return
	}
	rec := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "kind": kind}
	for k, v := range fields {
		rec[k] = v
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.f.Write(append(line, '\n'))
}

func (t *Trace) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.f.Close()
}
