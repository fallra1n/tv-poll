import { CheckCircle2, Clock3, LoaderCircle, Radio, RotateCcw, ShieldCheck, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter, CardHeader } from "@/components/ui/card";
import { ApiError, ApiTimeoutError } from "@/lib/api/http";
import { castVote, getPublicPoll, isVoteRejected } from "@/lib/api/public-client";
import type { PollPublic } from "@/lib/api/types";
import { formatCountdown, formatDateTime, getPollPhase, type PollPhase } from "@/lib/dates";
import { cn } from "@/lib/utils";
import { getPollIdFromPath } from "@/public/route";
import {
  getOrIssueVoteToken,
  getStoredVoteOutcome,
  markVoteOutcome,
  resetVoteToken,
  type VoteOutcome,
} from "@/public/vote-token";

type PollLoadState =
  | { status: "loading" }
  | { status: "ready"; poll: PollPublic }
  | { status: "not-found" }
  | { status: "error"; message: string };

type VoteState =
  | { status: "preparing" }
  | { status: "ready" }
  | { status: "submitting" }
  | { status: "accepted" }
  | { status: "duplicate"; optionId: number | null }
  | { status: "unavailable" }
  | { status: "server-closed" }
  | { status: "rate-limited"; retryAt: number }
  | { status: "error"; message: string };

// A 409 reason=closed can legitimately mean "scheduled -> open just hasn't
// landed on this instance yet" (06-frontend.md §7) — a single occurrence
// stays a retryable "unavailable". Two in a row past the device's own
// open/closed guess is treated as the server's word: the poll really did
// close (e.g. an admin ended it early), and "try again" would only mislead.
const consecutiveClosedRejectionsBeforeAuthoritative = 2;

function initialVoteState(pollId: string): VoteState {
  const outcome = getStoredVoteOutcome(pollId);

  if (outcome === "duplicate") {
    // Only the generic outcome is persisted (markVoteOutcome deliberately
    // stores no option), so a duplicate restored on a later visit has no
    // known option — only a duplicate discovered live, this session, can
    // name it (see the 409 handler in submitVote).
    return { status: "duplicate", optionId: null };
  }

  return outcome ? { status: outcome } : { status: "preparing" };
}

function retryDeadline(error: ApiError) {
  const fallbackWithJitter = 2 + Math.floor(Math.random() * 4);
  return Date.now() + (error.retryAfterSeconds ?? fallbackWithJitter) * 1000;
}

function describeError(error: unknown) {
  if (error instanceof ApiTimeoutError) {
    return "Сервис не ответил вовремя. Попробуйте ещё раз.";
  }

  if (error instanceof ApiError) {
    if (error.status === 503) {
      return "Сервис голосования временно недоступен. Попробуйте ещё раз.";
    }

    return error.message;
  }

  return "Не удалось связаться с сервисом. Проверьте интернет и попробуйте ещё раз.";
}

export function PollPage() {
  const pollId = getPollIdFromPath(window.location.pathname);

  if (!pollId) {
    return <PublicMessage title="Некорректная ссылка" message="Проверьте адрес из QR-кода и откройте его снова." />;
  }

  return <LoadedPollPage pollId={pollId} />;
}

function LoadedPollPage({ pollId }: { pollId: string }) {
  const [loadState, setLoadState] = useState<PollLoadState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    getPublicPoll(pollId, controller.signal)
      .then((poll) => {
        setLoadState({ status: "ready", poll });
        return poll;
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) {
          return;
        }

        if (error instanceof ApiError && error.status === 404) {
          setLoadState({ status: "not-found" });
          return;
        }

        setLoadState({ status: "error", message: describeError(error) });
      });

    return () => {
      controller.abort();
    };
  }, [pollId]);

  if (loadState.status === "loading") {
    return <PublicLoading />;
  }

  if (loadState.status === "not-found") {
    return <PublicMessage title="Опрос не найден" message="Возможно, он ещё не опубликован или ссылка устарела." reload />;
  }

  if (loadState.status === "error") {
    return <PublicMessage title="Не удалось загрузить опрос" message={loadState.message} reload />;
  }

  return <VotingCard poll={loadState.poll} />;
}

function VotingCard({ poll }: { poll: PollPublic }) {
  const [now, setNow] = useState(() => Date.now());
  const [selectedOption, setSelectedOption] = useState<number | null>(null);
  const [voteState, setVoteState] = useState<VoteState>(() => initialVoteState(poll.id));
  const tokenRef = useRef<string | null>(null);
  const closedRejectionCountRef = useRef(0);
  let phase: PollPhase | null = null;

  try {
    phase = getPollPhase(poll, now);
  } catch {
    phase = null;
  }

  const terminalVote = voteState.status === "accepted" || voteState.status === "duplicate" ? voteState : null;
  const terminalOutcome: VoteOutcome | null = terminalVote?.status ?? null;

  useEffect(() => {
    if (phase === null || phase === "closed" || terminalOutcome) {
      return;
    }

    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        setNow(Date.now());
      }
    }, 1000);

    return () => {
      window.clearInterval(timer);
    };
  }, [phase, terminalOutcome]);

  useEffect(() => {
    if (phase === null || phase === "closed" || voteState.status !== "preparing") {
      return;
    }

    let active = true;

    getOrIssueVoteToken(poll.id)
      .then((token) => {
        if (active) {
          tokenRef.current = token;
          setVoteState((current) => current.status === "preparing" ? { status: "ready" } : current);
        }
        return token;
      })
      .catch((error: unknown) => {
        if (!active) {
          return;
        }

        if (error instanceof ApiError && error.status === 429) {
          setVoteState({ status: "rate-limited", retryAt: retryDeadline(error) });
          return;
        }

        setVoteState({ status: "error", message: describeError(error) });
      });

    return () => {
      active = false;
    };
  }, [phase, poll.id, voteState.status]);

  if (phase === null) {
    return <PublicMessage title="Некорректный опрос" message="У опроса неверно задано время голосования." />;
  }

  async function submitVote() {
    if (selectedOption === null || phase !== "open" || voteState.status === "submitting") {
      return;
    }

    if (voteState.status === "rate-limited" && now < voteState.retryAt) {
      return;
    }

    setVoteState({ status: "submitting" });

    try {
      const token = tokenRef.current ?? await getOrIssueVoteToken(poll.id);
      tokenRef.current = token;
      await castVote(poll.id, selectedOption, token);
      markVoteOutcome(poll.id, "accepted", poll.closes_at);
      setVoteState({ status: "accepted" });
    } catch (error: unknown) {
      if (error instanceof ApiError && error.status === 409 && isVoteRejected(error.body)) {
        if (error.body.reason === "duplicate") {
          markVoteOutcome(poll.id, "duplicate", poll.closes_at);
          setVoteState({ status: "duplicate", optionId: error.body.option_id ?? null });
        } else {
          closedRejectionCountRef.current += 1;
          setVoteState(closedRejectionCountRef.current >= consecutiveClosedRejectionsBeforeAuthoritative
            ? { status: "server-closed" }
            : { status: "unavailable" });
        }
        return;
      }

      if (error instanceof ApiError && error.status === 429) {
        setVoteState({ status: "rate-limited", retryAt: retryDeadline(error) });
        return;
      }

      if (error instanceof ApiError && error.status === 401) {
        resetVoteToken(poll.id);
        tokenRef.current = null;

        try {
          tokenRef.current = await getOrIssueVoteToken(poll.id);
          setVoteState({ status: "error", message: "Сессия обновлена. Отправьте выбранный ответ ещё раз." });
        } catch (refreshError: unknown) {
          if (refreshError instanceof ApiError && refreshError.status === 429) {
            setVoteState({ status: "rate-limited", retryAt: retryDeadline(refreshError) });
          } else {
            setVoteState({ status: "error", message: describeError(refreshError) });
          }
        }
        return;
      }

      setVoteState({ status: "error", message: describeError(error) });
    }
  }

  if (terminalVote) {
    const countedOptionLabel = terminalVote.status === "duplicate" && terminalVote.optionId !== null
      ? poll.options.find((option) => option.id === terminalVote.optionId)?.label ?? null
      : null;
    return <VoteComplete outcome={terminalVote.status} countedOptionLabel={countedOptionLabel} />;
  }

  if (phase === "closed" || voteState.status === "server-closed") {
    return (
      <PublicMessage
        icon={<Clock3 />}
        title="Голосование завершено"
        message={`Приём ответов закончился ${formatDateTime(poll.closes_at)}.`}
      />
    );
  }

  const retrySeconds = voteState.status === "rate-limited"
    ? Math.max(0, Math.ceil((voteState.retryAt - now) / 1000))
    : 0;
  const buttonDisabled = selectedOption === null
    || phase !== "open"
    || voteState.status === "submitting"
    || voteState.status === "preparing"
    || retrySeconds > 0;

  return (
    <PublicShell>
      <Card className="w-full max-w-xl" aria-busy={voteState.status === "submitting"}>
        <CardHeader className="gap-3">
          <div className="flex items-center justify-between gap-4 text-xs font-medium tracking-wide text-muted-foreground uppercase">
            <span className="inline-flex items-center gap-2"><Radio className="size-4" /> Анонимный опрос</span>
            <PhaseLabel phase={phase} poll={poll} now={now} />
          </div>
          <h1 className="text-balance text-2xl leading-tight font-semibold sm:text-3xl">{poll.question}</h1>
          <p className="text-sm text-muted-foreground">Выберите один вариант. Изменить ответ после отправки нельзя.</p>
        </CardHeader>
        <CardContent>
          <fieldset className="grid gap-3" disabled={voteState.status === "submitting"}>
            <legend className="sr-only">Варианты ответа</legend>
            {poll.options.map((option) => (
              <label
                key={option.id}
                className={cn(
                  "flex min-h-14 cursor-pointer items-center gap-3 rounded-lg border bg-background px-4 py-3 text-base transition-colors hover:bg-muted/70 has-[:focus-visible]:ring-3 has-[:focus-visible]:ring-ring/50",
                  selectedOption === option.id && "border-foreground bg-muted ring-1 ring-foreground",
                )}
              >
                <input
                  className="size-4 shrink-0 accent-foreground"
                  type="radio"
                  name="poll-option"
                  value={option.id}
                  checked={selectedOption === option.id}
                  onChange={() => setSelectedOption(option.id)}
                />
                <span>{option.label}</span>
              </label>
            ))}
          </fieldset>

          <VoteStatus state={voteState} phase={phase} retrySeconds={retrySeconds} />
        </CardContent>
        <CardFooter className="flex-col gap-3">
          <Button className="w-full" size="lg" disabled={buttonDisabled} onClick={submitVote}>
            {voteState.status === "submitting" && <LoaderCircle className="animate-spin" />}
            {voteState.status === "submitting"
              ? "Отправляем…"
              : retrySeconds > 0 ? `Повторить через ${retrySeconds} сек` : "Отправить ответ"}
          </Button>
          <p className="flex items-center justify-center gap-2 text-center text-xs text-muted-foreground">
            <ShieldCheck className="size-4" /> Регистрация не требуется. Ответ учитывается только в агрегированных результатах.
          </p>
        </CardFooter>
      </Card>
    </PublicShell>
  );
}

function PhaseLabel({ phase, poll, now }: { phase: PollPhase; poll: PollPublic; now: number }) {
  if (phase === "scheduled") {
    return <span>Старт через {formatCountdown(poll.opens_at, now)}</span>;
  }

  return <span>Осталось {formatCountdown(poll.closes_at, now)}</span>;
}

function VoteStatus({ state, phase, retrySeconds }: { state: VoteState; phase: PollPhase; retrySeconds: number }) {
  if (phase === "scheduled") {
    return <InlineStatus icon={<Clock3 />} message="Ответ можно выбрать сейчас и отправить после начала голосования." />;
  }

  if (state.status === "preparing") {
    return <InlineStatus icon={<LoaderCircle className="animate-spin" />} message="Подготавливаем защищённую сессию голосования…" />;
  }

  if (state.status === "rate-limited" && retrySeconds > 0) {
    return <InlineStatus tone="warning" icon={<TriangleAlert />} message={`Слишком много запросов. Повтор станет доступен через ${retrySeconds} сек.`} />;
  }

  if (state.status === "unavailable") {
    return <InlineStatus tone="warning" icon={<RotateCcw />} message="Голосование пока недоступно. Можно попробовать отправить ответ ещё раз." />;
  }

  if (state.status === "error") {
    return <InlineStatus tone="error" icon={<TriangleAlert />} message={state.message} />;
  }

  return null;
}

function InlineStatus({ icon, message, tone = "neutral" }: { icon: React.ReactNode; message: string; tone?: "neutral" | "warning" | "error" }) {
  return (
    <div
      role={tone === "error" ? "alert" : "status"}
      className={cn(
        "mt-1 flex items-start gap-2 rounded-md bg-muted px-3 py-2.5 text-sm text-muted-foreground",
        tone === "warning" && "bg-amber-50 text-amber-900",
        tone === "error" && "bg-red-50 text-red-800",
      )}
    >
      <span className="mt-0.5 shrink-0 [&>svg]:size-4">{icon}</span>
      <span>{message}</span>
    </div>
  );
}

function VoteComplete({ outcome, countedOptionLabel }: { outcome: VoteOutcome; countedOptionLabel: string | null }) {
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    headingRef.current?.focus();
  }, []);

  return (
    <PublicShell>
      <Card className="w-full max-w-lg text-center">
        <CardContent className="items-center gap-4 py-8">
          <CheckCircle2 className="size-12" strokeWidth={1.5} />
          <h1 ref={headingRef} tabIndex={-1} className="text-2xl font-semibold outline-none">{outcome === "accepted" ? "Ответ принят" : "Ваш голос уже учтён"}</h1>
          <p className="max-w-sm text-muted-foreground">
            {outcome === "accepted"
              ? "Спасибо за участие. Сервис сохраняет только агрегированные результаты."
              : countedOptionLabel
                ? `Учтён вариант «${countedOptionLabel}». Повторно голосовать с этого устройства не нужно.`
                : "Повторно голосовать с этого устройства не нужно."}
          </p>
        </CardContent>
      </Card>
    </PublicShell>
  );
}

function PublicLoading() {
  return (
    <PublicShell>
      <output className="block w-full max-w-xl" aria-label="Загрузка опроса">
        <Card>
          <CardContent className="gap-4 py-8">
            <div className="h-4 w-32 animate-pulse rounded bg-muted" />
            <div className="h-8 w-4/5 animate-pulse rounded bg-muted" />
            <div className="h-14 animate-pulse rounded-lg bg-muted" />
            <div className="h-14 animate-pulse rounded-lg bg-muted" />
          </CardContent>
        </Card>
      </output>
    </PublicShell>
  );
}

function PublicMessage({
  title,
  message,
  icon = <TriangleAlert />,
  reload = false,
}: {
  title: string;
  message: string;
  icon?: React.ReactNode;
  reload?: boolean;
}) {
  return (
    <PublicShell>
      <Card className="w-full max-w-lg text-center">
        <CardContent className="items-center gap-4 py-8">
          <span className="[&>svg]:size-10">{icon}</span>
          <h1 className="text-2xl font-semibold">{title}</h1>
          <p className="max-w-sm text-muted-foreground">{message}</p>
          {reload && <Button variant="outline" onClick={() => window.location.reload()}>Попробовать снова</Button>}
        </CardContent>
      </Card>
    </PublicShell>
  );
}

function PublicShell({ children }: { children: React.ReactNode }) {
  return (
    <main className="grid min-h-dvh place-items-center px-4 py-8 sm:px-6">
      {children}
    </main>
  );
}
