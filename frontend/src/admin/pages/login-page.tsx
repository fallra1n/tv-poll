import { KeyRound, ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";

import { useAdminAuth } from "@/admin/auth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function LoginPage() {
  const { error, login } = useAdminAuth();
  const [token, setToken] = useState("");

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    login(token);
  }

  return (
    <main className="grid min-h-dvh place-items-center px-4 py-8">
      <Card className="w-full max-w-md">
        <CardHeader className="gap-3">
          <span className="grid size-10 place-items-center rounded-lg bg-primary text-primary-foreground">
            <KeyRound className="size-5" />
          </span>
          <h1 className="text-2xl font-semibold">Управление опросами</h1>
          <p className="text-sm text-muted-foreground">
            Введите Bearer-токен администратора. Он хранится только до закрытия вкладки.
          </p>
        </CardHeader>
        <CardContent>
          <form className="grid gap-4" onSubmit={submit}>
            <div className="grid gap-2">
              <Label htmlFor="admin-token">Токен администратора</Label>
              <Input
                id="admin-token"
                type="password"
                autoComplete="off"
                value={token}
                aria-invalid={Boolean(error)}
                aria-describedby={error ? "login-error" : undefined}
                onChange={(event) => setToken(event.target.value)}
              />
              {error && <p id="login-error" role="alert" className="text-sm text-destructive">{error}</p>}
            </div>
            <Button type="submit" size="lg">Продолжить</Button>
            <p className="flex items-start gap-2 text-xs text-muted-foreground">
              <ShieldCheck className="mt-0.5 size-4 shrink-0" />
              Токен не встраивается в frontend-сборку и не сохраняется после завершения сессии.
            </p>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
