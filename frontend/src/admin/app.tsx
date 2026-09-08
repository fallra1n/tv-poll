import { BarChart3, List, LogOut, Plus } from "lucide-react";
import { Link, Navigate, Route, Routes } from "react-router-dom";

import { useAdminAuth } from "@/admin/auth";
import { CreatePollPage } from "@/admin/pages/create-poll-page";
import { LoginPage } from "@/admin/pages/login-page";
import { PollDetailPage } from "@/admin/pages/poll-detail-page";
import { PollListPage } from "@/admin/pages/poll-list-page";
import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function AdminApp() {
  const { token } = useAdminAuth();

  if (!token) {
    return <LoginPage />;
  }

  return (
    <div className="min-h-dvh bg-background">
      <AdminHeader />
      <main className="mx-auto w-full max-w-6xl px-4 py-8 sm:px-6">
        <Routes>
          <Route path="/admin" element={<PollListPage />} />
          <Route path="/admin/polls/new" element={<CreatePollPage />} />
          <Route path="/admin/polls/:pollId" element={<PollDetailPage />} />
          <Route path="*" element={<Navigate to="/admin" replace />} />
        </Routes>
      </main>
    </div>
  );
}

function AdminHeader() {
  const { logout } = useAdminAuth();

  return (
    <header className="border-b bg-card">
      <div className="mx-auto flex min-h-16 max-w-6xl flex-wrap items-center gap-3 px-4 py-3 sm:px-6">
        <Link to="/admin" className="mr-auto inline-flex items-center gap-2 font-semibold">
          <span className="grid size-8 place-items-center rounded-md bg-primary text-primary-foreground">
            <BarChart3 className="size-4" />
          </span>
          TV Poll
        </Link>
        <nav aria-label="Основная навигация" className="flex items-center gap-1">
          <Link to="/admin" className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}>
            <List /> Опросы
          </Link>
          <Link to="/admin/polls/new" className={cn(buttonVariants({ variant: "outline", size: "sm" }))}>
            <Plus /> Создать
          </Link>
        </nav>
        <Button variant="ghost" size="icon" aria-label="Выйти" title="Выйти" onClick={() => logout()}>
          <LogOut />
        </Button>
      </div>
    </header>
  );
}
