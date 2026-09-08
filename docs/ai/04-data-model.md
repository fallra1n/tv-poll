# Модель данных: Postgres, Redis, и зазор между ними

Это последний документ из изначально запланированной тройки 03/04/05
(`README.md`). К этому моменту токен и dedup-ключ уже зафиксированы
(`03-deduplication.md`), а wire-контракт — в `api/openapi.yaml`. Здесь —
собственно таблицы и Redis-ключи, из которых всё это собирается.

## Найденная по пути ошибка, которую стоит зафиксировать явно

Пока раскладывал таблицы, обнаружился реальный конфликт с уже принятыми
решениями, а не гипотетический. `02-load-model.md` §4.2 описывает хендлер
голосования так: «разбор JSON → проверка HMAC-подписи токена → один
`EVALSHA` в Redis → сериализация ответа. **Никаких обращений к Postgres**».
Но нигде не сказано, откуда хендлер на этом пути берёт три вещи, без
которых он не может ни принять, ни отклонить голос:

- `state` опроса (открыт ли приём),
- `closes_at` (не истекло ли окно),
- множество валидных `option_id` (чтобы отклонить мусорный `option_id`
  с `400`, а не молча инкрементить несуществующий вариант).

Если это читать из Postgres на каждый голос — прямое нарушение «никаких
обращений к Postgres» и лишний round-trip сверх заявленного. Если каждый
Go-инстанс кеширует это в памяти процесса — возникает риск рассинхрона
между инстансами (один узнал о ручном закрытии опроса раньше другого, и
кто-то продолжает принимать голоса после закрытия — уже не «безобидная»
неточность, а нарушение целостности результата).

Решение — в §2.3 ниже: эти поля живут в самом Redis, и Lua-скрипт читает их
**в рамках того же `EVALSHA`**, а не отдельным вызовом. Round-trip
по-прежнему один (клиент делает один сетевой вызов к Redis), просто внутри
этого одного вызова Lua теперь выполняет не две Redis-команды, а четыре-пять
— это не сетевые операции, они не стоят лишних round-trip'ов, и заявленный
в `02-load-model.md` бюджет латентности не нарушается.

---

## 1. PostgreSQL

Четыре таблицы — ровно то, что перечислено в `01-stack.md` («опросы,
варианты ответов, окна голосования, состояния, снапшоты результатов,
аудит действий администратора»), ничего сверх.

### 1.1 `polls`

```sql
CREATE TABLE polls (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    question               TEXT NOT NULL CHECK (length(question) BETWEEN 1 AND 500),
    state                  TEXT NOT NULL DEFAULT 'draft'
                               CHECK (state IN ('draft', 'scheduled', 'open', 'closed')),

    scheduled_at           TIMESTAMPTZ,
    opens_at               TIMESTAMPTZ,
    voting_window_seconds  INTEGER NOT NULL DEFAULT 300 CHECK (voting_window_seconds >= 30),
    closes_at              TIMESTAMPTZ,
    closed_at              TIMESTAMPTZ,

    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION polls_set_closes_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.closes_at := NEW.opens_at + NEW.voting_window_seconds * INTERVAL '1 second';
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER polls_closes_at_trigger
    BEFORE INSERT OR UPDATE OF opens_at, voting_window_seconds ON polls
    FOR EACH ROW EXECUTE FUNCTION polls_set_closes_at();

CREATE INDEX polls_state_idx ON polls (state);
```

Решения, которые стоит объяснить:

- **`closes_at` считает триггер, а не приложение — и не `GENERATED ALWAYS
  AS`.** Первая версия этого документа как раз использовала
  generated-колонку (`GENERATED ALWAYS AS (opens_at + voting_window_seconds
  * INTERVAL '1 second') STORED`) — идея была правильная (в
  `03-deduplication.md` зафиксировано «TTL = `closes_at` опроса», и явный
  расчёт в коде приложения — источник ровно того класса багов, который там
  разбирался на примере фиксированного TTL), но сам DDL не проходит:
  прогнал его на реальном Postgres 16 в docker, и `CREATE TABLE` падает с
  `ERROR: generation expression is not immutable`. Причина — оператор
  `timestamptz + interval` в Postgres помечен `STABLE`, а не `IMMUTABLE`
  (арифметика с датами формально зависит от часового пояса сессии из-за
  перехода на летнее/зимнее время), а generated-колонки требуют
  immutable-выражение. Ограничение действует на любое выражение вида
  `timestamptz + interval`, независимо от того, что именно в интервале —
  даже если там только секунды.

  Триггер даёт ту же гарантию не поломанным способом: приложение может
  вообще не трогать `closes_at`, значение всегда проставляется `BEFORE
  INSERT OR UPDATE` из `opens_at`/`voting_window_seconds` — так же
  невозможно рассинхронизировать вручную. Проверено на том же Postgres 16:
  вставка опроса без `opens_at` (draft) даёт `closes_at = NULL`, вставка
  с `opens_at` и `voting_window_seconds = 300` даёт `closes_at`, ровно на
  5 минут позже `opens_at`.
- `state` — `TEXT + CHECK`, не Postgres `ENUM`: добавление нового состояния
  в `ENUM` требует `ALTER TYPE`, что подключает лишнюю сложность миграций
  ради множества из 4 значений, которое почти наверняка не изменится.
- `opens_at`/`closes_at`/`closed_at` — три разных момента, не один:
  `opens_at` — когда реально открылось голосование (может отличаться от
  `scheduled_at`, если открыли вручную или прескейлинг сработал не секунда
  в секунду); `closes_at` — расчётное время закрытия, использовалось для
  TTL токена и dedup-ключа (`03-deduplication.md`); `closed_at` — когда
  реально закрылось (может быть раньше `closes_at`, если админ закрыл
  вручную досрочно). Разница между `closes_at` и `closed_at` — единственный
  способ отличить в данных «закрылось по расписанию» от «закрыли руками
  раньше срока».
- Нет `created_by`/`actor`: в системе один статический admin-токен без
  ролей (`01-stack.md`, «Аутентификация админки») — писать в схему поле,
  которое всегда будет одним и тем же значением, бессмысленно.

### 1.2 `poll_options`

```sql
CREATE TABLE poll_options (
    poll_id     UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    ordinal     SMALLINT NOT NULL CHECK (ordinal >= 1),
    label       TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 200),
    PRIMARY KEY (poll_id, ordinal)
);
```

`ordinal` (1, 2, 3…) — это и есть `option_id` из `api/openapi.yaml`, а не
отдельный суррогатный UUID. Причина: `option_id` — операнд поля хеша в
Redis (`HINCRBY poll:{id}:counts:{shard} {option_id} 1`) на горячем пути,
и лишние байты там платятся на каждый голос, а не один раз. У опций нет
собственной жизни вне опроса (на них никто не ссылается извне), так что
глобальная уникальность UUID здесь не нужна — только уникальность внутри
опроса, которую и даёт составной PK.

Инвариант «`ordinal` идут подряд 1..N без дыр» не оформлен как DB-констрейнт
(это потребовало бы триггера ради множества из ≤50 строк) — он гарантирован
тем, что опции вставляются один раз, одной транзакцией, при создании опроса,
и `05-api-contract.md` уже исключил `PATCH`/`DELETE` для опроса. Раз опции
неизменяемы после создания, а создаются только одним кодовым путём — дыры
взяться неоткуда.

### 1.3 `poll_result_snapshots`

```sql
CREATE TABLE poll_result_snapshots (
    poll_id                 UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    snapshot_at             TIMESTAMPTZ NOT NULL,
    total_accepted          BIGINT NOT NULL,
    rejected_duplicate      BIGINT NOT NULL DEFAULT 0,
    rejected_rate_limited   BIGINT NOT NULL DEFAULT 0,
    option_counts           JSONB NOT NULL,       -- {"1": 12345, "2": 6789}
    sampling_enabled        BOOLEAN NOT NULL DEFAULT false,
    sampling_rate           INTEGER,              -- K в "1 из K", NULL если выключено
    PRIMARY KEY (poll_id, snapshot_at)
);
```

Пишет воркер раз в секунду, пока опрос открыт (`01-stack.md`,
`02-load-model.md` §6.6). Значения — не дельты, а текущее состояние
Redis-счётчиков на момент снятия: `total_accepted` и `option_counts` растут
монотонно от снапшота к снапшоту, ровно как растут сами Redis-счётчики.

**Почему `option_counts` — `JSONB`, а не отдельная таблица
`(poll_id, snapshot_at, option_ordinal, count)`.** Нормализованный вариант
дал бы до 50 строк на каждый снапшот × ~300–600 снапшотов на опрос — не
проблема для Postgres по объёму (см. §3), но это усложнило бы и запись
(вставка пачкой вместо одной строки раз в секунду), и чтение для
`/results/timeseries` (нужна агрегация обратно в массив вместо одной строки
на точку). Одна JSONB-колонка ровно повторяет форму `ResultsSnapshotPoint`
из `api/openapi.yaml` — воркер пишет одну строку в секунду, а не пачку.
`label` в снапшоте не хранится — он не меняется (опрос неизменяем после
публикации) и подтягивается из `poll_options` при чтении.

`sampling_enabled`/`sampling_rate` — на уровне снапшота, а не опроса,
потому что деградация уровня 2 (`02-load-model.md` §6.7) — это состояние,
которое может включиться и выключиться посреди эфира; фиксировать его как
свойство опроса целиком стёрло бы, когда именно это произошло. Активация
самого режима — механизм оперативного реагирования на инцидент, а не часть
обычного админского флоу, и в `api/openapi.yaml` намеренно не заведён под
неё эндпоинт — это открытый пункт, см. §6.

Составной PK `(poll_id, snapshot_at)` покрывает оба паттерна чтения без
дополнительного индекса: «последний снапшот» (`ORDER BY snapshot_at DESC
LIMIT 1`) для `/results` и «вся история» (`ORDER BY snapshot_at ASC`) для
`/results/timeseries`.

### 1.4 `admin_audit_log`

```sql
CREATE TABLE admin_audit_log (
    id            BIGSERIAL PRIMARY KEY,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    action        TEXT NOT NULL,          -- 'poll.create' | 'poll.transition' | 'warmup.trigger'
    poll_id       UUID REFERENCES polls(id),
    detail        JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX admin_audit_log_poll_id_idx ON admin_audit_log (poll_id);
```

`poll_id` нужен nullable — не каждое админское действие привязано к опросу
(`/internal/warmup` без `poll_id` в теле). Ценность этой таблицы — именно
**история** переходов: `polls` хранит только текущее и последнее значения
таймстемпов, а `detail` при `action='poll.transition'` хранит запрошенный
переход (`{"from": "scheduled", "to": "open"}`) — то, что из самой таблицы
`polls` после следующего перехода восстановить уже нельзя.

---

## 2. Redis

Сводит воедино то, что уже зафиксировано в `01-stack.md`,
`02-load-model.md` и `03-deduplication.md`, плюс закрывает пробел из
начала документа.

| Ключ | Тип | Пишет | Назначение |
|---|---|---|---|
| `poll:{id}:meta` | HASH | приложение, при каждом переходе состояния | зеркало `state`/`closes_at`/`option_count` из Postgres — чтобы Lua-скрипту голосования не нужен был Postgres (см. §2.3) |
| `poll:{id}:counts:{0..N-1}` | HASH | Lua-скрипт голосования | шардированные счётчики голосов, `01-stack.md`/`02-load-model.md` §4.3 |
| `poll:{id}:rejected:{0..N-1}` | HASH, поля `duplicate`/`rate_limited` | Lua-скрипты голосования и rate-limit | источник `PollResults.rejected` в `api/openapi.yaml` |
| `d:{poll_id}:{nonce}` | STRING, `NX EX` | Lua-скрипт голосования | dedup-ключ, `03-deduplication.md` |
| `rl:{poll_id}:{ip}` | см. §2.4 | Lua-скрипт rate-limit | анти-флуд, `03-deduplication.md` §3 |

### 2.1 `poll:{id}:meta`

```
HSET poll:{id}:meta state open closes_at 1737295500 option_count 4
```

- `state` — строка, зеркало `polls.state`.
- `closes_at` — unix-время в секундах (не `TIMESTAMPTZ`-строка — сравнение
  чисел в Lua дешевле парсинга времени).
- `option_count` — используется для валидации `option_id` без похода в
  Postgres (см. §2.3); корректно, потому что `ordinal` в `poll_options`
  гарантированно 1..N без дыр (§1.2).

Без строгого TTL — в отличие от dedup-ключей (которых миллионы, и они
обязаны истекать, чтобы не раздувать память, `02-load-model.md` §4.4), это
один ключ на опрос, и не жалко просто дать ему TTL с большим запасом
(`closes_at` + сутки) или не ставить вовсе и полагаться на то, что `state`
внутри него скажет «closed».

### 2.2 Шардирование счётчиков

Без изменений относительно `02-load-model.md` §4.3 — случайный шард на
инкремент, сумма по `N` шардов на чтение. `poll:{id}:rejected:{shard}`
шардируется по той же причине и тем же числом шардов: во время скриптового
флуда объём **отклонённых** попыток может доминировать над объёмом принятых
голосов, так что это тот же single-key риск, что и у counts.

Важно: не все причины отказа шардируются в этот ключ. `rate_limited`
отклоняется ещё до вызова скрипта голосования — отдельным
rate-limit-скриптом (§2.4), который сам инкрементит
`poll:{id}:rejected:{shard}` полем `rate_limited`. `closed` (голосование
вне окна) в Redis вообще не пишется — см. §2.3, почему.

### 2.3 Lua-скрипт голосования — обновлённая версия

```lua
-- KEYS[1] = poll:{id}:meta
-- KEYS[2] = d:{poll_id}:{nonce}
-- KEYS[3] = poll:{id}:counts:{shard}
-- KEYS[4] = poll:{id}:rejected:{shard}
-- ARGV[1] = option_id
-- ARGV[2] = now (unix seconds)
-- ARGV[3] = dedup TTL (seconds)

local state        = redis.call('HGET', KEYS[1], 'state')
local closes_at     = tonumber(redis.call('HGET', KEYS[1], 'closes_at'))
local option_count  = tonumber(redis.call('HGET', KEYS[1], 'option_count'))

if state ~= 'open' or not closes_at or tonumber(ARGV[2]) > closes_at then
    return 'closed'          -- нет записи в Redis: см. ниже
end

local option_id = tonumber(ARGV[1])
if not option_id or option_id < 1 or option_id > option_count then
    return 'invalid_option'
end

if redis.call('SET', KEYS[2], '1', 'NX', 'EX', ARGV[3]) then
    redis.call('HINCRBY', KEYS[3], ARGV[1], 1)
    return 'accepted'
end

redis.call('HINCRBY', KEYS[4], 'duplicate', 1)
return 'duplicate'
```

Два уточнения к тому, что было в `01-stack.md` («один `EVALSHA` содержит
две команды»):

1. Команд внутри скрипта теперь до пяти (два `HGET`, `SET NX`, `HINCRBY`),
   не две — но это по-прежнему **один round-trip** с точки зрения клиента,
   а именно round-trip был предметом бюджета латентности в
   `02-load-model.md` §4.2-4.3, не число команд внутри Lua.
2. `state ~= 'open'` не пишет в `poll:{id}:rejected:{shard}`. У «слишком
   рано» / «слишком поздно» нет продуктовой ценности как сигнал накрутки
   (`01-stack.md` объясняет, зачем нужен именно `duplicate` — резкий рост
   его доли сигнализирует накрутку; попытки проголосовать до открытия или
   после закрытия таким сигналом не являются). Это событие остаётся видно
   только в Prometheus (`votes_rejected_total{reason="closed"}`,
   инкрементируется в Go-коде по возврату скрипта) — что уже достаточно для
   мониторинга и не требует персистентности в Postgres через снапшоты.

### 2.4 Rate-limit ключи

Отдельный Lua-скрипт (скользящее окно), ключ `rl:{poll_id}:{ip}`, порог из
`03-deduplication.md` §3.2 (черновой, 60/10с). Привязка к `poll_id`, а не
только к `ip`, — состояние лимитера не переживает конкретный опрос и не
нужно отдельно чистить между эфирами; проверяется тем же скриптом на
`/token` и на `/votes`.

Точная структура (ZSET с таймстемпами vs счётчик с фиксированными бакетами)
— деталь реализации самого лимитера, не хранилища данных; выбор между ними
не меняет ни одну из таблиц или ключей выше, поэтому не фиксируется в этом
документе.

---

## 3. Переход состояний: порядок записи Postgres ↔ Redis

Postgres и Redis не транзакционны друг относительно друга — при переходе
состояния пишем в оба, и порядок записи имеет значение, причём **разный для
разных переходов**:

| Переход | Порядок | Почему |
|---|---|---|
| → `open` | сначала Postgres, потом Redis | Если Redis-запись не удалась, `poll:meta` остаётся в состоянии «не open» → скрипт голосования fail-closed, опрос просто ещё не начал принимать голоса. Безопасная сторона ошибки — задержка старта, не её отсутствие. |
| → `closed` | сначала Redis, потом Postgres | Если Postgres-запись не удалась, а Redis уже обновлён — голосование уже остановлено немедленно, Postgres дописывается ретраем. Обратный порядок оставил бы окно, где опрос уже закрыт в Postgres, но Redis всё ещё принимает голоса. |

Правило одной строкой: **опасная сторона рассинхрона — не «поздно закрыли»,
а «случайно приняли голос не в то время»**; поэтому для закрытия сначала
останавливаем приём, а для открытия — сначала фиксируем durable-факт, что
разрешение дано.

`/internal/warmup` (`05-api-contract.md`) естественным образом дублирует
роль реконсиляции: раз он уже вызывается перед эфиром, ничего не мешает ему
дополнительно перечитать состояние опроса из Postgres и перезаписать
`poll:{id}:meta`, подстраховывая обычный path записи при переходе — без
отдельного фонового job'а под эту же задачу.

---

## 4. Масштаб — почему тюнинг Postgres не нужен

Проверка утверждения из `01-stack.md`/`02-load-model.md` §4.6 на
конкретных числах:

| Таблица | Строк на один опрос | Частота записи |
|---|---|---|
| `polls` | 1 | раз на опрос |
| `poll_options` | ≤50 | раз на опрос, одной транзакцией |
| `poll_result_snapshots` | ~300–600 (окно 5 мин, снапшот раз в секунду) | 1 INSERT/сек, только пока опрос открыт |
| `admin_audit_log` | единицы | на каждое админское действие |

Даже при 1000 опросов за время жизни продукта — это ~600К строк в
`poll_result_snapshots`, тривиально для Postgres без партиционирования и
дополнительных индексов. Утверждение «Postgres не узкое место» из
`02-load-model.md` §4.6 подтверждено на уровне конкретной схемы, а не
только качественно.

---

## 5. Что сознательно не делаем

| Что | Почему нет |
|---|---|
| Партиционирование `poll_result_snapshots` по времени | §4 — счёт на сотни тысяч строк, не миллиарды. |
| `ENUM` для `state` | Одна ALTER TYPE ради 4 значений, которые вряд ли изменятся — не окупается. |
| Отдельная таблица под опции вместо JSONB в снапшотах | Обсуждено в §1.3 — усложняет и запись, и чтение таймсерии ради нормализации, которая здесь не нужна (снапшот и так один агрегат на момент времени). |
| `actor`/`created_by` в audit log | Один статический admin-токен, различать некого (`01-stack.md`). |
| DB-констрейнт на непрерывность `ordinal` в `poll_options` | Гарантировано конструкцией (один код-путь создания, без edit), см. §1.2. |

---

## 6. Открыто для реализации

- **Механизм включения sampling (деградация уровня 2).** Схема данных под
  него готова (`sampling_enabled`/`sampling_rate` в снапшотах,
  зарезервированное поле в `poll:meta`), но кто и как его включает — не
  решено. Скорее всего не обычный админский CRUD-эндпоинт, а
  operational-рычаг на случай инцидента (внутренний эндпоинт или прямое
  вмешательство через `redis-cli` по runbook) — сознательно не проектируется
  сейчас вместе с остальным API.
- Точная реализация `rl:{poll_id}:{ip}` (ZSET-скользящее-окно против
  фиксированных бакетов) — деталь rate-лимитера, не схемы данных.
- Число шардов `N` для `counts`/`rejected` — параметр конфигурации,
  подбирается k6-замером (`02-load-model.md` §7), не часть схемы.
