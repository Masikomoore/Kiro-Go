package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// claudePingInterval matches Kiro-RS-Tool. Sub2API's upstream idle window is
// longer than this, so a frame at this cadence keeps a silent generation alive.
const claudePingInterval = 25 * time.Second

// claudeSSEWriter serializes Anthropic SSE writes and emits ping frames while
// the upstream body is open but not producing client-visible events.
type claudeSSEWriter struct {
	mu         sync.Mutex
	w          http.ResponseWriter
	flusher    http.Flusher
	interval   time.Duration
	stop       chan struct{}
	stopOnce   sync.Once
	started    bool
	generation uint64
}

func newClaudeSSEWriter(w http.ResponseWriter, flusher http.Flusher, interval time.Duration) *claudeSSEWriter {
	if interval <= 0 {
		interval = claudePingInterval
	}
	return &claudeSSEWriter{
		w:        w,
		flusher:  flusher,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

func (s *claudeSSEWriter) Send(event string, data interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeLocked(event, data)
}

// Arm waits one interval, then starts the ping loop. A fast failure can still
// be retried if Disarm runs before that interval: nothing has been written yet.
// Once the loop starts, the response is committed.
func (s *claudeSSEWriter) Arm() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.generation++
	generation := s.generation
	interval := s.interval
	s.mu.Unlock()

	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		select {
		case <-s.stop:
			return
		case <-timer.C:
			s.mu.Lock()
			if s.generation != generation || s.started {
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			s.Start()
		}
	}()
}

// Disarm cancels an armed ping that has not written yet.
func (s *claudeSSEWriter) Disarm() {
	s.mu.Lock()
	if !s.started {
		s.generation++
	}
	s.mu.Unlock()
}

// Committed reports whether a ping has been written. After that, retrying the
// attempt would append a second response onto the same stream.
func (s *claudeSSEWriter) Committed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// Start emits one ping immediately, then one every interval until Stop.
// The first call commits the HTTP response.
func (s *claudeSSEWriter) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.writeLocked("ping", map[string]string{"type": "ping"})
	s.mu.Unlock()
	go s.loop()
}

func (s *claudeSSEWriter) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *claudeSSEWriter) loop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			s.writeLocked("ping", map[string]string{"type": "ping"})
			s.mu.Unlock()
		}
	}
}

func (s *claudeSSEWriter) writeLocked(event string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, jsonData)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// firstByteReader reports the first Read that returns payload bytes.
// Headers alone do not count: a failed status is handled before this wrapper
// is installed, and an empty read must not commit the downstream response.
type firstByteReader struct {
	r      io.Reader
	once   sync.Once
	notify func()
}

func (f *firstByteReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if n > 0 && f.notify != nil {
		f.once.Do(f.notify)
	}
	return n, err
}
