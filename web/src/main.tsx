import i18n, { applyLanguage } from "./i18n.ts";
import { useTranslation } from "react-i18next";
import type { LibrarySearch } from "./types.ts";
import type { SearchSchemaInput } from "@tanstack/react-router";
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  Outlet,
  Link,
  useRouterState,
} from "@tanstack/react-router";
import { Auth, useAuth, ErrorNotice, APIError, ChangePassword, useAction } from "./api.tsx";
import Library from "./library.tsx";
import Reader from "./reader.tsx";
import Admin from "./admin.tsx";
import "./style.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 10 * 60_000,
      retry: (count, error) => error instanceof APIError && error.status >= 500 && count < 1,
      refetchOnWindowFocus: false,
    },
  },
});
function Shell() {
  const { t } = useTranslation();
  const { user, logout } = useAuth();
  const [error, setError] = useState<unknown>(null);
  const reader = useRouterState({
    select: (state) => state.location.pathname.startsWith("/read/"),
  });
  return (
    <>
      <a href="#main" className="skip-link">
        {t("nav.skipToContent")}
      </a>
      {!reader && (
        <header className="site-header">
          <Link to="/" className="brand">
            ARChika<span>.</span>
          </Link>
          <nav aria-label={t("nav.main")}>
            <Link to="/">{t("nav.library")}</Link>
            {user.admin && (
              <Link to="/admin" aria-label={t("nav.administration")}>
                <span className="wide-only">{t("nav.administration")}</span>
                <span className="narrow-only">{t("nav.admin")}</span>
              </Link>
            )}
            <Link to="/account">{user.name}</Link>
            <button onClick={() => logout().catch(setError)}>{t("nav.signOut")}</button>
          </nav>
        </header>
      )}
      <ErrorNotice error={error} />
      <Outlet />
    </>
  );
}
const root = createRootRoute({
  component: () => (
    <Auth>
      <Shell />
    </Auth>
  ),
  notFoundComponent: NotFound,
  errorComponent: RouteError,
});
function NotFound() {
  const { t } = useTranslation();
  return (
    <main id="main">
      <h1>{t("nav.notFound")}</h1>
      <Link to="/">{t("nav.returnToLibrary")}</Link>
    </main>
  );
}
function RouteError({ error }: { error: unknown }) {
  const { t } = useTranslation();
  return (
    <main>
      <ErrorNotice error={error} />
      <a href="/">{t("nav.library")}</a>
    </main>
  );
}
const searchText = (value: unknown) => String(value);
const positive = (value: unknown, fallback: number, max = 1_000_000) =>
  Math.max(1, Math.min(max, Math.floor(Number(value) || fallback)));
const library = createRoute({
  getParentRoute: () => root,
  path: "/",
  validateSearch: (search: Partial<Record<keyof LibrarySearch, unknown>> & SearchSchemaInput) => ({
    browse: search.browse === "folders" ? "folders" : "all",
    folder: typeof search.folder === "string" ? search.folder.replace(/^\/+|\/+$/g, "") : "",
    q: searchText(search.q || ""),
    sort:
      typeof search.sort === "string" && ["recent", "title", "mtime"].includes(search.sort)
        ? search.sort
        : "recent",
    direction: search.direction === "asc" ? "asc" : "desc",
    page: positive(search.page, 1),
    limit: [20, 40, 80, 100].includes(Number(search.limit)) ? Number(search.limit) : 40,
    root: searchText(search.root || ""),
    view: search.view === "infinite" ? "infinite" : "pages",
  }),
  component: Library,
});
const reader = createRoute({
  getParentRoute: () => root,
  path: "/read/$workId",
  validateSearch: (search: { page?: unknown; immersive?: unknown } & SearchSchemaInput) => ({
    page: positive(search.page, 1),
    immersive: search.immersive === true || search.immersive === "true",
  }),
  component: Reader,
});
const admin = createRoute({
  getParentRoute: () => root,
  path: "/admin",
  validateSearch: (search: { tab?: unknown } & SearchSchemaInput) => ({
    tab:
      typeof search.tab === "string" &&
      ["roots", "users", "jobs", "offline", "providers", "plugins", "audit"].includes(search.tab)
        ? search.tab
        : "roots",
  }),
  component: Admin,
});
const account = createRoute({
  getParentRoute: () => root,
  path: "/account",
  component: Account,
});
function Account() {
  const { t } = useTranslation();
  const { user } = useAuth();
  const action = useAction();
  const [language, setLanguage] = useState(user.preferences?.language ?? "");
  return (
    <main id="main" className="account">
      <h1>{t("nav.account")}</h1>
      <section className="panel">
        <h2>{t("account.language")}</h2>
        <label htmlFor="language">{t("account.language")}</label>
        <select
          id="language"
          name="language"
          value={language}
          disabled={action.isPending}
          onChange={(event) => {
            const value = event.target.value;
            setLanguage(value);
            applyLanguage(value);
            action.mutate(
              { path: "/preferences", method: "PUT", body: { language: value }, invalidate: [] },
              {
                onError: () => {
                  setLanguage(user.preferences?.language ?? "");
                  applyLanguage(user.preferences?.language);
                },
              },
            );
          }}
        >
          <option value="">{t("account.browserDefault")}</option>
          <option value="en">{t("account.english")}</option>
          <option value="ko">{t("account.korean")}</option>
        </select>
        {action.isPending && <p role="status">{t("auth.saving")}</p>}
        <ErrorNotice error={action.error} />
      </section>
      <section className="panel">
        <h2>{t("nav.changePassword")}</h2>
        <p className="muted">{t("nav.updateThePasswordForYourAccount")}</p>
        <ChangePassword />
      </section>
    </main>
  );
}

const router = createRouter({
  routeTree: root.addChildren([library, reader, admin, account]),
  defaultPreload: false,
});
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
const container = document.getElementById("root");
if (!container) throw new Error(i18n.t("nav.rootElementMissing"));
createRoot(container).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);
