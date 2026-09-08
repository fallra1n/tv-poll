import type { PollState } from "@/lib/api/types";
import { cn } from "@/lib/utils";

const stateLabels: Record<PollState, string> = {
  draft: "Черновик",
  scheduled: "Запланирован",
  open: "Открыт",
  closed: "Завершён",
};

export function pollStateLabel(state: PollState) {
  return stateLabels[state];
}

export function PollStateBadge({ state }: { state: PollState }) {
  return (
    <span
      className={cn(
        "inline-flex w-fit items-center rounded-full px-2.5 py-1 text-xs font-medium",
        state === "draft" && "bg-neutral-100 text-neutral-700",
        state === "scheduled" && "bg-blue-50 text-blue-800",
        state === "open" && "bg-emerald-50 text-emerald-800",
        state === "closed" && "bg-neutral-200 text-neutral-800",
      )}
    >
      {stateLabels[state]}
    </span>
  );
}
