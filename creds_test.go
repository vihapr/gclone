package main

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveToken(t *testing.T) {
	t.Run("флаг перекрывает env и fill", func(t *testing.T) {
		fillCalled := false
		fill := func(protocol, host string) (string, string, error) {
			fillCalled = true
			return "", "", nil
		}
		token, err := resolveToken("flag-tok", func(string) string { return "env-tok" }, fill, "gitlab.example.com")
		if err != nil {
			t.Fatalf("не ожидалась ошибка: %v", err)
		}
		if token != "flag-tok" {
			t.Errorf("token = %q, хочу %q", token, "flag-tok")
		}
		if fillCalled {
			t.Error("fill не должен вызываться, когда задан флаг")
		}
	})

	t.Run("GCLONE_TOKEN перекрывает GITLAB_TOKEN", func(t *testing.T) {
		fillCalled := false
		fill := func(protocol, host string) (string, string, error) {
			fillCalled = true
			return "", "", nil
		}
		getenv := func(key string) string {
			if key == "GCLONE_TOKEN" {
				return "gclone-env"
			}
			if key == "GITLAB_TOKEN" {
				return "gitlab-env"
			}
			return ""
		}
		token, err := resolveToken("", getenv, fill, "gitlab.example.com")
		if err != nil {
			t.Fatalf("не ожидалась ошибка: %v", err)
		}
		if token != "gclone-env" {
			t.Errorf("token = %q, хочу %q", token, "gclone-env")
		}
		if fillCalled {
			t.Error("fill не должен вызываться, когда задан env")
		}
	})

	t.Run("GITLAB_TOKEN используется", func(t *testing.T) {
		getenv := func(key string) string {
			if key == "GITLAB_TOKEN" {
				return "gitlab-env"
			}
			return ""
		}
		token, err := resolveToken("", getenv, func(string, string) (string, string, error) {
			t.Fatal("fill не должен вызываться")
			return "", "", nil
		}, "gitlab.example.com")
		if err != nil {
			t.Fatalf("не ожидалась ошибка: %v", err)
		}
		if token != "gitlab-env" {
			t.Errorf("token = %q, хочу %q", token, "gitlab-env")
		}
	})

	t.Run("fallback на git credential fill", func(t *testing.T) {
		var gotProtocol, gotHost string
		fill := func(protocol, host string) (string, string, error) {
			gotProtocol, gotHost = protocol, host
			return "viktor", "glpat-123", nil
		}
		token, err := resolveToken("", func(string) string { return "" }, fill, "gitlab.example.com")
		if err != nil {
			t.Fatalf("не ожидалась ошибка: %v", err)
		}
		if token != "glpat-123" {
			t.Errorf("token = %q, хочу %q", token, "glpat-123")
		}
		if gotProtocol != "https" || gotHost != "gitlab.example.com" {
			t.Errorf("fill вызван с (%q, %q), хочу (https, gitlab.example.com)", gotProtocol, gotHost)
		}
	})

	t.Run("fill вернул ошибку", func(t *testing.T) {
		fill := func(string, string) (string, string, error) {
			return "", "", errors.New("нет сохранённых credential")
		}
		_, err := resolveToken("", func(string) string { return "" }, fill, "gitlab.example.com")
		if err == nil {
			t.Fatal("ожидалась ошибка")
		}
		if !strings.Contains(err.Error(), "Access Tokens") {
			t.Errorf("ошибка должна содержать подсказку про Access Tokens: %v", err)
		}
	})

	t.Run("все источники пусты", func(t *testing.T) {
		_, err := resolveToken("", func(string) string { return "" }, func(string, string) (string, string, error) {
			return "", "", nil
		}, "gitlab.example.com")
		if err == nil {
			t.Fatal("ожидалась ошибка")
		}
		if !strings.Contains(err.Error(), "Access Tokens") {
			t.Errorf("ошибка должна содержать подсказку про Access Tokens: %v", err)
		}
	})
}

func TestParseCredentialOutput(t *testing.T) {
	t.Run("полный вывод", func(t *testing.T) {
		in := "protocol=https\nhost=x\nusername=u\npassword=p\n\n"
		username, password, err := parseCredentialOutput(strings.NewReader(in))
		if err != nil {
			t.Fatalf("не ожидалась ошибка: %v", err)
		}
		if username != "u" || password != "p" {
			t.Errorf("получено (%q, %q), хочу (u, p)", username, password)
		}
	})

	t.Run("строки после пустой игнорируются", func(t *testing.T) {
		in := "username=u\npassword=p\n\nlater=ignored\n"
		username, password, err := parseCredentialOutput(strings.NewReader(in))
		if err != nil {
			t.Fatalf("не ожидалась ошибка: %v", err)
		}
		if username != "u" || password != "p" {
			t.Errorf("получено (%q, %q), хочу (u, p)", username, password)
		}
	})

	t.Run("нет password", func(t *testing.T) {
		in := "protocol=https\nhost=x\nusername=u\n\n"
		_, _, err := parseCredentialOutput(strings.NewReader(in))
		if err == nil {
			t.Fatal("ожидалась ошибка без password")
		}
	})

	t.Run("пустой ввод", func(t *testing.T) {
		_, _, err := parseCredentialOutput(strings.NewReader(""))
		if err == nil {
			t.Fatal("ожидалась ошибка на пустом вводе")
		}
	})
}
