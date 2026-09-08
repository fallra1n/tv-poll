# TV Poll frontend

Статический frontend для публичного голосования и администрирования опросов.
Основан на PBS-шаблоне: Bun, React 19, Tailwind CSS 4 и shadcn/Base UI.

## Требования

- Bun 1.4.2
- API из `../api/openapi.yaml`, локально доступный на `http://localhost:8080`

## Запуск

```bash
bun install
bun run api:types
bun run dev
```

Frontend открывается на `http://localhost:3000`:

- публичный опрос: `http://localhost:3000/polls/{uuid}`;
- админка: `http://localhost:3000/admin`.

API origin настраивается переменной `BUN_PUBLIC_API_URL`. Значение по
умолчанию для локальной разработки — `http://localhost:8080`. В переменную
попадает только публичный адрес API; `ADMIN_TOKEN` в frontend environment
передавать нельзя.

## Команды

```bash
bun run dev          # Bun dev server с HMR
bun run api:types    # типы из ../api/openapi.yaml
bun run lint         # Oxlint
bun run typecheck    # TypeScript
bun run test         # unit/API tests
bun run test:e2e     # Playwright: desktop + mobile Chromium
bun run build        # статический dist/
```

Перед первым e2e-запуском нужен браузер Playwright:

```bash
bunx playwright install chromium
```

## Архитектура

Сборка имеет два HTML entrypoint:

- `src/index.html` загружает только публичный voting flow;
- `src/admin/index.html` загружает роутер, формы и графики админки.

Общие React/UI-зависимости выносятся Bun в shared chunk. Recharts и
административный код не загружаются на массовой публичной странице.

Публичный клиент получает токен один раз, хранит его под ключом конкретного
опроса в `localStorage` и отправляет через `X-Vote-Token`. Запросы `/token` и
`/votes` также используют `credentials: include`, поэтому cookie остаётся
резервным каналом — **кроме локальной разработки по http**: бэкенд ставит
`vote_token` с `SameSite=None`, а такую комбинацию без `Secure` браузер молча
отбрасывает (`COOKIE_SECURE=false` в `docker-compose.yml` — обнаружено при
проверке против реального `make up`, не по описанию контракта). На проде за
HTTPS резервный канал работает, как задумано. Выбранный вариант локально не
сохраняется.

Bearer-токен администратора вводится на экране входа и хранится только в
`sessionStorage`. Ответ `401` очищает сессию. Это клиентский UX-механизм;
авторизацию всегда обеспечивает API.

## CDN

`dist/` предназначен для статического CDN, а не для раздачи Go-сервисом.
Нужны два fallback-правила:

```text
/polls/*       -> /index.html
/admin         -> /admin/index.html
/admin/*       -> /admin/index.html
```

Ссылки на assets абсолютные (`/assets/...`), поэтому deep links работают
после fallback. Рекомендуемая политика кеша:

```text
/index.html, /admin/index.html  Cache-Control: no-cache
/assets/*                       Cache-Control: public, max-age=31536000, immutable
```

В production `BUN_PUBLIC_API_URL` должен указывать на edge/API origin, где
`GET /v1/polls/{id}` кешируется согласно ответу API, а POST-запросы проходят
к backend без кеширования.
