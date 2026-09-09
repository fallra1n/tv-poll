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

## Что сознательно остаётся за границей demo

- атомарная граница между Redis dedup claim и in-memory increment;
- корректный baseline после полной потери Redis;
- Redis auth/TLS/Cluster и multi-host HA;
- CDN для массовой публичной страницы и pre-scaling нескольких API replicas;
- production secret manager и автоматические backups.

Эти ограничения нельзя выдавать за production-ready состояние; они уже
зафиксированы как риски аудита и требуют отдельной архитектурной итерации.
