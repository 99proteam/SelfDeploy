package deploy

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/99proteam/selfdeploy/internal/store"
)

// logWriter buffers build output and flushes it to the deployment row twice a
// second, so the UI can tail logs without one database write per line.
// Secrets passed in redact are masked before anything is stored.
type logWriter struct {
	store  *store.Store
	id     int64
	redact *strings.Replacer

	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
	wg   sync.WaitGroup
}

func newLogWriter(s *store.Store, id int64, secrets ...string) *logWriter {
	var pairs []string
	for _, sec := range secrets {
		if len(sec) >= 4 {
			pairs = append(pairs, sec, "********")
		}
	}
	w := &logWriter{store: s, id: id, redact: strings.NewReplacer(pairs...), done: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				w.flush(false)
			case <-w.done:
				w.flush(true)
				return
			}
		}
	}()
	return w
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	w.mu.Unlock()
	return len(p), nil
}

// flush stores complete lines only (unless final), so a secret is never split
// across two flushes and missed by the redactor.
func (w *logWriter) flush(final bool) {
	w.mu.Lock()
	data := w.buf.Bytes()
	n := len(data)
	if !final {
		n = bytes.LastIndexByte(data, '\n') + 1
	}
	if n == 0 {
		w.mu.Unlock()
		return
	}
	text := string(data[:n])
	w.buf.Next(n)
	w.mu.Unlock()
	w.store.AppendDeploymentLog(w.id, w.redact.Replace(text))
}

func (w *logWriter) Close() {
	close(w.done)
	w.wg.Wait()
}
