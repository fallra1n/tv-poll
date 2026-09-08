import "../../styles/globals.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { ErrorBoundary } from "@/components/error-boundary";
import { PollPage } from "@/public/poll-page";

const element = document.getElementById("root");

if (!element) {
  throw new Error("Missing #root element");
}

const app = (
  <StrictMode>
    <ErrorBoundary title="Что-то пошло не так" message="Обновите страницу — данные опроса не сохраняются на устройстве.">
      <PollPage />
    </ErrorBoundary>
  </StrictMode>
);

if (import.meta.hot) {
  const root = (import.meta.hot.data.root ??= createRoot(element));
  root.render(app);
} else {
  createRoot(element).render(app);
}
