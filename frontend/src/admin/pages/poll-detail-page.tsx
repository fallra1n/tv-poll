import { ArrowLeft, Check, Clipboard, Clock3, ExternalLink, LoaderCircle, Play, Square } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";

import { useAdminAuth } from "@/admin/auth";
import { AdminInlineError } from "@/admin/pages/poll-list-page";
import { PollStateBadge } from "@/admin/poll-state";
import { ResultsPanel } from "@/admin/results-panel";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { getAdminPoll, transitionPoll } from "@/lib/api/admin-client";
import { ApiError } from "@/lib/api/http";
import type { PollAdmin, PollTransitionRequest } from "@/lib/api/types";
import { formatDateTime, localDateTimeToIso, toDateTimeLocalValue } from "@/lib/dates";
import { cn } from "@/lib/utils";
import { isUuid } from "@/lib/uuid";

type DetailState =
  | { status: "loading" }
  | { status: "ready"; poll: PollAdmin }
  | { status: "not-found" }
  | { status: "error"; message: string };

type Confirmation = "open" | "closed" | null;

export function PollDetailPage() {
  const { pollId } = useParams();
  const validPollId = pollId && isUuid(pollId) ? pollId : null;
  const { token, logout } = useAdminAuth();
  const [detail, setDetail] = useState<DetailState>({ status: "loading" });
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const [transitionError, setTransitionError] = useState<string | null>(null);
  const [transitioning, setTransitioning] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation>(null);
  const [scheduledAt, setScheduledAt] = useState("");
  const [scheduleError, setScheduleError] = useState<string | null>(null);
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">("idle");

  useEffect(() => {
    if (!validPollId || !token || transitioning) {
      return;
    }

    const adminToken = token;
    const currentPollId = validPollId;
    let stopped = false;
    let inFlight = false;
    let keepPolling = true;
    let timer: number | null = null;
    let controller: AbortController | null = null;

    function scheduleNextLoad() {
      if (!stopped && keepPolling && document.visibilityState === "visible") {
        timer = window.setTimeout(() => {
          void load();
        }, 3000);
      }
    }

    async function load() {
      if (stopped || inFlight || document.visibilityState === "hidden") {
        return;
      }

      inFlight = true;
      controller = new AbortController();

      try {
        const poll = await getAdminPoll(adminToken, currentPollId, controller.signal);
        if (!stopped) {
          setDetail({ status: "ready", poll });
          setRefreshError(null);
          keepPolling = poll.state !== "closed";
          if (poll.state === "draft") {
            setScheduledAt((current) => current || toDateTimeLocalValue(new Date(Date.now() + 5 * 60_000)));
          }
        }
      } catch (error: unknown) {
        if (stopped || controller.signal.aborted) {
          return;
        }

        if (error instanceof ApiError && error.status === 401) {
          stopped = true;
          logout("Токен недействителен. Введите актуальный токен администратора.");
          return;
        }

        if (error instanceof ApiError && error.status === 404) {
          setDetail({ status: "not-found" });
          return;
        }

        const message = error instanceof ApiError ? error.message : "Не удалось загрузить опрос.";
        let wasReady = false;
        setDetail((current) => {
          wasReady = current.status === "ready";
          return wasReady ? current : { status: "error", message };
        });
        // A poll being watched live during a broadcast must never look
        // frozen with no explanation — surface the failure without
        // discarding the last good data (mirrors ResultsPanel's error banner).
        if (wasReady) {
          setRefreshError(message);
        }
      } finally {
        inFlight = false;
        scheduleNextLoad();
      }
    }

    function handleVisibilityChange() {
      if (document.visibilityState === "visible") {
        if (timer !== null) {
          window.clearTimeout(timer);
          timer = null;
        }
        void load();
      } else if (timer !== null) {
        window.clearTimeout(timer);
        timer = null;
      }
    }

    document.addEventListener("visibilitychange", handleVisibilityChange);
    void load();

    return () => {
      stopped = true;
      controller?.abort();
      if (timer !== null) {
        window.clearTimeout(timer);
      }
      document.removeEventListener("visibilitychange", handleVisibilityChange);
    };
  }, [logout, token, transitioning, validPollId]);

  async function performTransition(request: PollTransitionRequest) {
    if (!validPollId || !token || detail.status !== "ready") {
      return;
    }

    setTransitioning(true);
    setTransitionError(null);

    try {
      const poll = await transitionPoll(token, validPollId, request);
      setDetail({ status: "ready", poll });
      setRefreshError(null);
      setConfirmation(null);
    } catch (error: unknown) {
      if (error instanceof ApiError && error.status === 401) {
        logout("Токен недействителен. Введите актуальный токен администратора.");
        return;
      }

      setTransitionError(error instanceof ApiError ? error.message : "Не удалось изменить состояние опроса.");
    } finally {
      setTransitioning(false);
    }
  }

  function schedule(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const isoDate = localDateTimeToIso(scheduledAt);

    if (!isoDate) {
      setScheduleError("Укажите дату и время эфира.");
      return;
    }

    setScheduleError(null);
    void performTransition({ to: "scheduled", scheduled_at: isoDate });
  }

  async function copyPublicLink(poll: PollAdmin) {
    const publicLink = `${window.location.origin}/polls/${poll.id}`;

    try {
      await navigator.clipboard.writeText(publicLink);
      setCopyState("copied");
    } catch {
      setCopyState("error");
    }

    window.setTimeout(() => setCopyState("idle"), 2000);
  }

  if (!validPollId) {
    return <AdminInlineError message="В адресе указан некорректный идентификатор опроса." />;
  }

  if (detail.status === "loading") {
    return <output className="block h-80 animate-pulse rounded-xl bg-muted" aria-label="Загрузка опроса" />;
  }

  if (detail.status === "not-found") {
    return <AdminInlineError message="Опрос не найден." />;
  }

  if (detail.status === "error") {
    return <AdminInlineError message={detail.message} />;
  }

  const poll = detail.poll;
  const publicLink = `${window.location.origin}/polls/${poll.id}`;

  return (
    <section className="grid gap-8">
      {refreshError && (
        <output className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          {refreshError} Показаны последние загруженные данные.
        </output>
      )}
      <div>
        <Link to="/admin" className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "-ml-2 mb-3")}>
          <ArrowLeft /> К списку
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="min-w-0">
            <PollStateBadge state={poll.state} />
            <h1 className="mt-3 max-w-3xl text-balance text-3xl font-semibold tracking-tight">{poll.question}</h1>
            <p className="mt-2 break-all font-mono text-xs text-muted-foreground">{poll.id}</p>
          </div>
          {poll.state !== "draft" && (
            <div className="flex gap-2">
              <Button variant="outline" onClick={() => void copyPublicLink(poll)}>
                {copyState === "copied" ? <Check /> : <Clipboard />}
                {copyState === "copied" ? "Скопировано" : "Копировать ссылку"}
              </Button>
              <a href={publicLink} target="_blank" rel="noreferrer" className={cn(buttonVariants({ variant: "outline", size: "icon" }))} aria-label="Открыть публичную страницу">
                <ExternalLink />
              </a>
            </div>
          )}
        </div>
        {copyState === "error" && <p role="alert" className="mt-2 text-sm text-destructive">Не удалось скопировать ссылку.</p>}
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <Card>
          <CardHeader>
            <h2 className="text-lg font-semibold">Варианты ответа</h2>
          </CardHeader>
          <CardContent>
            <ol className="grid gap-2">
              {poll.options.map((option) => (
                <li key={option.id} className="flex gap-3 rounded-lg bg-muted/60 px-4 py-3">
                  <span className="text-muted-foreground">{option.id}.</span>
                  <span>{option.label}</span>
                </li>
              ))}
            </ol>
          </CardContent>
        </Card>

        <Card className="h-fit">
          <CardHeader>
            <h2 className="text-lg font-semibold">Параметры</h2>
          </CardHeader>
          <CardContent>
            <dl>
              <Definition label="Создан" value={formatDateTime(poll.created_at)} />
              <Definition label="Эфир" value={poll.scheduled_at ? formatDateTime(poll.scheduled_at) : "Не запланирован"} />
              <Definition label="Закрытие" value={poll.closes_at ? formatDateTime(poll.closes_at) : "Не определено"} />
              <Definition label="Окно" value={`${poll.voting_window_seconds} сек`} />
            </dl>
          </CardContent>
        </Card>
      </div>

      <TransitionCard
        poll={poll}
        scheduledAt={scheduledAt}
        scheduleError={scheduleError}
        transitioning={transitioning}
        confirmation={confirmation}
        transitionError={transitionError}
        onScheduledAtChange={setScheduledAt}
        onSchedule={schedule}
        onConfirm={setConfirmation}
        onTransition={performTransition}
      />

      {(poll.state === "open" || poll.state === "closed") && <ResultsPanel key={poll.id} pollId={poll.id} pollState={poll.state} />}
    </section>
  );
}

function Definition({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid grid-cols-[6rem_1fr] gap-3 border-b py-2.5 last:border-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="text-right font-medium">{value}</dd>
    </div>
  );
}

function TransitionCard({
  poll,
  scheduledAt,
  scheduleError,
  transitioning,
  confirmation,
  transitionError,
  onScheduledAtChange,
  onSchedule,
  onConfirm,
  onTransition,
}: {
  poll: PollAdmin;
  scheduledAt: string;
  scheduleError: string | null;
  transitioning: boolean;
  confirmation: Confirmation;
  transitionError: string | null;
  onScheduledAtChange: (value: string) => void;
  onSchedule: (event: FormEvent<HTMLFormElement>) => void;
  onConfirm: (confirmation: Confirmation) => void;
  onTransition: (request: PollTransitionRequest) => Promise<void>;
}) {
  if (poll.state === "closed") {
    return null;
  }

  return (
    <Card>
      <CardHeader>
        <h2 className="text-lg font-semibold">Следующий этап</h2>
      </CardHeader>
      <CardContent>
        {poll.state === "draft" && (
          <form className="flex flex-wrap items-end gap-3" onSubmit={onSchedule}>
            <div className="grid min-w-64 flex-1 gap-2">
              <Label htmlFor="transition-scheduled-at">Дата и время эфира</Label>
              <Input
                id="transition-scheduled-at"
                type="datetime-local"
                value={scheduledAt}
                aria-invalid={Boolean(scheduleError)}
                aria-describedby={scheduleError ? "transition-schedule-error" : undefined}
                onChange={(event) => onScheduledAtChange(event.target.value)}
              />
              {scheduleError && <p id="transition-schedule-error" className="text-sm text-destructive">{scheduleError}</p>}
            </div>
            <Button type="submit" disabled={transitioning}>
              {transitioning ? <LoaderCircle className="animate-spin" /> : <Clock3 />}
              Запланировать
            </Button>
          </form>
        )}

        {poll.state === "scheduled" && confirmation !== "open" && (
          <div className="flex flex-wrap items-center justify-between gap-4">
            <p className="text-sm text-muted-foreground">Открытие разрешит принимать голоса через публичную страницу.</p>
            <Button onClick={() => onConfirm("open")}><Play /> Открыть голосование</Button>
          </div>
        )}

        {poll.state === "open" && confirmation !== "closed" && (
          <div className="flex flex-wrap items-center justify-between gap-4">
            <p className="text-sm text-muted-foreground">Досрочное завершение немедленно остановит приём новых голосов.</p>
            <Button variant="destructive" onClick={() => onConfirm("closed")}><Square /> Завершить голосование</Button>
          </div>
        )}

        {confirmation && (
          <div className="flex flex-wrap items-center justify-between gap-4 rounded-lg bg-muted p-4">
            <p className="font-medium">
              {confirmation === "open" ? "Открыть приём голосов сейчас?" : "Завершить приём голосов досрочно?"}
            </p>
            <div className="flex gap-2">
              <Button variant="ghost" disabled={transitioning} onClick={() => onConfirm(null)}>Отмена</Button>
              <Button
                variant={confirmation === "closed" ? "destructive" : "default"}
                disabled={transitioning}
                onClick={() => void onTransition({ to: confirmation })}
              >
                {transitioning && <LoaderCircle className="animate-spin" />}
                Подтвердить
              </Button>
            </div>
          </div>
        )}

        {transitionError && <AdminInlineError message={transitionError} />}
      </CardContent>
    </Card>
  );
}
