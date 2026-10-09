package main

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// credentialFiller абстрагирует `git credential fill` для тестов.
type credentialFiller func(protocol, host string) (username, password string, err error)

const tokenHelp = "создайте Personal Access Token со scope `api` (достаточно `read_api`; GitLab: Profile → Access Tokens) и передайте его флагом --token или переменной окружения GCLONE_TOKEN"

// resolveToken ищет токен по цепочке: флаг → GCLONE_TOKEN → GITLAB_TOKEN → git credential fill.
func resolveToken(flagToken string, getenv func(string) string, fill credentialFiller, host string) (string, error) {
	if flagToken != "" {
		return flagToken, nil
	}
	if t := getenv("GCLONE_TOKEN"); t != "" {
		return t, nil
	}
	if t := getenv("GITLAB_TOKEN"); t != "" {
		return t, nil
	}
	_, password, err := fill("https", host)
	if err == nil && password != "" {
		return password, nil
	}
	return "", fmt.Errorf("токен не найден: %s", tokenHelp)
}

// gitCredentialFill спрашивает сохранённые credential у git (osxkeychain и т.п.).
func gitCredentialFill(protocol, host string) (string, string, error) {
	cmd := exec.Command("git", "credential", "fill")
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0", // не виснуть на интерактивном пароле
		"GIT_ASKPASS=true",      // и не запускать askpass
	)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("protocol=%s\nhost=%s\n\n", protocol, host))
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git credential fill: %w", err)
	}
	return parseCredentialOutput(strings.NewReader(string(out)))
}

// parseCredentialOutput разбирает построчный вывод git credential fill
// до первой пустой строки; строки после неё игнорируются.
func parseCredentialOutput(r io.Reader) (username, password string, err error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		switch {
		case strings.HasPrefix(line, "username="):
			username = strings.TrimPrefix(line, "username=")
		case strings.HasPrefix(line, "password="):
			password = strings.TrimPrefix(line, "password=")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if password == "" {
		return "", "", fmt.Errorf("в выводе git credential fill нет password")
	}
	return username, password, nil
}
