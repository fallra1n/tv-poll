import type { PollPublic } from "@/lib/api/types";

export type PollPhase = "scheduled" | "open" | "closed";

const dateTimeFormatter = new Intl.DateTimeFormat("ru-RU", {
  dateStyle: "medium",
  timeStyle: "short",
});

const integerFormatter = new Intl.NumberFormat("ru-RU", {
  maximumFractionDigits: 0,
});

export function getPollPhase(poll: Pick<PollPublic, "opens_at" | "closes_at">, now = Date.now()): PollPhase {
  const opensAt = Date.parse(poll.opens_at);
  const closesAt = Date.parse(poll.closes_at);

  if (!Number.isFinite(opensAt) || !Number.isFinite(closesAt) || opensAt >= closesAt) {
    throw new Error("Poll has an invalid voting window");
  }

  if (now < opensAt) {
    return "scheduled";
  }

  if (now >= closesAt) {
    return "closed";
  }

  return "open";
}

export function formatDateTime(value: string) {
  const date = new Date(value);

  if (!Number.isFinite(date.getTime())) {
    return "Некорректная дата";
  }

  return dateTimeFormatter.format(date);
}

export function formatInteger(value: number) {
  return integerFormatter.format(value);
}

export function formatCountdown(target: string, now = Date.now()) {
  const milliseconds = Math.max(0, Date.parse(target) - now);
  const totalSeconds = Math.ceil(milliseconds / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;

  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60);
    const remainingMinutes = minutes % 60;
    return `${hours} ч ${remainingMinutes} мин`;
  }

  if (minutes > 0) {
    return `${minutes}:${seconds.toString().padStart(2, "0")}`;
  }

  return `${seconds} сек`;
}

export function toDateTimeLocalValue(date: Date) {
  const localTime = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return localTime.toISOString().slice(0, 19);
}

export function localDateTimeToIso(value: string) {
  const date = new Date(value);

  if (!value || !Number.isFinite(date.getTime())) {
    return null;
  }

  return date.toISOString();
}
