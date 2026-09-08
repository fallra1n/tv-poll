import { Activity, AlertTriangle, Ban, Clock3, CopyX } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";

import { useAdminAuth } from "@/admin/auth";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { getPollResults, getPollResultsTimeseries } from "@/lib/api/admin-client";
import { ApiError } from "@/lib/api/http";
import type { PollResults, PollState, ResultsSnapshotPoint } from "@/lib/api/types";
import { formatDateTime, formatInteger } from "@/lib/dates";

type ResultsData = {
  results: PollResults;
  points: ResultsSnapshotPoint[];
};

const timeFormatter = new Intl.DateTimeFormat("ru-RU", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});

function resultsErrorMessage(error: unknown, fallback: string) {
  return error instanceof ApiError ? error.message : fallback;
}

export function ResultsPanel({ pollId, pollState }: { pollId: string; pollState: PollState }) {
  const { token, logout } = useAdminAuth();
  const [data, setData] = useState<ResultsData | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Captures only the first render's pollState. ResultsPanel only ever
  // mounts for "open" or "closed" (see poll-detail-page.tsx), and a poll
  // never goes closed -> open, so this stays a reliable answer to "did
  // this admin watch the poll while it was live" for the component's
  // whole lifetime (it isn't remounted on state change — same `key`).
  const observedOpenRef = useRef(pollState === "open");

  useEffect(() => {
    if (token === null) {
      return;
    }
    const adminToken: string = token;
    const refreshInterval = pollState === "open" ? 1000 : null;
    // A poll that was already closed before this admin opened its detail
    // page has no in-flight snapshot to catch up on — fetch once and stop,
    // instead of polling a result that can never change again.
    const catchUpAfterClose = observedOpenRef.current;

    let stopped = false;
    let inFlight = false;
    let timer: number | null = null;
    let controller: AbortController | null = null;
    let observedClosed = pollState === "closed";
    let completedClosedLoads = 0;

    function scheduleNextLoad() {
      const shouldLoadAgain = refreshInterval !== null
        || (catchUpAfterClose && (!observedClosed || completedClosedLoads < 3));
      if (!stopped && shouldLoadAgain && document.visibilityState === "visible") {
        timer = window.setTimeout(() => {
          void load();
        }, refreshInterval ?? 1000);
      }
    }

    async function load() {
      if (stopped || inFlight || document.visibilityState === "hidden") {
        return;
      }

      inFlight = true;
      controller = new AbortController();

      try {
        const [resultsResponse, timeseriesResponse] = await Promise.allSettled([
          getPollResults(adminToken, pollId, controller.signal),
          getPollResultsTimeseries(adminToken, pollId, controller.signal),
        ]);

        const unauthorized = [resultsResponse, timeseriesResponse].find((response) =>
          response.status === "rejected" && response.reason instanceof ApiError && response.reason.status === 401);
        if (unauthorized) {
          stopped = true;
          logout("Токен недействителен. Введите актуальный токен администратора.");
          return;
        }

        if (stopped) {
          return;
        }

        if (resultsResponse.status === "fulfilled") {
          observedClosed = resultsResponse.value.state === "closed";
          if (!observedClosed) {
            completedClosedLoads = 0;
          }

          setData((current) => ({
            results: resultsResponse.value,
            points: timeseriesResponse.status === "fulfilled" ? timeseriesResponse.value.points : current?.points ?? [],
          }));
          setError(timeseriesResponse.status === "rejected"
            ? resultsErrorMessage(timeseriesResponse.reason, "Результаты обновлены, но график временно недоступен.")
            : null);
        } else {
          if (timeseriesResponse.status === "fulfilled") {
            setData((current) => current ? { ...current, points: timeseriesResponse.value.points } : current);
          }
          setError(resultsErrorMessage(resultsResponse.reason, "Не удалось обновить результаты."));
        }
      } catch (loadError: unknown) {
        if (stopped || controller.signal.aborted) {
          return;
        }

        if (loadError instanceof ApiError && loadError.status === 401) {
          stopped = true;
          logout("Токен недействителен. Введите актуальный токен администратора.");
          return;
        }

        setError(loadError instanceof ApiError ? loadError.message : "Не удалось обновить результаты.");
      } finally {
        inFlight = false;
        if (observedClosed) {
          completedClosedLoads += 1;
        }
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
  }, [logout, pollId, pollState, token]);

  if (!data && !error) {
    return <output className="block h-56 animate-pulse rounded-xl bg-muted" aria-label="Загрузка результатов" />;
  }

  if (!data) {
    return <div role="alert" className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">{error}</div>;
  }

  const { results, points } = data;
  const chartData = points.map((point) => ({
    time: timeFormatter.format(new Date(point.at)),
    votes: point.total_accepted,
  }));

  return (
    <section aria-labelledby="results-title" className="grid gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="results-title" className="text-2xl font-semibold">Результаты</h2>
          <p className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
            <Clock3 className="size-3.5" />
            {results.snapshot_at ? `Снимок на ${formatDateTime(results.snapshot_at)}` : "Первый снимок ещё не готов"}
          </p>
        </div>
        <p className="text-right">
          <span className="block text-3xl font-semibold tabular-nums">{formatInteger(results.total_accepted)}</span>
          <span className="text-xs text-muted-foreground">принято голосов</span>
        </p>
      </div>

      {error && <output className="rounded-lg bg-amber-50 px-4 py-3 text-sm text-amber-900">{error} Показан последний успешный снимок.</output>}

      {results.sampling.enabled && (
        <output className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-950">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>
            Для этого опроса применялось сэмплирование
            {results.sampling.rate ? ` 1 из ${results.sampling.rate}` : ""}. Счётчики являются оценочными.
          </span>
        </output>
      )}

      <Card>
        <CardHeader>
          <h3 className="text-base font-semibold">Распределение ответов</h3>
        </CardHeader>
        <CardContent className="gap-5">
          {results.options.map((option) => {
            const rawShare = option.share ?? (results.total_accepted > 0 ? option.count / results.total_accepted : 0);
            const share = Math.max(0, Math.min(1, rawShare));
            return (
              <div key={option.option_id} className="grid gap-2">
                <div className="flex items-baseline justify-between gap-4">
                  <span className="font-medium">{option.label}</span>
                  <span className="shrink-0 text-sm tabular-nums text-muted-foreground">
                    {formatInteger(option.count)} · {(share * 100).toLocaleString("ru-RU", { maximumFractionDigits: 1 })}%
                  </span>
                </div>
                <div className="h-2.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                  <div className="h-full rounded-full bg-primary transition-[width]" style={{ width: `${share * 100}%` }} />
                </div>
              </div>
            );
          })}
        </CardContent>
      </Card>

      <div className="grid gap-4 sm:grid-cols-2">
        <MetricCard icon={<CopyX />} label="Повторные попытки" value={results.rejected?.duplicate ?? 0} />
        <MetricCard icon={<Ban />} label="Ограничено rate limit" value={results.rejected?.rate_limited ?? 0} />
      </div>

      <Card>
        <CardHeader>
          <h3 className="flex items-center gap-2 text-base font-semibold"><Activity className="size-4" /> Динамика голосования</h3>
        </CardHeader>
        <CardContent>
          {chartData.length > 0 ? (
            <figure className="h-72 w-full" aria-label="График общего числа принятых голосов по времени">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={chartData} accessibilityLayer margin={{ left: 4, right: 12, top: 8, bottom: 0 }}>
                  <defs>
                    <linearGradient id="votes-fill" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--chart-1)" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="var(--chart-1)" stopOpacity={0.02} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} />
                  <XAxis dataKey="time" minTickGap={32} tickLine={false} axisLine={false} fontSize={11} />
                  <YAxis width={64} tickFormatter={formatInteger} tickLine={false} axisLine={false} fontSize={11} />
                  <Tooltip formatter={(value) => [formatInteger(Number(value)), "Голосов"]} />
                  <Area type="monotone" dataKey="votes" stroke="var(--chart-1)" fill="url(#votes-fill)" strokeWidth={2} />
                </AreaChart>
              </ResponsiveContainer>
            </figure>
          ) : (
            <p className="py-12 text-center text-sm text-muted-foreground">Точки динамики появятся после первого снимка.</p>
          )}
        </CardContent>
      </Card>
    </section>
  );
}

function MetricCard({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <Card className="gap-3 py-4">
      <CardContent className="flex-row items-center gap-3 px-4">
        <span className="grid size-9 place-items-center rounded-md bg-muted [&>svg]:size-4">{icon}</span>
        <span>
          <span className="block text-xl font-semibold tabular-nums">{formatInteger(value)}</span>
          <span className="text-xs text-muted-foreground">{label}</span>
        </span>
      </CardContent>
    </Card>
  );
}
