package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseProgressLine(t *testing.T) {
	cases := []struct {
		line string
		pct  int
		ok   bool
	}{
		{"remote: Enumerating objects: 12, done.", 0, false},
		{"Receiving objects:  45% (123/273), 1.20 MiB | 2.00 MiB/s", 45, true},
		{"Resolving deltas:  100% (500/500), done.", 100, true},
		{"Updating files:  10% (1/10)", 10, true},
		{"error: rpc failed", 0, false},
	}
	for _, tc := range cases {
		pct, ok := parseProgressLine(tc.line)
		if ok != tc.ok || pct != tc.pct {
			t.Errorf("parseProgressLine(%q) = (%d, %v), хочу (%d, %v)", tc.line, pct, ok, tc.pct, tc.ok)
		}
	}
}

func TestLineSplitter(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	w := newLineSplitter(func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	})

	w.Write([]byte("Counting objects: 100% (5/5), d"))
	w.Write([]byte("one.\rReceiving objects:  20% (1/5)\rReceiving objects:  40% (2/5)\n"))
	w.Write([]byte("Resolving deltas: 100%")) // хвост без терминатора
	w.Flush()                                 // отдаётся при закрытии стрима

	want := []string{
		"Counting objects: 100% (5/5), done.",
		"Receiving objects:  20% (1/5)",
		"Receiving objects:  40% (2/5)",
		"Resolving deltas: 100%",
	}
	if len(lines) != len(want) {
		t.Fatalf("строк %d (%v), хочу %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("строка[%d] = %q, хочу %q", i, lines[i], want[i])
		}
	}
}

func TestProgressTracker(t *testing.T) {
	tr := newProgressTracker(4)
	tr.Update("platform/api", 45)
	tr.Update("platform/web", 10)
	tr.Update("platform/web", 20) // последнее значение побеждает

	got := tr.render(time.Second * 34)
	want := "⏳ 0/4 готово | platform/api 45% · platform/web 20% (34s)"
	if got != want {
		t.Errorf("render = %q, хочу %q", got, want)
	}

	tr.Finish("platform/api") // завершённый клон уходит из «в работе»
	got = tr.render(time.Second * 34)
	want = "⏳ 1/4 готово | platform/web 20% (34s)"
	if got != want {
		t.Errorf("render после Finish = %q, хочу %q", got, want)
	}

	tr.Finish("platform/web")
	tr.Finish("platform/db")
	tr.Finish("platform/mobile")
	if got := tr.render(time.Second * 34); got != "" {
		t.Errorf("render при пустой работе = %q, хочу пустую строку", got)
	}
}

func TestProgressTrackerNoPercentYet(t *testing.T) {
	tr := newProgressTracker(2)
	tr.Update("platform/api", 0)
	got := tr.render(time.Second * 5)
	want := "⏳ 0/2 готово | platform/api 0% (5s)"
	if got != want {
		t.Errorf("render = %q, хочу %q", got, want)
	}
}

func TestProgressTrackerWorkWithoutPercent(t *testing.T) {
	tr := newProgressTracker(3)
	tr.Finish("platform/api")
	// Двое в работе, но ни один ещё не прислал процент — пульс всё равно тикает.
	got := tr.render(2 * time.Second)
	want := "⏳ 1/3 готово (2s)"
	if got != want {
		t.Errorf("render = %q, хочу %q", got, want)
	}
}

func TestStartHeartbeat(t *testing.T) {
	tr := newProgressTracker(2)
	tr.Update("platform/api", 50)

	var stdout strings.Builder
	stop := make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		startHeartbeat(&stdout, tr, time.Now(), 5*time.Millisecond, stop)
	}()

	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-done

	out := stdout.String()
	if !strings.Contains(out, "⏳") || !strings.Contains(out, "platform/api 50%") {
		t.Errorf("пульс не напечатан: %q", out)
	}
	if strings.Count(out, "\n") < 1 {
		t.Errorf("ожидалась хотя бы одна строка пульса: %q", out)
	}
}
