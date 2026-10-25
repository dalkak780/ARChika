import type { ReactNode, FormEvent } from "react";
import type { QueryClient, QueryKey, UseQueryOptions } from "@tanstack/react-query";
import type { User, Preferences } from "./types.ts";
import React, { createContext, useContext, useEffect, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
let csrf = "";
function setCsrf(token: string) {
  csrf = token;
}
const authClients = new Set<QueryClient>();
export class APIError extends Error {
  status: number;
  code?: string;
  constructor(status: number, body: unknown) {
    const error = body && typeof body === "object" && "error" in body ? body.error : null;
    const message =
      error && typeof error === "object" && "message" in error && typeof error.message === "string"
        ? error.message
        : undefined;
    super(message || `Request failed (${status})`);
    this.status = status;
    this.code =
      error && typeof error === "object" && "code" in error && typeof error.code === "string"
        ? error.code
        : undefined;
  }
}
interface APIOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}
export async function api<T = unknown>(
  path: string,
  { method = "GET", body, signal }: APIOptions = {},
): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method,
    signal,
    credentials: "same-origin",
    headers: method === "GET" ? {} : { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const value: unknown = response.status === 204 ? null : await response.json().catch(() => null);
  if (response.status === 401) {
    csrf = "";
    for (const client of authClients) client.setQueryData(["me"], null);
  }
  if (!response.ok) {
    const error = new APIError(response.status, value);
    if (error.status === 403 && error.code === "password_change_required") {
      for (const client of authClients) void client.invalidateQueries({ queryKey: ["me"] });
    }
    throw error;
  }
  // The caller supplies the response contract from the Go handler.
  return value as T;
}
export const encoded = encodeURIComponent;
export function useAPI<T>(
  key: QueryKey,
  path: string,
  options: Omit<UseQueryOptions<T, Error>, "queryKey" | "queryFn"> = {},
) {
  return useQuery({
    queryKey: key,
    queryFn: ({ signal }) => api<T>(path, { signal }),
    ...options,
  });
}
interface ActionVariables<TBody> {
  path: string;
  method?: string;
  body?: TBody;
  invalidate?: readonly QueryKey[];
}
export function useAction<T = unknown, TBody = unknown>() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ path, method = "POST", body }: ActionVariables<TBody>) =>
      api<T>(path, { method, body: body === undefined ? {} : body }),
    onSuccess: (_, variables) => {
      if (variables.path === "/preferences")
        client.setQueryData<Partial<User>>(["me"], (user) => ({
          ...user,
          // Only the preferences form sends this endpoint's validated payload.
          preferences: variables.body as Preferences,
        }));
      if (variables.invalidate !== undefined)
        return Promise.all(
          variables.invalidate.map((queryKey) => client.invalidateQueries({ queryKey })),
        );
      return client.invalidateQueries({
        predicate: (query) => query.queryKey[0] !== "me",
      });
    },
  });
}
export function ErrorNotice({ error }: { error: unknown }) {
  const code =
    error && typeof error === "object" && "code" in error && typeof error.code === "string"
      ? error.code
      : undefined;
  const message =
    error && typeof error === "object" && "message" in error && typeof error.message === "string"
      ? error.message
      : undefined;
  const fallback = String(error);
  return error ? (
    <p className="notice error" role="alert">
      {Boolean(code) && <strong>{String(code)}: </strong>}
      {message || fallback}
    </p>
  ) : null;
}
export function Pending({ children = "Loading…" }: { children?: ReactNode }) {
  return (
    <p className="muted" role="status">
      {children}
    </p>
  );
}
const AuthContext = createContext<{ user: User; logout: () => Promise<void> } | null>(null);
export function useAuth() {
  const auth = useContext(AuthContext);
  if (!auth) throw new Error("Auth provider required");
  return auth;
}
export function Auth({ children }: { children: ReactNode }) {
  const client = useQueryClient();
  useEffect(() => {
    authClients.add(client);
    return () => {
      authClients.delete(client);
    };
  }, [client]);
  const me = useAPI<User | null>(["me"], "/auth/me", { retry: false, staleTime: 60_000 });
  useEffect(() => {
    if (me.data) setCsrf(me.data.csrf || "");
  }, [me.data]);
  if (me.isPending)
    return (
      <main className="login">
        <h1>ARChika</h1>
        <Pending />
      </main>
    );
  if (!me.data)
    return (
      <Login
        error={!(me.error instanceof APIError && me.error.status === 401) ? me.error : null}
        onLogin={(user) => {
          csrf = user.csrf || "";
          client.setQueryData(["me"], user);
        }}
      />
    );
  return (
    <AuthContext.Provider
      value={{
        user: me.data,
        logout: async () => {
          await api("/auth/logout", { method: "POST", body: {} });
          csrf = "";
          client.clear();
          window.location.replace("/");
        },
      }}
    >
      {me.data.must_change_password ? (
        <main className="login">
          <div className="panel">
            <h1>Change password</h1>
            <p>Choose a new password to continue.</p>
            <ChangePassword />
          </div>
        </main>
      ) : (
        children
      )}
    </AuthContext.Provider>
  );
}
function Login({ error, onLogin }: { error: unknown; onLogin: (user: User) => void }) {
  const [failure, setFailure] = useState<unknown>(null),
    [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setFailure(null);
    const data = new FormData(event.currentTarget);
    try {
      onLogin(
        await api<User>("/auth/login", {
          method: "POST",
          body: { name: data.get("name"), password: data.get("password") },
        }),
      );
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="login">
      <div className="panel">
        <div className="eyebrow">YOUR READ-ONLY IMAGE LIBRARY</div>
        <h1>
          ARChika<span className="accent">.</span>
        </h1>
        <p>Welcome back. Open your library.</p>
        <form onSubmit={submit}>
          <label>
            Username
            <input name="name" autoComplete="username" required autoFocus />
          </label>
          <label>
            Password
            <input name="password" type="password" autoComplete="current-password" required />
          </label>
          <button className="primary" disabled={busy}>
            {busy ? "Signing in…" : "Sign in"}
          </button>
        </form>
        <ErrorNotice error={failure || error} />
        <p className="muted small">
          First installation? Find the one-time administrator password in the server logs, unless
          you configured an initial password.
        </p>
      </div>
    </main>
  );
}

export function ChangePassword() {
  const client = useQueryClient();
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const value = data.get("new_password");
    const password = typeof value === "string" ? value : "";
    setSaved(false);
    setFailure(null);
    if (new TextEncoder().encode(password).length < 12) {
      setFailure(new Error("Use at least 12 bytes for the new password."));
      return;
    }
    if (password !== data.get("confirm_password")) {
      setFailure(new Error("New passwords must match."));
      return;
    }
    setBusy(true);
    try {
      const user = await api<User>("/auth/password", {
        method: "POST",
        body: { current_password: data.get("current_password"), new_password: password },
      });
      client.setQueryData(["me"], user);
      form.reset();
      setSaved(true);
    } catch (error) {
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit}>
      <label>
        Current password
        <input name="current_password" type="password" autoComplete="current-password" required />
      </label>
      <label>
        New password
        <input
          name="new_password"
          type="password"
          autoComplete="new-password"
          minLength={12}
          required
        />
      </label>
      <label>
        Confirm new password
        <input
          name="confirm_password"
          type="password"
          autoComplete="new-password"
          minLength={12}
          required
        />
      </label>
      <button className="primary" disabled={busy}>
        {busy ? "Saving…" : "Change password"}
      </button>
      <ErrorNotice error={failure} />
      {saved && <p role="status">Password changed. Other sessions have been signed out.</p>}
    </form>
  );
}
