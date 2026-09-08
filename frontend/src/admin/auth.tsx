import { createContext, use, useCallback, useMemo, useState, type ReactNode } from "react";

const storageKey = "tv-poll:admin-token";

type AdminAuthValue = {
  token: string | null;
  error: string | null;
  login: (token: string) => void;
  logout: (message?: string) => void;
};

const AdminAuthContext = createContext<AdminAuthValue | null>(null);

function readStoredToken() {
  try {
    return window.sessionStorage.getItem(storageKey);
  } catch {
    return null;
  }
}

const initialToken = readStoredToken();

export function AdminAuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(initialToken);
  const [error, setError] = useState<string | null>(null);

  const login = useCallback((nextToken: string) => {
    const trimmedToken = nextToken.trim();

    if (!trimmedToken) {
      setError("Введите токен администратора.");
      return;
    }

    try {
      window.sessionStorage.setItem(storageKey, trimmedToken);
    } catch {
      // The token still remains available in memory for the current page.
    }

    setError(null);
    setToken(trimmedToken);
  }, []);

  const logout = useCallback((message?: string) => {
    try {
      window.sessionStorage.removeItem(storageKey);
    } catch {
      // Storage can be unavailable in privacy modes; clearing memory is enough.
    }

    setToken(null);
    setError(message ?? null);
  }, []);

  const value = useMemo(() => ({ token, error, login, logout }), [error, login, logout, token]);

  return (
    <AdminAuthContext value={value}>
      {children}
    </AdminAuthContext>
  );
}

export function useAdminAuth() {
  const value = use(AdminAuthContext);

  if (!value) {
    throw new Error("useAdminAuth must be used inside AdminAuthProvider");
  }

  return value;
}
