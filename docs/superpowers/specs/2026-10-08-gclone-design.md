# gclone — дизайн

- Дата: 2026-10-08
- Статус: подход A одобрен пользователем в чате
- Путь: `golang/claude-study/gclone`

## Назначение

Крошечная Go-утилита для групп проектов GitLab (например `https://gitlab.example.com/platform`):
- `--analyze` — вывести список репозиториев группы без клонирования
- `--clone` — клонировать все репозитории группы

Ключевая идея: авторизация переиспользует уже настроенные в системе credential'ы git
(osxkeychain / SSH-ключи), чтобы в типичном случае работать без передачи токена руками.

## Нефункциональные требования

- Один бинарник; только Go stdlib, ноль сторонних модулей
- Системный `git` нужен как subprocess (клонирование и `git credential fill`)
- Go 1.26, файлы `main.go`, `gitlab.go`, `creds.go`, `clone.go` в package main

## CLI

```
gclone --analyze <group-url>
gclone --clone <group-url> [флаги]
```

URL группы — позиционный аргумент, вида `https://<host>/<путь группы>` (путь может быть
вложенным: `group/subgroup`).

| Флаг | Тип | По умолчанию | Смысл |
|---|---|---|---|
| `--analyze` | bool | — | вывести список проектов |
| `--clone` | bool | — | клонировать все проекты |
| `--token` | string | — | GitLab PAT (перекрывает всё остальное) |
| `--ssh` | bool | false | клонировать по `ssh_url_to_repo` вместо HTTPS |
| `--subgroups` | bool | true | включать подгруппы |
| `--depth` | int | 0 | shallow clone (0 = полный clone) |
| `-p` | int | 4 | параллельных клонирований |
| `--out` | string | `.` | каталог назначения |

Ровно один из `--analyze` / `--clone` обязателен, иначе — usage и exit 2.

## Разрешение токена (по порядку)

1. Флаг `--token`
2. Env `GCLONE_TOKEN`
3. Env `GITLAB_TOKEN`
4. `git credential fill` (stdin: `protocol=https`, `host=<host>`; env `GIT_TERMINAL_PROMPT=0`,
   чтобы не виснуть на интерактивном пароле). Пароль из ответа используем как PAT.

Если ничего не найдено — exit 2 с инструкцией: создать PAT со scope `api`
(`Profile → Access Tokens`) и передать через `--token` или `GCLONE_TOKEN`.

## GitLab API

- `GET https://<host>/api/v4/groups/<url-encoded путь>/projects`
  с `include_subgroups=<bool>`, `per_page=100`, простая пагинация по заголовку `X-Next-Page`
  до пустого значения
- Заголовок `PRIVATE-TOKEN: <token>`
- Из ответа используются поля: `path_with_namespace`, `ssh_url_to_repo`,
  `http_url_to_repo`, `default_branch`, `last_activity_at`
- Коды: `401` — токен невалиден; `403` — у токена нет scope `api` (понятная подсказка);
  `404` — группа не найдена

## Клонирование

- Каталог назначения: `<out>/<path_with_namespace>` (зеркалит структуру GitLab,
  коллизий имён нет)
- Subprocess `git clone [--depth N] <url> <dest>` — авторизация полностью на стороне git
  (keychain / SSH уже настроены у пользователя)
- Если каталог назначения уже существует и не пуст — skip `↷ skipped (exists)`
- Пул из `-p` воркеров; каждая неудача печатается, но не останавливает остальные
- В конце сводка: `N cloned, M skipped, K failed`; exit 1, если были неудачи
- `SIGINT` (Ctrl+C): перестать запускать новые клоны, дождаться завершённых, выйти

## `--analyze` вывод

Выровненная таблица, сортировка по пути:

```
PATH                        BRANCH     UPDATED
platform/backend/api         main       2026-10-01
platform/frontend/web         develop    2026-09-28
...
N projects
```

## Обработка ошибок

| Ситуация | Поведение |
|---|---|
| Не передан URL / оба флага / ни одного | usage, exit 2 |
| Токен не найден | инструкция про PAT, exit 2 |
| API 401 / 403 | подсказка про невалидный токен / scope `api`, exit 2 |
| API 404 | «группа не найдена», exit 2 |
| Сеть недоступна / таймаут (30s) | понятная ошибка, exit 2 |
| Отдельный упавший клон | продолжать остальные, в конце exit 1 |

## Тестирование (TDD)

- `creds_test.go`: цепочка разрешения токена (флаг → env → заглушка credential-филлера
  через интерфейс)
- `gitlab_test.go`: парсинг URL группы, пагинация через `httptest.Server`, маппинг
  401/403/404 в понятные ошибки, вычисление каталога назначения
- `clone_test.go`: пул воркеров, skip существующих, подсчёт итогов (git вызывается через
  интерфейс-заглушку)
- Ручная проверка: `gclone --analyze https://gitlab.example.com/platform` против реального GitLab
  (read-only), затем `--clone` в тестовый каталог

## Границы (не делаем)

- Никаких `--json`, зеркал, pull/update существующих клонов
- Никакой работы с merge requests, CI и прочим API — только список проектов
- Никакого хранения токенов утилитой — только чтение существующих источников
