import { ArrowLeft, ArrowRight, CalendarClock, Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { useAdminAuth } from "@/admin/auth";
import { PollStateBadge } from "@/admin/poll-state";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { listPolls } from "@/lib/api/admin-client";
import { ApiError } from "@/lib/api/http";
import type { PollAdmin, PollState } from "@/lib/api/types";
import { formatDateTime } from "@/lib/dates";
import { cn } from "@/lib/utils";

const pageSize = 20;
const validStates = new Set<PollState>(["draft", "scheduled", "open", "closed"]);

type ListState =
  | { status: "loading" }
  | { status: "ready"; items: PollAdmin[]; total: number }
  | { status: "error"; message: string };

export function PollListPage() {
  const { token, logout } = useAdminAuth();
  const [searchParameters, setSearchParameters] = useSearchParams();
  const [state, setState] = useState<ListState>({ status: "loading" });
  const stateValue = searchParameters.get("state");
  const stateFilter = stateValue && validStates.has(stateValue as PollState) ? stateValue as PollState : undefined;
  const rawOffset = Number(searchParameters.get("offset") ?? 0);
  const offset = Number.isInteger(rawOffset) && rawOffset >= 0 ? rawOffset : 0;

  useEffect(() => {
    if (!token) {
      return;
    }

    const controller = new AbortController();

    listPolls(token, { state: stateFilter, limit: pageSize, offset }, controller.signal)
      .then((response) => {
        setState({ status: "ready", ...response });
        return response;
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) {
          return;
        }

        if (error instanceof ApiError && error.status === 401) {
          logout("Токен недействителен. Введите актуальный токен администратора.");
          return;
        }

        setState({
          status: "error",
          message: error instanceof ApiError ? error.message : "Не удалось загрузить список опросов.",
        });
      });

    return () => {
      controller.abort();
    };
  }, [logout, offset, stateFilter, token]);

  function updateFilter(nextState: string) {
    setState({ status: "loading" });
    const next = new URLSearchParams();
    if (validStates.has(nextState as PollState)) {
      next.set("state", nextState);
    }
    setSearchParameters(next);
  }

  function goToOffset(nextOffset: number) {
    setState({ status: "loading" });
    const next = new URLSearchParams(searchParameters);
    if (nextOffset > 0) {
      next.set("offset", String(nextOffset));
    } else {
      next.delete("offset");
    }
    setSearchParameters(next);
  }

  return (
    <section className="grid gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p className="text-sm text-muted-foreground">Администрирование</p>
          <h1 className="text-3xl font-semibold tracking-tight">Опросы</h1>
        </div>
        <Link to="/admin/polls/new" className={cn(buttonVariants({ size: "lg" }))}>
          <Plus /> Новый опрос
        </Link>
      </div>

      <div className="flex items-center gap-3">
        <label htmlFor="state-filter" className="text-sm font-medium">Состояние</label>
        <select
          id="state-filter"
          className="h-10 rounded-md border border-input bg-card px-3 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
          value={stateFilter ?? ""}
          onChange={(event) => updateFilter(event.target.value)}
        >
          <option value="">Все</option>
          <option value="draft">Черновики</option>
          <option value="scheduled">Запланированные</option>
          <option value="open">Открытые</option>
          <option value="closed">Завершённые</option>
        </select>
      </div>

      {state.status === "loading" && <PollListSkeleton />}
      {state.status === "error" && <AdminInlineError message={state.message} />}
      {state.status === "ready" && state.items.length === 0 && <EmptyPollList />}
      {state.status === "ready" && state.items.length > 0 && (
        <>
          <div className="grid gap-3">
            {state.items.map((poll) => <PollListItem key={poll.id} poll={poll} />)}
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
            <p className="text-sm text-muted-foreground">
              Показано {offset + 1}–{Math.min(offset + state.items.length, state.total)} из {state.total}
            </p>
            <div className="flex gap-2">
              <Button variant="outline" disabled={offset === 0} onClick={() => goToOffset(Math.max(0, offset - pageSize))}>
                <ArrowLeft /> Назад
              </Button>
              <Button variant="outline" disabled={offset + state.items.length >= state.total} onClick={() => goToOffset(offset + pageSize)}>
                Далее <ArrowRight />
              </Button>
            </div>
          </div>
        </>
      )}
    </section>
  );
}

function PollListItem({ poll }: { poll: PollAdmin }) {
  return (
    <Link to={`/admin/polls/${poll.id}`} className="group block rounded-xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50">
      <Card className="gap-3 py-4 transition-colors group-hover:bg-muted/40">
        <CardContent className="gap-3 px-4 sm:flex-row sm:items-center sm:justify-between sm:px-5">
          <div className="min-w-0">
            <h2 className="truncate text-base font-medium">{poll.question}</h2>
            <p className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
              <CalendarClock className="size-3.5" />
              {poll.scheduled_at ? formatDateTime(poll.scheduled_at) : `Создан ${formatDateTime(poll.created_at)}`}
            </p>
          </div>
          <PollStateBadge state={poll.state} />
        </CardContent>
      </Card>
    </Link>
  );
}

function PollListSkeleton() {
  return (
    <output className="grid gap-3" aria-label="Загрузка списка">
      {[1, 2, 3].map((item) => <div key={item} className="h-24 animate-pulse rounded-xl bg-muted" />)}
    </output>
  );
}

function EmptyPollList() {
  return (
    <Card>
      <CardContent className="items-center py-10 text-center">
        <h2 className="text-lg font-medium">Опросов пока нет</h2>
        <p className="text-muted-foreground">Создайте первый опрос или измените фильтр.</p>
      </CardContent>
    </Card>
  );
}

export function AdminInlineError({ message }: { message: string }) {
  return (
    <div role="alert" className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
      {message}
    </div>
  );
}
