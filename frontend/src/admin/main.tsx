import "../../styles/globals.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";

import { AdminApp } from "@/admin/app";
import { AdminAuthProvider } from "@/admin/auth";
import { ErrorBoundary } from "@/components/error-boundary";

const element = document.getElementById("root");

if (!element) {
  throw new Error("Missing #root element");
}

const app = (
  <StrictMode>
  <ErrorBoundary title="Что-то пошло не так" message="Обновите страницу. Если ошибка повторится, сообщите разработчикам.">
    <BrowserRouter>
      <AdminAuthProvider>
        <AdminApp />
      </AdminAuthProvider>
    </BrowserRouter>
  </ErrorBoundary>
  </StrictMode>
);

if (import.meta.hot) {
  const root = (import.meta.hot.data.root ??= createRoot(element));
  root.render(app);
} else {
  createRoot(element).render(app);
}
