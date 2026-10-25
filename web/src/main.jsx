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
import { Auth, useAuth, ErrorNotice } from "./api.jsx";
import Library from "./library.jsx";
import Reader from "./reader.jsx";
import Admin from "./admin.jsx";
import "./style.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 10 * 60_000,
      retry: (count, error) => error.status >= 500 && count < 1,
      refetchOnWindowFocus: false,
    },
  },
});
function Shell() {
  const { user, logout } = useAuth();
  const [error, setError] = useState(null);
  const reader = useRouterState({
    select: (state) => state.location.pathname.startsWith("/read/"),
  });
  return (
    <>
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      {!reader && (
        <header className="site-header">
          <Link to="/" className="brand">
            ARChika<span>.</span>
          </Link>
          <nav aria-label="Main">
            <Link to="/">Library</Link>
            {user.admin && <Link to="/admin">Administration</Link>}
            <span className="muted">{user.name}</span>
            <button onClick={() => logout().catch(setError)}>Sign out</button>
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
  notFoundComponent: () => (
    <main id="main">
      <h1>Not found</h1>
      <Link to="/">Return to library</Link>
    </main>
  ),
  errorComponent: ({ error }) => (
    <main>
      <ErrorNotice error={error} />
      <a href="/">Library</a>
    </main>
  ),
});
const positive = (value, fallback, max = 1_000_000) =>
  Math.max(1, Math.min(max, Math.floor(Number(value) || fallback)));
const library = createRoute({
  getParentRoute: () => root,
  path: "/",
  validateSearch: (search) => ({
    q: String(search.q || ""),
    sort: ["recent", "title", "mtime"].includes(search.sort)
      ? search.sort
      : "recent",
    direction: search.direction === "asc" ? "asc" : "desc",
    page: positive(search.page, 1),
    limit: [20, 40, 80, 100].includes(Number(search.limit))
      ? Number(search.limit)
      : 40,
    root: String(search.root || ""),
    view: search.view === "infinite" ? "infinite" : "pages",
  }),
  component: Library,
});
const reader = createRoute({
  getParentRoute: () => root,
  path: "/read/$workId",
  validateSearch: (search) => ({
    page: positive(search.page, 1),
    immersive: search.immersive === true || search.immersive === "true",
  }),
  component: Reader,
});
const admin = createRoute({
  getParentRoute: () => root,
  path: "/admin",
  validateSearch: (search) => ({
    tab: [
      "roots",
      "users",
      "jobs",
      "offline",
      "providers",
      "plugins",
      "audit",
    ].includes(search.tab)
      ? search.tab
      : "roots",
  }),
  component: Admin,
});
const router = createRouter({
  routeTree: root.addChildren([library, reader, admin]),
  defaultPreload: false,
});
createRoot(document.getElementById("root")).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);
