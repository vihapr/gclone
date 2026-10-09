package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Швы для тестов: подменяются в main_test.go.
var (
	resolveTokenFn  = resolveToken
	listProjectsFn  = listProjects
	apiHTTPClientFn = func() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run — вся логика CLI; возвращает код выхода.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gclone", flag.ContinueOnError)
	fs.SetOutput(stderr)
	analyze := fs.Bool("analyze", false, "вывести список проектов группы без клонирования")
	clone := fs.Bool("clone", false, "клонировать все проекты группы")
	token := fs.String("token", "", "GitLab API токен (Personal Access Token)")
	useSSH := fs.Bool("ssh", false, "клонировать по SSH URL вместо HTTPS")
	subgroups := fs.Bool("subgroups", true, "включать проекты подгрупп")
	depth := fs.Int("depth", 0, "shallow clone указанной глубины (0 — полный clone)")
	parallel := fs.Int("p", 4, "число параллельных клонирований")
	out := fs.String("out", ".", "каталог назначения")

	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usage(fs))
		return 2
	}
	if *analyze == *clone { // ни одного или оба
		fmt.Fprintln(stderr, "ошибка: укажите ровно один режим --analyze или --clone")
		fmt.Fprint(stderr, usage(fs))
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "ошибка: ожидается один аргумент — URL группы, например https://gitlab.example.com/platform")
		fmt.Fprint(stderr, usage(fs))
		return 2
	}

	baseURL, groupPath, err := parseGroupURL(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "ошибка: %v\n", err)
		return 2
	}
	host := baseURL
	if u, err := url.Parse(baseURL); err == nil {
		host = u.Host
	}

	token2, err := resolveTokenFn(*token, os.Getenv, gitCredentialFill, host)
	if err != nil {
		fmt.Fprintf(stderr, "ошибка: %v\n", err)
		return 2
	}

	projects, err := listProjectsFn(apiHTTPClientFn(), baseURL, groupPath, token2, *subgroups)
	if err != nil {
		fmt.Fprintf(stderr, "ошибка: %v\n", err)
		return 2
	}

	if *analyze {
		fmt.Fprint(stdout, formatAnalyzeTable(projects))
		return 0
	}

	// Первый SIGINT — перестать запускать новые клоны и дождаться текущих;
	// второй — немедленный выход.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(sigCh, stderr, cancel, os.Exit)

	// Живой прогресс: проценты git → трекер → пульс; результат — в момент события.
	tracker := newProgressTracker(len(projects))
	destToName := make(map[string]string, len(projects))
	for _, p := range projects {
		destToName[filepath.Join(*out, p.PathWithNamespace)] = p.PathWithNamespace
	}
	onResult := func(r CloneResult) {
		tracker.Finish(r.Project.PathWithNamespace)
		switch r.Status {
		case "cloned":
			fmt.Fprintf(stdout, "✔ %s\n", r.Project.PathWithNamespace)
		case "skipped":
			fmt.Fprintf(stdout, "↷ %s (уже существует)\n", r.Project.PathWithNamespace)
		case "failed":
			fmt.Fprintf(stdout, "✖ %s: %v\n", r.Project.PathWithNamespace, r.Err)
		case "interrupted":
			fmt.Fprintf(stdout, "– %s (прервано)\n", r.Project.PathWithNamespace)
		}
	}

	startedAt := time.Now()
	hbStop := make(chan struct{})
	go startHeartbeat(stdout, tracker, startedAt, heartbeatInterval, hbStop)

	results := cloneAll(ctx, projects, CloneOptions{
		Out:      *out,
		UseSSH:   *useSSH,
		Depth:    *depth,
		Parallel: *parallel,
		OnResult: onResult,
	}, newGitRunner(func(dest string, percent int) {
		if name, ok := destToName[dest]; ok {
			tracker.Update(name, percent)
		}
	}))
	close(hbStop)

	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}

	summary := fmt.Sprintf("%d cloned, %d skipped, %d failed", counts["cloned"], counts["skipped"], counts["failed"])
	if counts["interrupted"] > 0 {
		summary += fmt.Sprintf(", %d interrupted", counts["interrupted"])
	}
	fmt.Fprintln(stdout, summary)

	switch {
	case counts["failed"] > 0:
		return 1
	case counts["interrupted"] > 0:
		return 130
	default:
		return 0
	}
}

// handleSignals: первый сигнал — graceful cancel с подсказкой,
// второй — немедленный выход с кодом 130.
func handleSignals(sigCh <-chan os.Signal, stderr io.Writer, cancel func(), exit func(int)) {
	received := 0
	for range sigCh {
		received++
		if received == 1 {
			fmt.Fprintln(stderr, "прерывание: новые клоны не запускаются, ждём завершения текущих (Ctrl+C ещё раз — немедленный выход)")
			cancel()
			continue
		}
		exit(130)
	}
}

// formatAnalyzeTable — таблица PATH/BRANCH/UPDATED, отсортированная по пути.
func formatAnalyzeTable(projects []Project) string {
	if len(projects) == 0 {
		return "0 projects\n"
	}
	sorted := make([]Project, len(projects))
	copy(sorted, projects)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PathWithNamespace < sorted[j].PathWithNamespace })

	rows := make([][3]string, 0, len(sorted)+1)
	rows = append(rows, [3]string{"PATH", "BRANCH", "UPDATED"})
	for _, p := range sorted {
		branch := p.DefaultBranch
		if branch == "" {
			branch = "-"
		}
		updated := "-"
		if len(p.LastActivityAt) >= 10 {
			updated = p.LastActivityAt[:10]
		}
		rows = append(rows, [3]string{p.PathWithNamespace, branch, updated})
	}

	widths := make([]int, 3)
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			b.WriteString(cell)
			if i < 2 { // последняя колонка без выравнивания
				b.WriteString(strings.Repeat(" ", widths[i]-len(cell)+2))
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d projects\n", len(projects))
	return b.String()
}

// usage возвращает справку по флагам.
func usage(fs *flag.FlagSet) string {
	var b strings.Builder
	b.WriteString("Использование: gclone --analyze|--clone <URL группы> [флаги]\n")
	b.WriteString("Пример: gclone --clone https://gitlab.example.com/platform\n\n")
	fs.SetOutput(&b)
	fs.PrintDefaults()
	return b.String()
}
