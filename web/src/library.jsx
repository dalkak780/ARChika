import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Link, getRouteApi, useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  api,
  encoded,
  useAPI,
  useAction,
  ErrorNotice,
  Pending,
} from "./api.jsx";

const route = getRouteApi("/");
export const libraryOrigins = new Map();
const scrollPositions = new Map();
const failedThumbnails = new Map();
const infiniteResume = new Map();
function workPath(search, page, cursor) {
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
function Card({ work, search }) {
  const action = useAction();
  const thumbnailKey = `${work.id}:${work.version}`;
  const [failed, setFailed] = useState(() =>
    failedThumbnails.has(thumbnailKey),
  );
  function failThumbnail() {
    failedThumbnails.set(thumbnailKey, true);
    if (failedThumbnails.size > 512)
      failedThumbnails.delete(failedThumbnails.keys().next().value);
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
          if (libraryOrigins.size > 64)
            libraryOrigins.delete(libraryOrigins.keys().next().value);
        }}
        className="work-link"
      >
        <div className="cover">
          {failed ? (
            <span className="cover-placeholder" aria-label="Cover unavailable">
              ▧
              <small>
                {work.cover_unresolved ? "Cover needs selection" : "No preview"}
              </small>
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
          <span className={`status ${work.index_status}`}>
            {work.index_status || "registered"}
          </span>
        </div>
        <h3 title={work.title}>{work.title}</h3>
        <p title={work.relative_path}>
          {work.root_name} · {work.relative_path}
        </p>
        {work.error_code && (
          <small className="error-text">{work.error_code}</small>
        )}
      </Link>
      <button
        className="favorite-toggle"
        aria-label={`${work.favorite ? "Remove favorite" : "Add favorite"}: ${work.title}`}
        aria-pressed={work.favorite}
        disabled={action.isPending}
        onClick={() =>
          action.mutate({
            path: `/works/${encoded(work.id)}/favorite`,
            method: "PUT",
            body: { favorite: !work.favorite },
          })
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
  const roots = useAPI(["roots"], "/roots"),
    saved = useAPI(["saved-searches"], "/saved-searches"),
    action = useAction();
  useEffect(() => setQuery(search.q), [search.q]);
  const update = (patch) =>
    navigate({
      search: (previous) => ({ ...previous, ...patch, page: patch.page ?? 1 }),
    });
  function save(event) {
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
            onClick={() =>
              update({ q: search.q === "favorite:true" ? "" : "favorite:true" })
            }
          >
            Favorites
          </button>
        </div>
        <div className="segmented" aria-label="Library view">
          <button
            aria-pressed={search.view === "pages"}
            onClick={() => update({ view: "pages" })}
          >
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
            update({ q: query });
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
            <select
              value={search.root}
              onChange={(e) => update({ root: e.target.value })}
            >
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
            <select
              value={search.sort}
              onChange={(e) => update({ sort: e.target.value })}
            >
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
            Combine AND, OR, NOT and parentheses. Use quoted phrases, title:,
            path:, root:, tag:namespace:value, word: or substring:. One–two
            character terms match indexed prefixes/exact values; longer terms
            match substrings. Only accessible root paths are searched.
          </p>
          <div className="chips">
            {saved.data?.map((item) => (
              <span key={item.id}>
                <button
                  onClick={() =>
                    update({ q: item.query, sort: item.sort || "recent" })
                  }
                >
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
        <InfiniteLibrary
          key={JSON.stringify({ ...search, page: undefined })}
          search={search}
        />
      ) : (
        <PagedLibrary search={search} update={update} />
      )}
    </main>
  );
}
function PagedLibrary({ search, update }) {
  const key = JSON.stringify(search),
    result = useAPI(["works", search], workPath(search, search.page));
  useLayoutEffect(() => {
    if (result.isPending) return;
    window.scrollTo(0, scrollPositions.get(key) || 0);
    const save = () => {
      scrollPositions.set(key, window.scrollY);
      if (scrollPositions.size > 64)
        scrollPositions.delete(scrollPositions.keys().next().value);
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
                <p>
                  Change the search or ask an administrator to explicitly scan
                  an online root.
                </p>
              </div>
            )}
            <nav className="pagination" aria-label="Pages">
              <button
                disabled={search.page <= 1}
                onClick={() => update({ page: search.page - 1 })}
              >
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
                    max={Math.max(
                      1,
                      Math.ceil(result.data.total / search.limit),
                    )}
                    defaultValue={search.page}
                  />
                </label>
                <button>Go</button>
                <span>
                  of {Math.max(1, Math.ceil(result.data.total / search.limit))}
                </span>
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
function InfiniteLibrary({ search }) {
  const container = useRef(null),
    [columns, setColumns] = useState(4),
    key = JSON.stringify(search);
  const result = useInfiniteQuery({
    queryKey: ["works-infinite", search],
    initialPageParam: { page: infiniteResume.get(key)?.page || search.page },
    maxPages: 5,
    queryFn: async ({ pageParam, signal }) => ({
      ...(await api(workPath(search, pageParam.page, pageParam.cursor), {
        signal,
      })),
      pageNumber: pageParam.page,
    }),
    getNextPageParam: (last) =>
      last.next_cursor
        ? { page: last.pageNumber + 1, cursor: last.next_cursor }
        : undefined,
    getPreviousPageParam: (first) =>
      first.pageNumber > 1 ? { page: first.pageNumber - 1 } : undefined,
  });
  const items = result.data?.pages.flatMap((page) => page.items) || [];
  const virtual = useVirtualizer({
    count: Math.ceil(items.length / columns),
    getScrollElement: () => container.current,
    estimateSize: () => 312,
    overscan: 2,
    initialOffset: scrollPositions.get(key) || 0,
  });
  const rows = virtual.getVirtualItems(),
    startPage = result.data?.pages[0]?.pageNumber;
  const previousStart = useRef(startPage);
  const restored = useRef(false);
  useLayoutEffect(() => {
    if (!restored.current && startPage && container.current) {
      const capacity = Math.max(
        1,
        Math.floor(container.current.clientWidth / 180),
      );
      const actualColumns =
        capacity >= 5 ? 5 : capacity >= 4 ? 4 : capacity >= 2 ? 2 : 1;
      if (columns !== actualColumns) {
        setColumns(actualColumns);
        return;
      }
      const resume = infiniteResume.get(key);
      if (resume)
        container.current.scrollTop =
          (resume.page - startPage) * Math.ceil(search.limit / columns) * 312 +
          resume.offset;
      restored.current = true;
    }
  }, [startPage, columns, key, search.limit]);
  function saveScroll(event) {
    const top = event.currentTarget.scrollTop,
      pageHeight = Math.ceil(search.limit / columns) * 312;
    scrollPositions.set(key, top);
    if (scrollPositions.size > 64)
      scrollPositions.delete(scrollPositions.keys().next().value);
    if (startPage)
      infiniteResume.set(key, {
        page: startPage + Math.floor(top / pageHeight),
        offset: top % pageHeight,
      });
    if (infiniteResume.size > 64)
      infiniteResume.delete(infiniteResume.keys().next().value);
  }
  useLayoutEffect(() => {
    if (
      previousStart.current &&
      startPage &&
      previousStart.current !== startPage &&
      container.current
    ) {
      container.current.scrollTop +=
        (previousStart.current - startPage) *
        Math.ceil(search.limit / columns) *
        312;
    }
    previousStart.current = startPage;
  }, [startPage, columns, search.limit]);
  useEffect(() => {
    const observer = new ResizeObserver((entries) => {
      const capacity = Math.max(
        1,
        Math.floor(entries[0].contentRect.width / 180),
      );
      setColumns(capacity >= 5 ? 5 : capacity >= 4 ? 4 : capacity >= 2 ? 2 : 1);
    });
    if (container.current) observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (
      rows.at(-1)?.index >= Math.ceil(items.length / columns) - 2 &&
      result.hasNextPage &&
      !result.isFetching &&
      !result.error
    )
      result.fetchNextPage();
  }, [
    rows.at(-1)?.index,
    items.length,
    columns,
    result.hasNextPage,
    result.isFetching,
    result.error,
  ]);
  return (
    <>
      <ErrorNotice error={result.error} />
      <div className="results-count">
        {result.data?.pages[0]?.total.toLocaleString() ?? "…"} works · up to
        five result pages retained
      </div>
      <div className="toolbar">
        <button
          disabled={!result.hasPreviousPage || result.isFetching}
          onClick={() => result.fetchPreviousPage()}
        >
          Load earlier results
        </button>
        <button disabled={result.isFetching} onClick={() => result.refetch()}>
          Refresh results
        </button>
      </div>
      <div
        className="infinite-list"
        ref={container}
        onScroll={saveScroll}
        tabIndex="0"
        aria-label="Scrollable library results"
      >
        <div style={{ height: virtual.getTotalSize(), position: "relative" }}>
          {rows.map((row) => (
            <div
              key={row.key}
              className="virtual-card-row"
              style={{
                transform: `translateY(${row.start}px)`,
                gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
              }}
            >
              {items
                .slice(row.index * columns, (row.index + 1) * columns)
                .map((work) => (
                  <Card key={work.id} work={work} search={search} />
                ))}
            </div>
          ))}
        </div>
        {result.isPending && <Pending />}
      </div>
      <button
        disabled={!result.hasNextPage || result.isFetching}
        onClick={() => result.fetchNextPage()}
      >
        {result.isFetching
          ? "Loading…"
          : result.hasNextPage
            ? "Load more"
            : "End of results"}
      </button>
    </>
  );
}
