import React, { createContext, useContext, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
let csrf = "";
export class APIError extends Error {
  constructor(status, body) {
    super(body?.error?.message || `Request failed (${status})`);
    this.status = status;
    this.code = body?.error?.code;
  }
}
export async function api(path, { method = "GET", body, signal } = {}) {
  const response = await fetch(`/api${path}`, {
    method,
    signal,
    credentials: "same-origin",
    headers:
      method === "GET"
        ? {}
        : { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const value =
    response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) throw new APIError(response.status, value);
  return value;
}
export const encoded = encodeURIComponent;
export function useAPI(key, path, options = {}) {
  return useQuery({
    queryKey: key,
    queryFn: ({ signal }) => api(path, { signal }),
    ...options,
  });
}
export function useAction() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ path, method = "POST", body = {} }) =>
      api(path, { method, body }),
    onSuccess: (_, variables) => {
      if (variables.path === "/preferences")
        client.setQueryData(["me"], (user) => ({
          ...user,
          preferences: variables.body,
        }));
      return client.invalidateQueries({
        predicate: (query) => query.queryKey[0] !== "me",
      });
    },
  });
}
export function ErrorNotice({ error }) {
  return error ? (
    <p className="notice error" role="alert">
      {error.code && <strong>{error.code}: </strong>}
      {error.message || String(error)}
    </p>
  ) : null;
}
export function Pending({ children = "Loading…" }) {
  return (
    <p className="muted" role="status">
      {children}
    </p>
  );
}
const AuthContext = createContext(null);
export function useAuth() {
  return useContext(AuthContext);
}
export function Auth({ children }) {
  const client = useQueryClient();
  const me = useAPI(["me"], "/auth/me", { retry: false, staleTime: 60_000 });
  if (me.isPending)
    return (
      <main className="login">
        <h1>ARChika</h1>
        <Pending />
      </main>
    );
  if (me.data) csrf = me.data.csrf;
  if (!me.data)
    return (
      <Login
        error={me.error?.status !== 401 ? me.error : null}
        onLogin={(user) => {
          csrf = user.csrf;
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
      {children}
    </AuthContext.Provider>
  );
}
function Login({ error, onLogin }) {
  const [failure, setFailure] = useState(null),
    [busy, setBusy] = useState(false);
  async function submit(event) {
    event.preventDefault();
    setBusy(true);
    setFailure(null);
    const data = new FormData(event.currentTarget);
    try {
      onLogin(
        await api("/auth/login", {
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
            <input
              name="password"
              type="password"
              autoComplete="current-password"
              required
            />
          </label>
          <button className="primary" disabled={busy}>
            {busy ? "Signing in…" : "Sign in"}
          </button>
        </form>
        <ErrorNotice error={failure || error} />
        <p className="muted small">
          First installation? An administrator must create the first account
          using the server bootstrap CLI. There is no default password.
        </p>
      </div>
    </main>
  );
}
