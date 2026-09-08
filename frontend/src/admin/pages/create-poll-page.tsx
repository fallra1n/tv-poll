import { ArrowLeft, LoaderCircle, Plus, Trash2 } from "lucide-react";
import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";

import { useAdminAuth } from "@/admin/auth";
import { AdminInlineError } from "@/admin/pages/poll-list-page";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardFooter, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { createPoll } from "@/lib/api/admin-client";
import { ApiError } from "@/lib/api/http";
import type { PollCreateRequest } from "@/lib/api/types";
import { localDateTimeToIso, toDateTimeLocalValue } from "@/lib/dates";
import { cn } from "@/lib/utils";

type OptionField = {
  key: string;
  label: string;
};

type FormErrors = {
  question?: string;
  options?: string;
  optionKeys: Record<string, string>;
  votingWindow?: string;
  scheduledAt?: string;
};

const emptyErrors: FormErrors = { optionKeys: {} };

function validateForm(
  question: string,
  options: OptionField[],
  votingWindow: string,
  scheduleEnabled: boolean,
  scheduledAt: string,
) {
  const errors: FormErrors = { optionKeys: {} };
  const trimmedQuestion = question.trim();
  const duration = Number(votingWindow);

  if (!trimmedQuestion) {
    errors.question = "Введите вопрос.";
  } else if (trimmedQuestion.length > 500) {
    errors.question = "Вопрос не должен превышать 500 символов.";
  }

  if (options.length < 2 || options.length > 50) {
    errors.options = "Нужно добавить от 2 до 50 вариантов.";
  }

  for (const option of options) {
    const label = option.label.trim();
    if (!label) {
      errors.optionKeys[option.key] = "Введите текст варианта.";
    } else if (label.length > 200) {
      errors.optionKeys[option.key] = "Не более 200 символов.";
    }
  }

  if (!Number.isInteger(duration) || duration < 30) {
    errors.votingWindow = "Укажите целое число не меньше 30 секунд.";
  }

  if (scheduleEnabled && !localDateTimeToIso(scheduledAt)) {
    errors.scheduledAt = "Укажите дату и время эфира.";
  }

  return errors;
}

function hasErrors(errors: FormErrors) {
  return Boolean(
    errors.question
    || errors.options
    || errors.votingWindow
    || errors.scheduledAt
    || Object.keys(errors.optionKeys).length > 0,
  );
}

export function CreatePollPage() {
  const { token, logout } = useAdminAuth();
  const navigate = useNavigate();
  const nextOptionKey = useRef(3);
  const [question, setQuestion] = useState("");
  const [options, setOptions] = useState<OptionField[]>([
    { key: "option-1", label: "" },
    { key: "option-2", label: "" },
  ]);
  const [votingWindow, setVotingWindow] = useState("300");
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduledAt, setScheduledAt] = useState("");
  const [errors, setErrors] = useState<FormErrors>(emptyErrors);
  const [serverError, setServerError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  function updateOption(key: string, label: string) {
    setOptions((current) => current.map((option) => option.key === key ? { ...option, label } : option));
  }

  function addOption() {
    if (options.length >= 50) {
      return;
    }

    const key = `option-${nextOptionKey.current}`;
    nextOptionKey.current += 1;
    setOptions((current) => [...current, { key, label: "" }]);
  }

  function removeOption(key: string) {
    if (options.length <= 2) {
      return;
    }

    setOptions((current) => current.filter((option) => option.key !== key));
  }

  function toggleSchedule(checked: boolean) {
    setScheduleEnabled(checked);
    if (checked && !scheduledAt) {
      setScheduledAt(toDateTimeLocalValue(new Date(Date.now() + 5 * 60_000)));
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextErrors = validateForm(question, options, votingWindow, scheduleEnabled, scheduledAt);
    setErrors(nextErrors);
    setServerError(null);

    if (hasErrors(nextErrors) || !token) {
      return;
    }

    const request: PollCreateRequest = {
      question: question.trim(),
      options: options.map((option) => ({ label: option.label.trim() })),
      voting_window_seconds: Number(votingWindow),
    };

    if (scheduleEnabled) {
      const isoDate = localDateTimeToIso(scheduledAt);
      if (isoDate) {
        request.scheduled_at = isoDate;
      }
    }

    setSubmitting(true);

    try {
      const poll = await createPoll(token, request);
      navigate(`/admin/polls/${poll.id}`, { replace: true });
    } catch (error: unknown) {
      if (error instanceof ApiError && error.status === 401) {
        logout("Токен недействителен. Введите актуальный токен администратора.");
        return;
      }

      setServerError(error instanceof ApiError ? error.message : "Не удалось создать опрос.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <section className="mx-auto grid max-w-3xl gap-6">
      <div>
        <Link to="/admin" className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "-ml-2 mb-3")}>
          <ArrowLeft /> К списку
        </Link>
        <h1 className="text-3xl font-semibold tracking-tight">Новый опрос</h1>
        <p className="mt-1 text-sm text-muted-foreground">После публикации вопрос и варианты нельзя изменить.</p>
      </div>

      <form className="grid gap-6" onSubmit={submit} noValidate>
        <Card>
          <CardHeader>
            <h2 className="text-lg font-semibold">Вопрос и ответы</h2>
          </CardHeader>
          <CardContent className="gap-6">
            <div className="grid gap-2">
              <div className="flex items-center justify-between gap-3">
                <Label htmlFor="poll-question">Вопрос</Label>
                <span className="text-xs text-muted-foreground">{question.length}/500</span>
              </div>
              <Textarea
                id="poll-question"
                value={question}
                maxLength={500}
                placeholder="Какой вариант вы выбираете?"
                aria-invalid={Boolean(errors.question)}
                aria-describedby={errors.question ? "question-error" : undefined}
                onChange={(event) => setQuestion(event.target.value)}
              />
              {errors.question && <p id="question-error" className="text-sm text-destructive">{errors.question}</p>}
            </div>

            <fieldset className="grid gap-3">
              <legend className="mb-1 text-sm font-medium">Варианты ответа</legend>
              {options.map((option, index) => {
                const error = errors.optionKeys[option.key];
                const inputId = `poll-${option.key}`;
                return (
                  <div key={option.key} className="grid gap-1.5">
                    <div className="flex items-center gap-2">
                      <span className="w-6 shrink-0 text-right text-sm text-muted-foreground">{index + 1}.</span>
                      <Label className="sr-only" htmlFor={inputId}>Вариант {index + 1}</Label>
                      <Input
                        id={inputId}
                        value={option.label}
                        maxLength={200}
                        placeholder={`Вариант ${index + 1}`}
                        aria-invalid={Boolean(error)}
                        aria-describedby={error ? `${inputId}-error` : undefined}
                        onChange={(event) => updateOption(option.key, event.target.value)}
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={`Удалить вариант ${index + 1}`}
                        disabled={options.length <= 2}
                        onClick={() => removeOption(option.key)}
                      >
                        <Trash2 />
                      </Button>
                    </div>
                    {error && <p id={`${inputId}-error`} className="ml-8 text-sm text-destructive">{error}</p>}
                  </div>
                );
              })}
              {errors.options && <p className="text-sm text-destructive">{errors.options}</p>}
              <Button className="w-fit" type="button" variant="outline" disabled={options.length >= 50} onClick={addOption}>
                <Plus /> Добавить вариант
              </Button>
            </fieldset>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <h2 className="text-lg font-semibold">Параметры голосования</h2>
          </CardHeader>
          <CardContent className="gap-5">
            <div className="grid max-w-xs gap-2">
              <Label htmlFor="voting-window">Окно голосования, секунд</Label>
              <Input
                id="voting-window"
                type="number"
                min={30}
                step={1}
                value={votingWindow}
                aria-invalid={Boolean(errors.votingWindow)}
                aria-describedby={errors.votingWindow ? "voting-window-error" : "voting-window-hint"}
                onChange={(event) => setVotingWindow(event.target.value)}
              />
              <p id="voting-window-hint" className="text-xs text-muted-foreground">По умолчанию 5 минут. Минимум 30 секунд.</p>
              {errors.votingWindow && <p id="voting-window-error" className="text-sm text-destructive">{errors.votingWindow}</p>}
            </div>

            <div className="flex items-start gap-3 rounded-lg border p-4">
              <input
                id="schedule-on-create"
                className="mt-0.5 size-4 accent-foreground"
                type="checkbox"
                checked={scheduleEnabled}
                onChange={(event) => toggleSchedule(event.target.checked)}
              />
              <Label htmlFor="schedule-on-create" className="block cursor-pointer leading-normal">
                <span className="block text-sm font-medium">Сразу запланировать эфир</span>
                <span className="mt-1 block text-xs text-muted-foreground">Без этой опции опрос сохранится как черновик.</span>
              </Label>
            </div>

            {scheduleEnabled && (
              <div className="grid max-w-sm gap-2">
                <Label htmlFor="scheduled-at">Дата и время эфира</Label>
                <Input
                  id="scheduled-at"
                  type="datetime-local"
                  value={scheduledAt}
                  aria-invalid={Boolean(errors.scheduledAt)}
                  aria-describedby={errors.scheduledAt ? "scheduled-at-error" : "scheduled-at-hint"}
                  onChange={(event) => setScheduledAt(event.target.value)}
                />
                <p id="scheduled-at-hint" className="text-xs text-muted-foreground">Время вводится в часовом поясе этого устройства.</p>
                {errors.scheduledAt && <p id="scheduled-at-error" className="text-sm text-destructive">{errors.scheduledAt}</p>}
              </div>
            )}
          </CardContent>
          <CardFooter className="flex-wrap justify-end gap-3 border-t pt-6">
            <Link to="/admin" className={cn(buttonVariants({ variant: "ghost" }))}>Отмена</Link>
            <Button type="submit" disabled={submitting}>
              {submitting && <LoaderCircle className="animate-spin" />}
              Создать опрос
            </Button>
          </CardFooter>
        </Card>

        {serverError && <AdminInlineError message={serverError} />}
      </form>
    </section>
  );
}
