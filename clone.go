package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// gitRunner абстрагирует запуск git для тестов.
type gitRunner func(ctx context.Context, args ...string) error

// CloneOptions — параметры клонирования.
type CloneOptions struct {
	Out      string            // корневой каталог назначения
	UseSSH   bool              // клонировать по ssh_url_to_repo вместо HTTPS
	Depth    int               // shallow clone; 0 = полный
	Parallel int               // число параллельных клонирований
	OnResult func(CloneResult) // опционально: вызывается в момент завершения каждого клона
}

// CloneResult — итог клонирования одного проекта.
type CloneResult struct {
	Project Project
	Status  string // cloned | skipped | failed | interrupted
	Err     error
}

// cloneAll клонирует проекты пулом воркеров. ctx — только флаг «больше не
// стартуем» (Ctrl+C): уже запущенные клоны выполняются до конца.
func cloneAll(ctx context.Context, projects []Project, opts CloneOptions, run gitRunner) []CloneResult {
	workers := opts.Parallel
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan Project)
	results := make(chan CloneResult)

	for i := 0; i < workers; i++ {
		go func() {
			for p := range jobs {
				var r CloneResult
				if ctx.Err() != nil {
					r = CloneResult{Project: p, Status: "interrupted"}
				} else {
					r = cloneOne(p, opts, run)
				}
				if opts.OnResult != nil {
					opts.OnResult(r)
				}
				results <- r
			}
		}()
	}

	go func() {
		for _, p := range projects {
			jobs <- p
		}
		close(jobs)
	}()

	// Каждый проект даёт ровно один результат — читаем ровно столько.
	all := make([]CloneResult, 0, len(projects))
	for range projects {
		all = append(all, <-results)
	}
	return all
}

func cloneOne(p Project, opts CloneOptions, run gitRunner) CloneResult {
	dest := filepath.Join(opts.Out, p.PathWithNamespace)

	entries, err := os.ReadDir(dest)
	switch {
	case err == nil && len(entries) > 0:
		// Непустой каталог — валидный клон или незавершённый/чужой каталог.
		if _, gitErr := os.Stat(filepath.Join(dest, ".git")); gitErr == nil {
			if verifyErr := run(context.Background(), "-C", dest, "rev-parse", "--verify", "HEAD"); verifyErr == nil {
				return CloneResult{Project: p, Status: "skipped"}
			}
		}
		return CloneResult{Project: p, Status: "failed", Err: fmt.Errorf("каталог %s существует, но не является завершённым клоном; удалите его и повторите запуск", dest)}
	case err == nil:
		// пустой каталог — можно клонировать
	case os.IsNotExist(err):
		// каталога нет — можно клонировать
	default:
		return CloneResult{Project: p, Status: "failed", Err: fmt.Errorf("чтение каталога %s: %w", dest, err)}
	}

	// git clone допускает существующий пустой каталог назначения.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return CloneResult{Project: p, Status: "failed", Err: fmt.Errorf("создание каталога %s: %w", dest, err)}
	}

	url := p.HTTPURLToRepo
	if opts.UseSSH {
		url = p.SSHURLToRepo
	}
	args := []string{"clone"}
	if opts.Depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", opts.Depth))
	}
	args = append(args, url, dest)

	// context.Background(): запущенный клон не убивается по Ctrl+C —
	// политика «дождаться завершённых».
	if err := run(context.Background(), args...); err != nil {
		return CloneResult{Project: p, Status: "failed", Err: err}
	}
	return CloneResult{Project: p, Status: "cloned"}
}

// newGitRunner возвращает раннер, запускающий системный git.
// При заданном onProgress клонирование получает флаг --progress, и проценты
// из stderr git транслируются в callback (dest — последний аргумент clone).
func newGitRunner(onProgress func(dest string, percent int)) gitRunner {
	return func(ctx context.Context, args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)

		// Разные буферы для stdout/stderr: os/exec пишет в них из разных горутин.
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf

		isClone := len(args) > 0 && args[0] == "clone"
		if isClone && onProgress != nil {
			dest := args[len(args)-1]
			cmd.Args = append([]string{"git", "clone", "--progress"}, args[1:]...)
			splitter := newLineSplitter(func(line string) {
				if pct, ok := parseProgressLine(line); ok {
					onProgress(dest, pct)
				}
			})
			cmd.Stderr = io.MultiWriter(&errBuf, splitter)
			defer splitter.Flush()
		}

		err := cmd.Run()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s%s", args, err, errBuf.String(), outBuf.String())
		}
		return nil
	}
}
