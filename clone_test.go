package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testProjects() []Project {
	return []Project{
		{PathWithNamespace: "platform/api", HTTPURLToRepo: "https://gitlab.example.com/platform/api.git", SSHURLToRepo: "git@gitlab.example.com:platform/api.git"},
		{PathWithNamespace: "platform/backend/web", HTTPURLToRepo: "https://gitlab.example.com/platform/backend/web.git", SSHURLToRepo: "git@gitlab.example.com:platform/backend/web.git"},
		{PathWithNamespace: "platform/mobile", HTTPURLToRepo: "https://gitlab.example.com/platform/mobile.git", SSHURLToRepo: "git@gitlab.example.com:platform/mobile.git"},
	}
}

func resultsByPath(results []CloneResult) map[string]CloneResult {
	m := make(map[string]CloneResult, len(results))
	for _, r := range results {
		m[r.Project.PathWithNamespace] = r
	}
	return m
}

func TestCloneAllClones(t *testing.T) {
	out := t.TempDir()
	var mu sync.Mutex
	calls := map[string][]string{}
	run := func(ctx context.Context, args ...string) error {
		mu.Lock()
		calls[args[2]] = args // args = [clone, url, dest]
		mu.Unlock()
		return nil
	}

	results := cloneAll(context.Background(), testProjects(), CloneOptions{Out: out, Parallel: 1}, run)

	if len(results) != 3 {
		t.Fatalf("результатов %d, хочу 3", len(results))
	}
	for path, r := range resultsByPath(results) {
		if r.Status != "cloned" {
			t.Errorf("%s: status = %q, хочу cloned", path, r.Status)
		}
	}
	for _, p := range testProjects() {
		wantDest := filepath.Join(out, p.PathWithNamespace)
		args, ok := calls[wantDest]
		if !ok {
			t.Fatalf("git не вызван для dest %q", wantDest)
		}
		wantArgs := []string{"clone", p.HTTPURLToRepo, wantDest}
		if len(args) != 3 || args[0] != wantArgs[0] || args[1] != wantArgs[1] || args[2] != wantArgs[2] {
			t.Errorf("args = %v, хочу %v", args, wantArgs)
		}
		if info, err := os.Stat(wantDest); err != nil || !info.IsDir() {
			t.Errorf("каталог %q не создан: %v", wantDest, err)
		}
	}
}

func TestCloneAllClonesIntoExistingEmptyDir(t *testing.T) {
	out := t.TempDir()
	existing := filepath.Join(out, "platform/api")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, args ...string) error { return nil }

	results := cloneAll(context.Background(), testProjects()[:1], CloneOptions{Out: out, Parallel: 1}, run)

	if results[0].Status != "cloned" {
		t.Errorf("status = %q, хочу cloned (пустой каталог не повод для skip)", results[0].Status)
	}
}

func TestCloneAllSSHAndDepth(t *testing.T) {
	out := t.TempDir()
	var gotArgs []string
	run := func(ctx context.Context, args ...string) error {
		gotArgs = args
		return nil
	}

	cloneAll(context.Background(), testProjects()[:1], CloneOptions{Out: out, UseSSH: true, Depth: 2, Parallel: 1}, run)

	want := []string{"clone", "--depth", "2", testProjects()[0].SSHURLToRepo, filepath.Join(out, "platform/api")}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, хочу %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("args[%d] = %q, хочу %q", i, gotArgs[i], want[i])
		}
	}
}

func TestCloneAllFailureContinues(t *testing.T) {
	out := t.TempDir()
	run := func(ctx context.Context, args ...string) error {
		if args[1] == testProjects()[1].HTTPURLToRepo {
			return errors.New("boom")
		}
		return nil
	}

	results := cloneAll(context.Background(), testProjects(), CloneOptions{Out: out, Parallel: 1}, run)

	byPath := resultsByPath(results)
	if byPath["platform/backend/web"].Status != "failed" || byPath["platform/backend/web"].Err == nil {
		t.Errorf("ожидался failed с Err для platform/backend/web, получено %+v", byPath["platform/backend/web"])
	}
	if byPath["platform/api"].Status != "cloned" || byPath["platform/mobile"].Status != "cloned" {
		t.Errorf("остальные проекты должны клонироваться: %+v", byPath)
	}
}

func TestCloneAllInterrupt(t *testing.T) {
	out := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	run := func(ctx context.Context, args ...string) error {
		once.Do(func() { close(firstStarted) })
		<-release // имитируем долгий clone: ждём отмены
		return nil
	}

	resultsCh := make(chan []CloneResult, 1)
	go func() {
		resultsCh <- cloneAll(ctx, testProjects(), CloneOptions{Out: out, Parallel: 1}, run)
	}()

	<-firstStarted // первый клон реально запущен
	cancel()       // Ctrl+C
	close(release) // запущенный клон завершается сам

	results := <-resultsCh
	byPath := resultsByPath(results)
	cloned, interrupted, failed := 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case "cloned":
			cloned++
		case "interrupted":
			interrupted++
		case "failed":
			failed++
		}
	}
	if cloned != 1 || interrupted != 2 || failed != 0 {
		t.Errorf("получено cloned=%d interrupted=%d failed=%d, хочу 1/2/0 (результаты: %+v)", cloned, interrupted, failed, byPath)
	}
}

func TestCloneAllSkipsOnlyValidClones(t *testing.T) {
	out := t.TempDir()
	// Валидный «клон»: каталог с .git, rev-parse проходит (fake runner).
	dest := filepath.Join(out, "platform/api")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var cloneAttempted bool
	run := func(ctx context.Context, args ...string) error {
		if args[0] == "clone" {
			cloneAttempted = true
		}
		return nil // rev-parse «успешен»
	}

	results := cloneAll(context.Background(), testProjects()[:1], CloneOptions{Out: out, Parallel: 1}, run)

	if results[0].Status != "skipped" {
		t.Errorf("status = %q, хочу skipped для валидного клона", results[0].Status)
	}
	if cloneAttempted {
		t.Error("clone не должен запускаться для валидного существующего клона")
	}
}

func TestCloneAllFailsOnBrokenPartialClone(t *testing.T) {
	out := t.TempDir()
	// Убитый посреди дела clone: .git есть, но HEAD не верифицируется.
	dest := filepath.Join(out, "platform/api")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var cloneAttempted bool
	run := func(ctx context.Context, args ...string) error {
		if args[0] == "clone" {
			cloneAttempted = true
		}
		return errors.New("fatal: not a git repository") // rev-parse падает
	}

	results := cloneAll(context.Background(), testProjects()[:1], CloneOptions{Out: out, Parallel: 1}, run)

	if results[0].Status != "failed" {
		t.Fatalf("status = %q, хочу failed для битого частичного клона", results[0].Status)
	}
	if cloneAttempted {
		t.Error("clone не должен запускаться в непустой каталог с битым .git")
	}
	if results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "завершённым клоном") {
		t.Errorf("Err = %v, хочу подсказку про незавершённый клон", results[0].Err)
	}
}

func TestCloneAllFailsOnNonRepoNonEmptyDir(t *testing.T) {
	out := t.TempDir()
	dest := filepath.Join(out, "platform/api")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cloneAttempted bool
	run := func(ctx context.Context, args ...string) error {
		if args[0] == "clone" {
			cloneAttempted = true
		}
		return nil
	}

	results := cloneAll(context.Background(), testProjects()[:1], CloneOptions{Out: out, Parallel: 1}, run)

	if results[0].Status != "failed" {
		t.Fatalf("status = %q, хочу failed: непустой каталог без .git не является клоном", results[0].Status)
	}
	if cloneAttempted {
		t.Error("clone в непустой каталог без .git запускаться не должен")
	}
	if results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "завершённым клоном") {
		t.Errorf("Err = %v, хочу подсказку про незавершённый клон/мешающий каталог", results[0].Err)
	}
}

func TestCloneAllOnResultCallback(t *testing.T) {
	out := t.TempDir()
	var mu sync.Mutex
	var got []CloneResult
	run := func(ctx context.Context, args ...string) error { return nil }

	opts := CloneOptions{Out: out, Parallel: 2, OnResult: func(r CloneResult) {
		mu.Lock()
		got = append(got, r)
		mu.Unlock()
	}}
	results := cloneAll(context.Background(), testProjects(), opts, run)

	if len(got) != len(testProjects()) {
		t.Fatalf("callback получил %d результатов, хочу %d", len(got), len(testProjects()))
	}
	gotByPath := resultsByPath(got)
	for _, r := range results {
		cb, ok := gotByPath[r.Project.PathWithNamespace]
		if !ok {
			t.Fatalf("callback не вызван для %s", r.Project.PathWithNamespace)
		}
		if cb.Status != r.Status {
			t.Errorf("callback status для %s = %q, хочу %q", r.Project.PathWithNamespace, cb.Status, r.Status)
		}
	}
}

func TestNewGitRunnerReportsProgress(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	src := makeLocalRepo(t) // helper из main_test.go
	dest := filepath.Join(t.TempDir(), "clone")

	var mu sync.Mutex
	var updates int
	var lastPct int
	run := newGitRunner(func(d string, pct int) {
		mu.Lock()
		updates++
		lastPct = pct
		mu.Unlock()
	})

	if err := run(context.Background(), "clone", "file://"+src, dest); err != nil {
		t.Fatalf("clone не прошёл: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Fatalf("клон не создан: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if updates == 0 {
		t.Fatal("onProgress не вызван ни разу")
	}
	if lastPct < 1 || lastPct > 100 {
		t.Errorf("последний процент = %d, хочу 1..100", lastPct)
	}
}

func TestNewGitRunnerNilProgress(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	src := makeLocalRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	if err := newGitRunner(nil)(context.Background(), "clone", src, dest); err != nil {
		t.Fatalf("clone с nil-callback не прошёл: %v", err)
	}
}
