package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// parseGroupURL разбирает URL группы GitLab вида https://<host>/<путь группы>.
// Возвращает базовый URL (scheme://host) и путь группы без слэшей по краям.
func parseGroupURL(raw string) (baseURL, groupPath string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("некорректный URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("ожидается http(s) URL группы, получено %q", raw)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("в URL отсутствует хост: %q", raw)
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return "", "", fmt.Errorf("ожидается URL группы https://<хост>/<группа>, получено %q", raw)
	}
	return u.Scheme + "://" + u.Host, path, nil
}

// Project — репозиторий из GitLab API (используемые поля).
type Project struct {
	PathWithNamespace string `json:"path_with_namespace"`
	SSHURLToRepo      string `json:"ssh_url_to_repo"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	DefaultBranch     string `json:"default_branch"`
	LastActivityAt    string `json:"last_activity_at"`
}

// APIError — неуспешный ответ GitLab API.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	switch e.Status {
	case 401:
		return fmt.Sprintf("GitLab API: токен невалиден (401). %s", tokenHelp)
	case 403:
		return fmt.Sprintf("GitLab API: у токена нет нужных прав (403) — вероятно, отсутствует scope api. %s", tokenHelp)
	case 404:
		return fmt.Sprintf("группа не найдена: %s", e.Body)
	default:
		return fmt.Sprintf("GitLab API: статус %d", e.Status)
	}
}

// listProjects возвращает все проекты группы, включая подгруппы,
// с пагинацией по заголовку X-Next-Page.
func listProjects(client *http.Client, baseURL, groupPath, token string, includeSubgroups bool) ([]Project, error) {
	var all []Project
	page := "1"
	for page != "" {
		u := fmt.Sprintf("%s/api/v4/groups/%s/projects?include_subgroups=%t&per_page=100&page=%s",
			baseURL, url.PathEscape(groupPath), includeSubgroups, page)
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("запрос к GitLab API: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			apiErr := &APIError{Status: resp.StatusCode, Body: groupPath}
			if resp.StatusCode != 404 {
				apiErr.Body = string(body)
			}
			return nil, apiErr
		}
		var projects []Project
		if err := json.Unmarshal(body, &projects); err != nil {
			return nil, fmt.Errorf("разбор ответа GitLab API: %w", err)
		}
		all = append(all, projects...)
		page = resp.Header.Get("X-Next-Page")
	}
	return all, nil
}
