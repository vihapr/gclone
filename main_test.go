package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFormatAnalyzeTable(t *testing.T) {
	projects := []Project{
		{PathWithNamespace: "platform/web", DefaultBranch: "develop", LastActivityAt: "2026-10-01T15:04:05Z"},
		{PathWithNamespace: "platform/api", DefaultBranch: "", LastActivityAt: ""},
	}

	got := formatAnalyzeTable(projects)

	want := strings.Join([]string{
		"PATH          BRANCH   UPDATED",
		"platform/api  -        -",
		"platform/web  develop  2026-10-01",
		"2 projects",
	}, "\n") + "\n"
	if got != want {
		t.Errorf("таблица:\n%s\nхочу:\n%s", got, want)
	}
}

func TestFormatAnalyzeTableEmpty(t *testing.T) {
	if got := formatAnalyzeTable(nil); got != "0 projects\n" {
		t.Errorf("пустая группа: %q, хочу %q", got, "0 projects\n")
	}
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunUsageErrors(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantStderrP string
	}{
		{"нет аргументов", nil, "Использование"},
		{"оба режима", []string{"--analyze", "--clone", "https://gitlab.example.com/platform"}, "ровно один"},
		{"нет URL", []string{"--analyze"}, "Использование"},
		{"URL без схемы", []string{"--analyze", "gitlab.example.com/platform"}, "http(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, хочу 2", code)
			}
			if !strings.Contains(stderr, tc.wantStderrP) {
				t.Errorf("stderr %q не содержит %q", stderr, tc.wantStderrP)
			}
		})
	}
}

// gitlabStub поднимает httptest-сервер, отвечающий списком проектов
// на любой запрос к API групп, и возвращает URL группы на нём.
func gitlabStub(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/platform"
}

func TestRunAnalyzeE2E(t *testing.T) {
	t.Setenv("GCLONE_TOKEN", "test-tok") // автоматически чистится
	groupURL := gitlabStub(t, `[{"path_with_namespace":"platform/api","ssh_url_to_repo":"","http_url_to_repo":"https://gitlab.example.com/platform/api.git","default_branch":"main","last_activity_at":"2026-10-01T15:04:05Z"}]`)

	code, stdout, stderr := runCLI(t, "--analyze", groupURL)
	if code != 0 {
		t.Fatalf("exit = %d, хочу 0 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "platform/api") || !strings.Contains(stdout, "1 projects") {
		t.Errorf("stdout не содержит проект/счётчик: %q", stdout)
	}
}

func TestRunAnalyzeEmptyGroupE2E(t *testing.T) {
	t.Setenv("GCLONE_TOKEN", "test-tok")
	groupURL := gitlabStub(t, `[]`)

	code, stdout, _ := runCLI(t, "--analyze", groupURL)
	if code != 0 {
		t.Fatalf("exit = %d, хочу 0", code)
	}
	if !strings.Contains(stdout, "0 projects") {
		t.Errorf("stdout = %q, хочу содержать %q", stdout, "0 projects")
	}
}

// makeLocalRepo создаёт настоящий git-репозиторий с одним коммитом
// и возвращает путь к нему (для клонирования по локальному пути).
func makeLocalRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@test")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestRunCloneE2E(t *testing.T) {
	t.Setenv("GCLONE_TOKEN", "test-tok")
	repo := makeLocalRepo(t)
	groupURL := gitlabStub(t, fmt.Sprintf(`[{"path_with_namespace":"platform/api","ssh_url_to_repo":"","http_url_to_repo":%q,"default_branch":"main","last_activity_at":""}]`, repo))

	out := t.TempDir()
	code, _, stderr := runCLI(t, "--clone", "--out", out, groupURL)
	if code != 0 {
		t.Fatalf("exit = %d, хочу 0 (stderr: %s)", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "platform", "api", ".git")); err != nil {
		t.Fatalf("клон не создан: %v", err)
	}

	// Повторный запуск: skip, не ошибка.
	code, stdout, _ := runCLI(t, "--clone", "--out", out, groupURL)
	if code != 0 {
		t.Fatalf("повторный exit = %d, хочу 0", code)
	}
	if !strings.Contains(stdout, "skipped") {
		t.Errorf("повторный stdout = %q, хочу содержать skip", stdout)
	}
}

func TestRunNoToken(t *testing.T) {
	t.Setenv("GCLONE_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	orig := resolveTokenFn
	resolveTokenFn = func(string, func(string) string, credentialFiller, string) (string, error) {
		return "", errors.New("токен не найден")
	}
	t.Cleanup(func() { resolveTokenFn = orig })

	code, _, stderr := runCLI(t, "--analyze", "https://gitlab.example.com/platform")
	if code != 2 {
		t.Errorf("exit = %d, хочу 2", code)
	}
	if !strings.Contains(stderr, "токен") {
		t.Errorf("stderr = %q, хочу сообщение про токен", stderr)
	}
}

func TestHandleSignals(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	canceled := make(chan struct{}, 1)
	exited := make(chan int, 1)
	var stderr strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSignals(sigCh, &stderr, func() { canceled <- struct{}{} }, func(code int) { exited <- code })
	}()

	sigCh <- syscall.SIGINT
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("первый SIGINT должен вызвать cancel")
	}
	if !strings.Contains(stderr.String(), "Ctrl+C") {
		t.Errorf("после первого SIGINT нет подсказки про повторный Ctrl+C: %q", stderr.String())
	}

	sigCh <- syscall.SIGINT
	select {
	case code := <-exited:
		if code != 130 {
			t.Errorf("второй SIGINT: exit = %d, хочу 130", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("второй SIGINT должен вызывать немедленный выход")
	}

	close(sigCh)
	<-done
}
