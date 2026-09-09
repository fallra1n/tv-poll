# Сессия: demo-деплой в JakeLoud

Дата: 2026-09-09.

## Запрос и границы

После аудита готовности пользователь выбрал demo-деплой тестового, а не
production под расчётные 75k голосов/с. PostgreSQL и Redis должны жить на том
же сервере JakeLoud, адрес на первом этапе — `sslip.io`.

Это важная граница: один self-hosted JakeLoud-хост подходит для демонстрации
полного flow, но не заменяет CDN, Redis HA/Cluster и горизонтально
масштабируемый API из `02-load-model.md`.

## Что обнаружилось до реализации

- Git remote уже настроен на `git@github.com:fallra1n/tv-poll.git`, рабочее
  дерево было чистым и совпадало с `origin/main`.
- Проект `tv-poll` уже существовал в JakeLoud, но release 1 завершился с
  ошибкой: default-команда выполнила `docker build` из корня, где не было
  `Dockerfile`.
- В проекте не были заданы domain и custom command.
- JakeLoud v2 запускает одну foreground-команду из release directory,
  передаёт ей `$PORT` и проксирует Nginx на host port. Per-project secrets и
  managed databases платформа не предоставляет.

## Принятые решения

1. Добавить корневой multi-stage image: Bun собирает оба frontend entrypoint,
   Go собирает `api` и `migrate`, финальный non-root container содержит оба
   бинарника и `frontend/dist`.
2. Для demo Go опционально раздаёт статику при заданном `FRONTEND_DIR`.
   Backend-only/CDN-режим остаётся прежним, если переменная пуста.
3. Публиковать candidate container только на
   `127.0.0.1:$PORT`, чтобы единственным ingress был Nginx JakeLoud.
4. Доверять первому `X-Forwarded-For` только при явном
   `TRUST_PROXY_HEADERS=true`. Без флага spoofable header игнорируется.
5. Держать PostgreSQL и Redis в отдельных host-level containers с named
   volumes и `restart=unless-stopped`, вне жизненного цикла release. Redis
   получает AOF и `noeviction`; наружу порты зависимостей не публикуются.
6. Хранить секреты только в `/etc/tvpoll/*.env` с mode `0600`. В Git и custom
   command попадают только пути к файлам.
7. Перед каждым запуском API применять встроенные Goose migrations отдельным
   одноразовым container. Для одного demo-проекта операция идемпотентна.
8. Закрывать poll автоматически по `closes_at` тем же leader-controlled
   scheduler, который выполняет auto-open. Ручной close остаётся досрочным
   операторским действием.
9. Попытка выполнить provisioning по SSH из рабочей среды обнаружила дефект
   cloud image: root key принимался, но forced command требовал пользователя
   `NONE`, для которого тот же ключ не авторизован. Поэтому первый release
   умеет безопасно вызвать idempotent provisioner сам. В custom command
   передаётся только несекретный `PUBLIC_URL`; JakeLoud запускает команду от
   root, а сгенерированные секреты остаются в `/etc/tvpoll`.
10. Изменение domain и следующий full reboot могут ненадолго запустить два
    candidate release параллельно. Первый реальный запуск поймал эту гонку:
    один process создал `tvpoll-redis`, пока второй уже прошёл `inspect` и
    тоже вызвал `docker run`. Весь host-wide provisioning сериализован через
    `flock`; это защищает и создание containers, и первоначальную запись пары
    env-файлов с общим database password.

## Проверка и найденная ошибочная ветка

До изменений baseline был зелёным: backend short tests, Oxlint, TypeScript и
18 frontend tests. После изменений выполнены:

- полный `go test ./...`, включая реальные Postgres 16 и Redis 7 через
  testcontainers;
- повторная генерация OpenAPI types без diff;
- frontend lint, typecheck, 18 unit/component tests и production build;
- ShellCheck обоих deployment scripts;
- root Docker build с production `BUN_PUBLIC_API_URL`;
- запуск собранного image против реальных зависимостей: Goose migration,
  `/healthz`, `/admin/*`, `/polls/*` и immutable `/assets/*`;
- live Playwright в desktop/mobile: create → schedule → auto-open → vote →
  duplicate → auto-close → финальный результат.

Первая версия расширенного live-теста падала ровно на 30 секундах, хотя
`toPass` получил timeout 45 секунд. Причина была не в auto-close: глобальный
Playwright test timeout оставался 30 секунд, а минимальное voting window равно
30 секундам плюс две секунды до `scheduled_at`. API и логи подтвердили, что
poll успешно закрылся сразу после прерывания теста. Исправление — локальный
timeout 60 секунд для единственного live-теста; повторный прогон дал 10/10.

## Фактический JakeLoud smoke

Изменения были отправлены в `main` тремя проверенными коммитами:

- `0d94890` — root image, runtime/config изменения и deployment scripts;
- `a31b933` — self-bootstrap persistent dependencies без отдельного SSH;
- `57143ff` — сериализация параллельного provisioning.

JakeLoud release 4 собрал `57143ff`, применил Goose migration version 1 и
после пятиминутного liveness window перешёл в `running/active` на внутреннем
host port 38003. Публичный origin:
`https://tv-poll.158.160.165.223.sslip.io`.

После promotion с внешней машины проверено:

- HTTP перенаправляется на HTTPS с `301`;
- сертификат Let's Encrypt выдан именно для
  `tv-poll.158.160.165.223.sslip.io`;
- `/healthz` отвечает `200` и сообщает `postgres=ok`, `redis=ok`;
- `/admin` и прямой SPA deep link `/polls/<uuid>` отвечают `200`;
- production assets отвечают `200` с immutable cache;
- CORS preflight отвечает `204` с ожидаемыми allow/expose headers;
- admin API без bearer token отвечает `401`.

Финальный flow выполнен на реальном poll
`a1be6e05-d0cc-4250-af66-a2a89bd876a3` с окном
`09:25:59Z`–`09:26:59Z`:

1. Опубликованное определение было доступно до эфира.
2. До auto-open голос вернул `409 rejected/closed`.
3. После auto-open голос за option 1 вернул `201 accepted`.
4. Повтор тем же token за option 2 вернул `409 duplicate` и исходный
   `option_id=1`.
5. После `closes_at` неиспользованный token вернул `401`, потому что срок его
   HMAC-подписи совпадает с концом окна.
6. В admin UI после refresh подтверждены `closed`, один принятый голос,
   option 1 = 1, option 2 = 0 и один duplicate. Это отдельно подтверждает
   auto-close и PostgreSQL snapshot; их нельзя вывести только из публичного
   отказа после временной границы.

## Что сознательно остаётся за границей demo

- атомарная граница между Redis dedup claim и in-memory increment;
- корректный baseline после полной потери Redis;
- Redis auth/TLS/Cluster и multi-host HA;
- CDN для массовой публичной страницы и pre-scaling нескольких API replicas;
- production secret manager и автоматические backups.

Эти ограничения нельзя выдавать за production-ready состояние; они уже
зафиксированы как риски аудита и требуют отдельной архитектурной итерации.
