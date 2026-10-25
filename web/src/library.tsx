import type { FormEvent, UIEvent } from "react";
import type { InfiniteData } from "@tanstack/react-query";
import type { Work, WorksPage, Root, SavedSearch, LibrarySearch } from "./types.ts";
import React, { useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";
import { Link, getRouteApi, useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { VirtualLibraryRows } from "./virtual.tsx";
import { api, APIError, encoded, useAPI, useAction, ErrorNotice, Pending } from "./api.tsx";

const route = getRouteApi("/");
export const libraryOrigins = new Map<string, LibrarySearch>();
const scrollPositions = new Map<string, number>();
const failedThumbnails = new Map<string, boolean>();
const infiniteResume = new Map<string, { page: number; offset: number }>();
function workPath(search: LibrarySearch, page: number, cursor?: string) {
  const params = new URLSearchParams({
    q: search.q,
    sort: search.sort,
    direction: search.direction,
    page: String(page),
    limit: String(search.limit),
  });
  if (search.root) params.set("root", search.root);
  if (cursor) params.set("cursor", cursor);
  return `/works?${params}`;
}
function Card({ work, search }: { work: Work; search: LibrarySearch }) {
  const action = useAction<unknown, { favorite: boolean }>();
  const client = useQueryClient();
  const thumbnailKey = `${work.id}:${work.version}`;
  const [failed, setFailed] = useState(() => failedThumbnails.has(thumbnailKey));
  function failThumbnail() {
    failedThumbnails.set(thumbnailKey, true);
    if (failedThumbnails.size > 512) failedThumbnails.delete(failedThumbnails.keys().next().value!);
    setFailed(true);
  }
  return (
    <article className="work-card">
      <Link
        to="/read/$workId"
        params={{ workId: work.id }}
        search={{ page: 1, immersive: false }}
        onClick={() => {
          libraryOrigins.set(String(work.id), search);
          if (libraryOrigins.size > 64) libraryOrigins.delete(libraryOrigins.keys().next().value!);
        }}
        className="work-link"
      >
        <div className="cover">
          {failed ? (
            <span className="cover-placeholder" aria-label="Cover unavailable">
              ▧<small>{work.cover_unresolved ? "Cover needs selection" : "No preview"}</small>
            </span>
          ) : (
            <img
              src={`/api/works/${encoded(work.id)}/thumbnail`}
              loading="lazy"
              decoding="async"
              alt=""
              onError={failThumbnail}
            />
          )}
          <span className={`status ${work.index_status}`}>{work.index_status || "registered"}</span>
        </div>
        <h3 title={work.title}>{work.title}</h3>
        <p title={work.relative_path}>
          {work.root_name} · {work.relative_path}
        </p>
        {work.error_code && <small className="error-text">{work.error_code}</small>}
      </Link>
      <button
        className="favorite-toggle"
        aria-label={`${work.favorite ? "Remove favorite" : "Add favorite"}: ${work.title}`}
        aria-pressed={work.favorite}
        disabled={action.isPending}
        onClick={() =>
          action.mutate(
            {
              path: `/works/${encoded(work.id)}/favorite`,
              method: "PUT",
              body: { favorite: !work.favorite },
              invalidate: [],
            },
            {
              onSuccess: (_, variables) => {
                const updatePage = <T extends WorksPage>(page: T): T => ({
                  ...page,
                  items: page.items.map((item) =>
                    item.id === work.id ? { ...item, favorite: variables.body!.favorite } : item,
                  ),
                });
                client.setQueriesData<WorksPage>({ queryKey: ["works"] }, (data) =>
                  data ? updatePage(data) : data,
                );
                client.setQueriesData<InfiniteData<WorksPage & { pageNumber: number }>>(
                  { queryKey: ["works-infinite"] },
                  (data) => (data ? { ...data, pages: data.pages.map(updatePage) } : data),
                );
              },
            },
          )
        }
      >
        {work.favorite ? "★" : "☆"}
      </button>
      <ErrorNotice error={action.error} />
    </article>
  );
}
export default function Library() {
  const search = route.useSearch(),
    navigate = useNavigate({ from: "/" });
  const [query, setQuery] = useState(search.q),
    [savedName, setSavedName] = useState("");
  const roots = useAPI<Root[]>(["roots"], "/roots"),
    saved = useAPI<SavedSearch[]>(["saved-searches"], "/saved-searches"),
    action = useAction();
  const [previousQuery, setPreviousQuery] = useState(search.q);
  if (previousQuery !== search.q) {
    setPreviousQuery(search.q);
    setQuery(search.q);
  }
  const update = (patch: Partial<LibrarySearch>) =>
    navigate({
      search: (previous) => ({ ...previous, ...patch, page: patch.page ?? 1 }),
    });
  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    action.mutate(
      {
        path: "/saved-searches",
        body: { name: savedName, query: search.q, sort: search.sort },
      },
      { onSuccess: () => setSavedName("") },
    );
  }
  return (
    <main id="main" className="library">
      <div className="page-heading">
        <div>
          <div className="eyebrow">BROWSE YOUR COLLECTION</div>
          <h1>Library</h1>
          <button
            aria-pressed={search.q === "favorite:true"}
            onClick={() => update({ q: search.q === "favorite:true" ? "" : "favorite:true" })}
          >
            Favorites
          </button>
        </div>
        <div className="segmented" aria-label="Library view">
          <button aria-pressed={search.view === "pages"} onClick={() => update({ view: "pages" })}>
            Pages
          </button>
          <button
            aria-pressed={search.view === "infinite"}
            onClick={() => update({ view: "infinite" })}
          >
            Infinite
          </button>
        </div>
      </div>
      <section className="panel filters" aria-label="Library filters">
        <form
          className="search-form"
          onSubmit={(event) => {
            event.preventDefault();
            void update({ q: query });
          }}
        >
          <label className="grow">
            Search
            <input
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={"Title, path, tag…"}
            />
          </label>
          <button className="primary">Search</button>
        </form>
        <div className="toolbar">
          <label>
            Root
            <select value={search.root} onChange={(e) => update({ root: e.target.value })}>
              <option value="">All accessible roots</option>
              {roots.data?.map((root) => (
                <option key={root.id} value={root.id}>
                  {root.name} · {root.status}
                </option>
              ))}
            </select>
          </label>
          <label>
            Sort
            <select value={search.sort} onChange={(e) => update({ sort: e.target.value })}>
              <option value="recent">Recently indexed</option>
              <option value="title">Title</option>
              <option value="mtime">File modified</option>
            </select>
          </label>
          <label>
            Direction
            <select
              value={search.direction}
              onChange={(e) => update({ direction: e.target.value })}
            >
              <option value="desc">Descending</option>
              <option value="asc">Ascending</option>
            </select>
          </label>
          <label>
            Page size
            <select
              value={search.limit}
              onChange={(e) => update({ limit: Number(e.target.value) })}
            >
              {[20, 40, 80, 100].map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
          </label>
        </div>
        <details>
          <summary>Search syntax & saved searches</summary>
          <p className="small muted">
            Combine AND, OR, NOT and parentheses. Use quoted phrases, title:, path:, root:,
            tag:namespace:value, word: or substring:. One–two character terms match indexed
            prefixes/exact values; longer terms match substrings. Only accessible root paths are
            searched.
          </p>
          <div className="chips">
            {saved.data?.map((item) => (
              <span key={item.id}>
                <button onClick={() => update({ q: item.query, sort: item.sort || "recent" })}>
                  {item.name}
                </button>
                <button
                  aria-label={`Delete ${item.name}`}
                  onClick={() =>
                    action.mutate({
                      path: `/saved-searches/${encoded(item.id)}`,
                      method: "DELETE",
                    })
                  }
                >
                  ×
                </button>
              </span>
            ))}
          </div>
          <form className="inline" onSubmit={save}>
            <input
              aria-label="Saved search name"
              value={savedName}
              onChange={(e) => setSavedName(e.target.value)}
              placeholder="Name this search"
              required
              maxLength={100}
            />
            <button disabled={action.isPending}>Save current search</button>
          </form>
        </details>
      </section>
      <ErrorNotice error={roots.error || saved.error || action.error} />
      {search.view === "infinite" ? (
        <InfiniteLibrary key={JSON.stringify({ ...search, page: undefined })} search={search} />
      ) : (
        <PagedLibrary search={search} update={update} />
      )}
    </main>
  );
}
function PagedLibrary({
  search,
  update,
}: {
  search: LibrarySearch;
  update: (patch: Partial<LibrarySearch>) => void;
}) {
  const key = JSON.stringify(search),
    result = useAPI<WorksPage>(["works", search], workPath(search, search.page));
  useLayoutEffect(() => {
    if (result.isPending) return;
    window.scrollTo(0, scrollPositions.get(key) || 0);
    const save = () => {
      scrollPositions.set(key, window.scrollY);
      if (scrollPositions.size > 64) scrollPositions.delete(scrollPositions.keys().next().value!);
    };
    window.addEventListener("scroll", save, { passive: true });
    return () => window.removeEventListener("scroll", save);
  }, [key, result.isPending]);
  return (
    <>
      <ErrorNotice error={result.error} />
      {result.isPending ? (
        <Pending />
      ) : (
        result.data && (
          <>
            <div className="results-count" role="status">
              {result.data.total.toLocaleString()} works · page {search.page}
            </div>
            <div className="card-grid">
              {result.data.items.map((work) => (
                <Card key={work.id} work={work} search={search} />
              ))}
            </div>
            {!result.data.items.length && (
              <div className="empty">
                <h2>No matching works</h2>
                <p>Change the search or ask an administrator to explicitly scan an online root.</p>
              </div>
            )}
            <nav className="pagination" aria-label="Pages">
              <button disabled={search.page <= 1} onClick={() => update({ page: search.page - 1 })}>
                ← Previous
              </button>
              <form
                className="inline"
                onSubmit={(event) => {
                  event.preventDefault();
                  update({
                    page: Number(new FormData(event.currentTarget).get("page")),
                  });
                }}
              >
                <label>
                  Page
                  <input
                    key={search.page}
                    name="page"
                    type="number"
                    min="1"
                    max={Math.max(1, Math.ceil(result.data.total / search.limit))}
                    defaultValue={search.page}
                  />
                </label>
                <button>Go</button>
                <span>of {Math.max(1, Math.ceil(result.data.total / search.limit))}</span>
              </form>
              <button
                disabled={search.page * search.limit >= result.data.total}
                onClick={() => update({ page: search.page + 1 })}
              >
                Next →
              </button>
            </nav>
          </>
        )
      )}
    </>
  );
}
function InfiniteLibrary({ search }: { search: LibrarySearch }) {
  const client = useQueryClient();
  const container = useRef<HTMLDivElement>(null),
    [columns, setColumns] = useState(4),
    [refreshCount, setRefreshCount] = useState(0),
    key = JSON.stringify(search);
  const initialPageParam: { page: number; cursor?: string } = {
    page: refreshCount ? 1 : infiniteResume.get(key)?.page || search.page,
  };
  const result = useInfiniteQuery({
    queryKey: ["works-infinite", search],
    initialPageParam,
    maxPages: 5,
    queryFn: async ({ pageParam, signal }) => ({
      ...(await api<WorksPage>(workPath(search, pageParam.page, pageParam.cursor), {
        signal,
      })),
      pageNumber: pageParam.page,
    }),
    getNextPageParam: (last) =>
      last.next_cursor ? { page: last.pageNumber + 1, cursor: last.next_cursor } : undefined,
    getPreviousPageParam: (first) =>
      first.pageNumber > 1 ? { page: first.pageNumber - 1 } : undefined,
  });
  const items = result.data?.pages.flatMap((page) => page.items) || [];
  const startPage = result.data?.pages[0]?.pageNumber;
  const previousStart = useRef(startPage);
  const restored = useRef(false);
  function resetScroll() {
    infiniteResume.delete(key);
    scrollPositions.delete(key);
    previousStart.current = undefined;
    restored.current = true;
    if (container.current) container.current.scrollTop = 0;
  }
  function refresh() {
    resetScroll();
    setRefreshCount((count) => count + 1);
  }
  const [previousError, setPreviousError] = useState<Error | null>(null);
  if (previousError !== result.error) {
    setPreviousError(result.error);
    if (result.error instanceof APIError && result.error.code === "cursor_stale")
      setRefreshCount((count) => count + 1);
  }
  const refreshOnError = useEffectEvent(resetScroll);
  useEffect(() => {
    if (result.error instanceof APIError && result.error.code === "cursor_stale") refreshOnError();
  }, [result.error]);
  useEffect(() => {
    if (refreshCount)
      void client.resetQueries({
        queryKey: ["works-infinite", JSON.parse(key)],
        exact: true,
      });
  }, [client, key, refreshCount]);
  useLayoutEffect(() => {
    if (!restored.current && startPage && container.current) {
      const capacity = Math.max(1, Math.floor(container.current.clientWidth / 180));
      const actualColumns = capacity >= 5 ? 5 : capacity >= 4 ? 4 : capacity >= 2 ? 2 : 1;
      if (columns !== actualColumns) {
        setColumns(actualColumns);
        return;
      }
      const resume = infiniteResume.get(key);
      if (resume)
        container.current.scrollTop =
          (resume.page - startPage) * Math.ceil(search.limit / columns) * 312 + resume.offset;
      restored.current = true;
    }
  }, [startPage, columns, key, search.limit]);
  function saveScroll(event: UIEvent<HTMLDivElement>) {
    const top = event.currentTarget.scrollTop,
      pageHeight = Math.ceil(search.limit / columns) * 312;
    scrollPositions.set(key, top);
    if (scrollPositions.size > 64) scrollPositions.delete(scrollPositions.keys().next().value!);
    if (startPage)
      infiniteResume.set(key, {
        page: startPage + Math.floor(top / pageHeight),
        offset: top % pageHeight,
      });
    if (infiniteResume.size > 64) infiniteResume.delete(infiniteResume.keys().next().value!);
  }
  useLayoutEffect(() => {
    if (
      previousStart.current &&
      startPage &&
      previousStart.current !== startPage &&
      container.current
    ) {
      container.current.scrollTop +=
        (previousStart.current - startPage) * Math.ceil(search.limit / columns) * 312;
    }
    previousStart.current = startPage;
  }, [startPage, columns, search.limit]);
  useEffect(() => {
    const observer = new ResizeObserver((entries) => {
      const capacity = Math.max(1, Math.floor(entries[0]!.contentRect.width / 180));
      setColumns(capacity >= 5 ? 5 : capacity >= 4 ? 4 : capacity >= 2 ? 2 : 1);
    });
    if (container.current) observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  return (
    <>
      <ErrorNotice error={result.error} />
      <div className="results-count">
        {result.data?.pages[0]?.total.toLocaleString() ?? "…"} works · up to five result pages
        retained
      </div>
      <div className="toolbar">
        <button
          disabled={!result.hasPreviousPage || result.isFetching}
          onClick={() => result.fetchPreviousPage()}
        >
          Load earlier results
        </button>
        <button disabled={result.isFetching} onClick={refresh}>
          Refresh results
        </button>
      </div>
      <div
        className="infinite-list"
        ref={container}
        onScroll={saveScroll}
        tabIndex={0}
        aria-label="Scrollable library results"
      >
        <VirtualLibraryRows
          container={container}
          items={items}
          columns={columns}
          initialOffset={scrollPositions.get(key) || 0}
          renderItem={(work) => <Card work={work} search={search} />}
          hasNextPage={result.hasNextPage}
          isFetching={result.isFetching}
          error={result.error}
          fetchNextPage={result.fetchNextPage}
        />
        {result.isPending && <Pending />}
      </div>
      <button
        disabled={!result.hasNextPage || result.isFetching}
        onClick={() => result.fetchNextPage()}
      >
        {result.isFetching ? "Loading…" : result.hasNextPage ? "Load more" : "End of results"}
      </button>
    </>
  );
}
