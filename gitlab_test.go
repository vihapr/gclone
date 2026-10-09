package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseGroupURL(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		baseURL   string
		groupPath string
	}{
		{"простая группа", "https://gitlab.example.com/platform", "https://gitlab.example.com", "platform"},
		{"вложенная группа", "https://gitlab.example.com/platform/backend", "https://gitlab.example.com", "platform/backend"},
		{"трейлинг-слэш", "https://gitlab.example.com/platform/", "https://gitlab.example.com", "platform"},
		{"схема http", "http://gitlab.local/g", "http://gitlab.local", "g"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseURL, groupPath, err := parseGroupURL(tc.in)
			if err != nil {
				t.Fatalf("не ожидалась ошибка: %v", err)
			}
			if baseURL != tc.baseURL {
				t.Errorf("baseURL = %q, хочу %q", baseURL, tc.baseURL)
			}
			if groupPath != tc.groupPath {
				t.Errorf("groupPath = %q, хочу %q", groupPath, tc.groupPath)
			}
		})
	}
}

func TestParseGroupURLErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"пустая строка", ""},
		{"нет схемы", "gitlab.example.com/platform"},
		{"нет пути", "https://gitlab.example.com"},
		{"пустой путь", "https://gitlab.example.com/"},
		{"не http схема", "ftp://gitlab.example.com/g"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseGroupURL(tc.in)
			if err == nil {
				t.Fatalf("ожидалась ошибка для %q", tc.in)
			}
		})
	}
}

func TestListProjectsPaginates(t *testing.T) {
	var requests int
	var sawToken, sawPerPage, sawIncludeSubgroups, sawEscapedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		sawToken = r.Header.Get("PRIVATE-TOKEN")
		sawPerPage = r.URL.Query().Get("per_page")
		sawIncludeSubgroups = r.URL.Query().Get("include_subgroups")
		sawEscapedPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"path_with_namespace":"platform/backend/api","ssh_url_to_repo":"git@gitlab.example.com:platform/backend/api.git","http_url_to_repo":"https://gitlab.example.com/platform/backend/api.git","default_branch":"main","last_activity_at":"2026-10-01T15:04:05Z"},{"path_with_namespace":"platform/web","ssh_url_to_repo":"git@gitlab.example.com:platform/web.git","http_url_to_repo":"https://gitlab.example.com/platform/web.git","default_branch":"develop","last_activity_at":"2026-09-28T10:00:00Z"}]`)
			return
		}
		fmt.Fprint(w, `[{"path_with_namespace":"platform/mobile","ssh_url_to_repo":"git@gitlab.example.com:platform/mobile.git","http_url_to_repo":"https://gitlab.example.com:platform/mobile.git","default_branch":"main","last_activity_at":"2026-10-05T08:30:00Z"}]`)
	}))
	defer srv.Close()

	projects, err := listProjects(srv.Client(), srv.URL, "platform/backend", "tok", true)
	if err != nil {
		t.Fatalf("не ожидалась ошибка: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("получено %d проектов, хочу 3", len(projects))
	}
	if requests != 2 {
		t.Errorf("запросов %d, хочу 2 (по страницам)", requests)
	}
	if sawToken != "tok" {
		t.Errorf("PRIVATE-TOKEN = %q, хочу %q", sawToken, "tok")
	}
	if sawPerPage != "100" {
		t.Errorf("per_page = %q, хочу 100", sawPerPage)
	}
	if sawIncludeSubgroups != "true" {
		t.Errorf("include_subgroups = %q, хочу true", sawIncludeSubgroups)
	}
	if want := "/api/v4/groups/platform%2Fbackend/projects"; sawEscapedPath != want {
		t.Errorf("путь запроса = %q, хочу %q", sawEscapedPath, want)
	}
	// поля первого проекта маппятся корректно
	p := projects[0]
	if p.PathWithNamespace != "platform/backend/api" || p.DefaultBranch != "main" || p.SSHURLToRepo != "git@gitlab.example.com:platform/backend/api.git" || p.HTTPURLToRepo != "https://gitlab.example.com/platform/backend/api.git" || p.LastActivityAt != "2026-10-01T15:04:05Z" {
		t.Errorf("некорректный маппинг полей: %+v", p)
	}
}

func TestListProjectsSinglePage(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"path_with_namespace":"platform/web","ssh_url_to_repo":"","http_url_to_repo":"https://gitlab.example.com/platform/web.git","default_branch":"main","last_activity_at":""}]`)
	}))
	defer srv.Close()

	projects, err := listProjects(srv.Client(), srv.URL, "platform", "tok", false)
	if err != nil {
		t.Fatalf("не ожидалась ошибка: %v", err)
	}
	if requests != 1 {
		t.Errorf("запросов %d, хочу 1 (X-Next-Page отсутствует → второй страницы нет)", requests)
	}
	if len(projects) != 1 {
		t.Errorf("получено %d проектов, хочу 1", len(projects))
	}
}

func TestListProjectsErrors(t *testing.T) {
	cases := []struct {
		status    int
		wantPatch string
	}{
		{401, "невалиден"},
		{403, "scope"},
		{404, "не найдена"},
		{500, "статус 500"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("статус %d", tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			_, err := listProjects(srv.Client(), srv.URL, "platform", "tok", true)
			if err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if !strings.Contains(err.Error(), tc.wantPatch) {
				t.Errorf("ошибка %q не содержит %q", err.Error(), tc.wantPatch)
			}
		})
	}
}
