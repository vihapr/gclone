package main

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var progressPercentRe = regexp.MustCompile(`(\d{1,3})%`)

// heartbeatInterval — период пульса при клонировании (переопределяется в тестах).
var heartbeatInterval = 5 * time.Second

// parseProgressLine вытаскивает процент из строки прогресса git, например
// "Receiving objects:  45% (123/273), 1.20 MiB | 2.00 MiB/s".
func parseProgressLine(line string) (percent int, ok bool) {
	m := progressPercentRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	fmt.Sscanf(m[1], "%d", &percent)
	if percent < 0 || percent > 100 {
		return 0, false
	}
	return percent, true
}

// progressTracker помнит последний процент каждого репозитория и кто завершён.
type progressTracker struct {
	mu    sync.Mutex
	total int
	done  map[string]bool
	pct   map[string]int
}

func newProgressTracker(total int) *progressTracker {
	return &progressTracker{
		total: total,
		done:  make(map[string]bool),
		pct:   make(map[string]int),
	}
}

func (t *progressTracker) Update(name string, percent int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pct[name] = percent
}

func (t *progressTracker) Finish(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done[name] = true
	delete(t.pct, name)
}

// render возвращает строку пульса; пустая строка — если в работе никого нет.
func (t *progressTracker) render(elapsed time.Duration) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	inFlight := t.total - len(t.done)
	if inFlight <= 0 {
		return ""
	}
	if len(t.pct) == 0 {
		return fmt.Sprintf("⏳ %d/%d готово (%ds)", len(t.done), t.total, int(elapsed.Seconds()))
	}
	names := make([]string, 0, len(t.pct))
	for name := range t.pct {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s %d%%", name, t.pct[name])
	}
	return fmt.Sprintf("⏳ %d/%d готово | %s (%ds)",
		len(t.done), t.total, strings.Join(parts, " · "), int(elapsed.Seconds()))
}

// lineSplitter — io.Writer, режущий поток на строки по \n и \r
// (прогресс git обновляет строку через \r) и передающий их в onLine.
type lineSplitter struct {
	onLine func(string)
	buf    []byte
}

func newLineSplitter(onLine func(string)) *lineSplitter {
	return &lineSplitter{onLine: onLine}
}

func (s *lineSplitter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		idx := strings.IndexAny(string(s.buf), "\r\n")
		if idx < 0 {
			break
		}
		line := string(s.buf[:idx])
		s.buf = s.buf[idx+1:]
		s.emit(line)
	}
	return len(p), nil
}

// Flush отдаёт накопленный хвост без терминатора (конец стрима).
func (s *lineSplitter) Flush() {
	if len(s.buf) > 0 {
		line := string(s.buf)
		s.buf = nil
		s.emit(line)
	}
}

func (s *lineSplitter) emit(line string) {
	line = strings.TrimRight(line, " ")
	if line != "" {
		s.onLine(line)
	}
}

// startHeartbeat печатает пульс каждые interval, пока не закрыт stop.
func startHeartbeat(stdout io.Writer, tracker *progressTracker, startedAt time.Time, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if s := tracker.render(time.Since(startedAt)); s != "" {
				fmt.Fprintln(stdout, s)
			}
		case <-stop:
			return
		}
	}
}
