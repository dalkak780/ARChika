import type { CSSProperties, PointerEvent as ReactPointerEvent } from "react";
import type { Page, WorkDetail } from "./types.ts";
import type { Dimensions, GesturePoint, NavigationAction } from "./navigation.ts";
import React, {
  useCallback,
  useEffect,
  useEffectEvent,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Link, getRouteApi, useNavigate, useLocation, useRouter } from "@tanstack/react-router";
import { VirtualContinuousPages } from "./virtual.tsx";
import { useAPI, useAction, useAuth, api, encoded, ErrorNotice, Pending } from "./api.tsx";
import {
  visiblePages,
  navigatePage,
  keyAction,
  gestureAction,
  known,
  wide,
  ignoresKeys,
} from "./navigation.ts";
import { pageBlob } from "./images.ts";
import { libraryOrigins } from "./library.tsx";
import { WorkMetadata } from "./admin.tsx";
type OnDimensions = (ordinal: number, width: number, height: number) => void;
interface PageImageProps {
  workId: string;
  page: Page;
  width: number;
  onDimensions: OnDimensions;
  className?: string;
  onLoaded?: () => void;
}
interface ContinuousReaderProps {
  workId: string;
  pages: Page[];
  index: number;
  setIndex: (index: number) => void;
  resize: number;
  zoom: number;
  onDimensions: OnDimensions;
  pointerDown: (event: ReactPointerEvent<HTMLDivElement>) => void;
  pointerUp: (event: ReactPointerEvent<HTMLDivElement>) => void;
  pointerCancel: () => void;
}
const route = getRouteApi("/read/$workId");
const positions = new Map<string, number>();
const readerId =
  globalThis.crypto.randomUUID?.() ||
  Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)), (value) =>
    value.toString(16).padStart(2, "0"),
  ).join("");

function PageImage({
  workId,
  page,
  width,
  onDimensions,
  className = "",
  onLoaded,
}: PageImageProps) {
  const [image, setImage] = useState<
      (Awaited<ReturnType<typeof pageBlob>> & { url: string }) | null
    >(null),
    [error, setError] = useState<unknown>(null),
    [retry, setRetry] = useState(0);
  const requestController = useRef<AbortController | null>(null),
    fallbackAttempted = useRef(false);
  const loadKey = `${workId}/${page.entry_id}/${page.ordinal}/${width}/${retry}`;
  const [previousLoadKey, setPreviousLoadKey] = useState(loadKey);
  if (previousLoadKey !== loadKey) {
    setPreviousLoadKey(loadKey);
    setImage(null);
    setError(null);
  }
  const loadPage = useEffectEvent((signal: AbortSignal) => pageBlob(workId, page, width, signal));
  useEffect(() => {
    const controller = new AbortController();
    let objectURL: string | undefined;
    requestController.current = controller;
    fallbackAttempted.current = false;
    async function load() {
      try {
        const result = await loadPage(controller.signal);
        if (controller.signal.aborted) return;
        objectURL = URL.createObjectURL(result.blob);
        setImage({ ...result, url: objectURL });
      } catch (error) {
        if (!controller.signal.aborted) setError(error);
      }
    }
    void load();
    return () => {
      controller.abort();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [loadKey]);
  useEffect(
    () => () => {
      if (image?.url) URL.revokeObjectURL(image.url);
    },
    [image?.url],
  );
  async function decodeError() {
    if (fallbackAttempted.current || image?.compatible) {
      setError(
        new Error(
          "This page could not be decoded, including its compatible representation. Other pages remain available.",
        ),
      );
      return;
    }
    fallbackAttempted.current = true;
    try {
      const controller = requestController.current;
      if (!controller) return;
      const result = await pageBlob(workId, page, width, controller.signal, true);
      if (controller.signal.aborted) return;
      setImage({ ...result, url: URL.createObjectURL(result.blob) });
    } catch (error) {
      if (!requestController.current?.signal.aborted) setError(error);
    }
  }
  return (
    <figure
      className={`reader-image ${className}`}
      style={{
        aspectRatio: page.width && page.height ? `${page.width}/${page.height}` : "2/3",
      }}
    >
      {error ? (
        <div className="page-error">
          <ErrorNotice error={error} />
          <button onClick={() => setRetry((n) => n + 1)}>Retry page {page.ordinal + 1}</button>
        </div>
      ) : image ? (
        <>
          <img
            src={image.url}
            alt={`Page ${page.ordinal + 1}`}
            draggable="false"
            onLoad={(event) => {
              onDimensions(
                page.ordinal,
                event.currentTarget.naturalWidth,
                event.currentTarget.naturalHeight,
              );
              onLoaded?.();
            }}
            onError={decodeError}
          />
          {image.fallback && (
            <figcaption>
              Still-frame compatibility representation; animation is not preserved.
            </figcaption>
          )}
        </>
      ) : (
        <div className="page-loading" role="status">
          Loading page {page.ordinal + 1}…
        </div>
      )}
    </figure>
  );
}
export default function Reader() {
  const { workId } = route.useParams(),
    search = route.useSearch(),
    searchStr = useLocation({ select: (location) => location.searchStr }),
    navigate = useNavigate({ from: "/read/$workId" }),
    router = useRouter();
  const { user } = useAuth(),
    action = useAction();
  const result = useAPI<WorkDetail>(["work", workId], `/works/${encoded(workId)}`, {
    staleTime: 60_000,
  });
  const urlPage = new URLSearchParams(searchStr).has("page") ? search.page : null;
  const [index, setIndex] = useState(() =>
    urlPage === null ? (positions.get(workId) ?? 0) : urlPage - 1,
  );
  const [location, setLocation] = useState({ workId, page: urlPage });
  if (location.workId !== workId || location.page !== urlPage) {
    setLocation({ workId, page: urlPage });
    setIndex(urlPage === null ? (positions.get(workId) ?? 0) : urlPage - 1);
  }
  const [mode, setMode] = useState(user.preferences?.reader_mode || "paged"),
    [layout, setLayout] = useState(user.preferences?.layout || "single");
  const [fit, setFit] = useState(user.preferences?.fit || "viewport"),
    [resize, setResize] = useState(user.preferences?.resize_width || 0),
    [zoom, setZoom] = useState(1);
  const [chrome, setChrome] = useState(true),
    [metadata, setMetadata] = useState(false),
    [dimensions, setDimensions] = useState<Record<number, Dimensions>>({}),
    [fullscreenError, setFullscreenError] = useState("");
  const enteredImmersive = useRef(false),
    surface = useRef<HTMLElement>(null),
    pointer = useRef<(GesturePoint & { id: number; type: string }) | null>(null),
    lastNavigation = useRef<{ time: number; index: number } | null>(null),
    velocity = useRef(0),
    direction = useRef(1);
  const [dimensionsWorkId, setDimensionsWorkId] = useState(workId);
  if (dimensionsWorkId !== workId) {
    setDimensionsWorkId(workId);
    setDimensions({});
  }
  const pages = useMemo(
    () =>
      (result.data?.pages || []).map((page) => ({
        ...page,
        ...dimensions[page.ordinal],
      })),
    [result.data?.pages, dimensions],
  );
  const pageIndex = Math.max(0, Math.min(index, Math.max(0, pages.length - 1)));
  const shown = visiblePages(pages, pageIndex, layout);
  useEffect(() => {
    if (!pages.length) return;
    positions.set(workId, pageIndex);
    if (positions.size > 64) positions.delete(positions.keys().next().value!);
    if (urlPage !== pageIndex + 1)
      void navigate({
        search: (old) => ({ ...old, page: pageIndex + 1 }),
        replace: true,
      });
  }, [workId, pageIndex, pages.length, urlPage, navigate]);
  useEffect(() => {
    lastNavigation.current = { time: performance.now(), index: pageIndex };
  }, [workId, pageIndex]);
  const dimensionsLoaded = useCallback(
    (ordinal: number, width: number, height: number) =>
      setDimensions((old) =>
        old[ordinal]?.width === width && old[ordinal]?.height === height
          ? old
          : { ...old, [ordinal]: { width, height } },
      ),
    [],
  );
  const go = useCallback(
    (action: NavigationAction | "chrome") => {
      if (action === "chrome") {
        setChrome((value) => !value);
        return;
      }
      const next = navigatePage(
        pages,
        pageIndex,
        action,
        mode === "scroll" ? "single" : layout,
        mode === "scroll" ? [pageIndex] : visiblePages(pages, pageIndex, layout),
      );
      const now = performance.now();
      direction.current = next >= pageIndex ? 1 : -1;
      const previous = lastNavigation.current ?? { time: now, index: pageIndex };
      velocity.current = Math.min(
        20,
        Math.abs(next - previous.index) / Math.max(0.1, (now - previous.time) / 1000),
      );
      lastNavigation.current = { time: now, index: next };
      setIndex(next);
    },
    [pages, pageIndex, layout, mode],
  );
  function exitImmersive() {
    setChrome(true);
    if (enteredImmersive.current) {
      enteredImmersive.current = false;
      router.history.back();
    } else
      void navigate({
        search: (old) => ({ ...old, immersive: false }),
        replace: true,
      });
  }
  function immersive() {
    if (search.immersive) return exitImmersive();
    enteredImmersive.current = true;
    setChrome(false);
    void navigate({
      search: (old) => ({ ...old, page: pageIndex + 1, immersive: true }),
    });
  }
  const exitImmersiveOnKey = useEffectEvent(exitImmersive);
  useEffect(() => {
    function key(event: KeyboardEvent) {
      if (event.key === "Escape" && (metadata || !ignoresKeys(event.target))) {
        if (metadata) setMetadata(false);
        else if (search.immersive) exitImmersiveOnKey();
        else setChrome(true);
        return;
      }
      const action = keyAction(event, mode);
      if (action && !metadata) {
        event.preventDefault();
        go(action);
      }
    }
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [go, mode, metadata, search.immersive]);
  const prefetchPage = useEffectEvent(() => pages[pageIndex]!.ordinal);
  useEffect(() => {
    if (!pages.length) return;
    const controller = new AbortController();
    const ordinal = prefetchPage();
    const timer = setTimeout(
      () =>
        api(`/works/${encoded(workId)}/prefetch`, {
          method: "POST",
          signal: controller.signal,
          body: {
            reader_id: readerId,
            page: ordinal,
            direction: direction.current,
            velocity: velocity.current,
            visible: mode === "scroll" ? 1 : shown.length,
            byte_budget: 16 * 1024 * 1024,
            pressure:
              "connection" in navigator &&
              navigator.connection &&
              typeof navigator.connection === "object" &&
              "saveData" in navigator.connection &&
              navigator.connection.saveData
                ? 0.9
                : 0,
          },
        }).catch(() => {}),
      220,
    );
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [workId, pageIndex, mode, layout, shown.length, pages.length]);
  useEffect(
    () => () => {
      api(`/works/${encoded(workId)}/prefetch?reader_id=${encoded(readerId)}`, {
        method: "DELETE",
      }).catch(() => {});
    },
    [workId],
  );
  async function fullscreen() {
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else if (surface.current?.requestFullscreen) await surface.current.requestFullscreen();
      else setFullscreenError("Browser fullscreen is unavailable. Immersive reading still works.");
    } catch {
      setFullscreenError("Fullscreen was declined. Immersive reading still works.");
    }
  }
  function preferences() {
    action.mutate({
      path: "/preferences",
      invalidate: [],
      method: "PUT",
      body: { reader_mode: mode, layout, resize_width: Number(resize), fit },
    });
  }
  function pointerCancel() {
    pointer.current = null;
  }
  function ignoresPointer(target: EventTarget | null) {
    return (
      ignoresKeys(target) ||
      (target instanceof Element &&
        !!target.closest('input, button, a, dialog, [role="dialog"], [role="button"]'))
    );
  }
  function pointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    if (ignoresPointer(event.target)) return;
    if (pointer.current && pointer.current.id !== event.pointerId) {
      pointer.current.cancelled = true;
      return;
    }
    pointer.current = {
      id: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      time: performance.now(),
      touches: event.isPrimary ? 1 : 2,
      type: event.pointerType,
    };
  }
  function pointerUp(event: ReactPointerEvent<HTMLDivElement>) {
    const start = pointer.current;
    pointer.current = null;
    if (
      !start ||
      start.id !== event.pointerId ||
      ignoresPointer(event.target) ||
      window.getSelection()?.toString()
    )
      return;
    const action = gestureAction(
      start,
      {
        x: event.clientX,
        y: event.clientY,
        time: performance.now(),
        touches: 1,
      },
      {
        mode,
        zoom: zoom * (window.visualViewport?.scale || 1),
        viewportWidth: window.innerWidth,
      },
    );
    if (action === "chrome" && start.type === "mouse" && mode !== "scroll") {
      const bounds = surface.current!.getBoundingClientRect(),
        position = (event.clientX - bounds.left) / bounds.width;
      go(position < 0.23 ? "previous" : position > 0.77 ? "next" : "chrome");
    } else if (action) go(action);
  }
  const zoomStyle: CSSProperties & { "--zoom": number } = { "--zoom": zoom };
  const origin = libraryOrigins.get(workId),
    work = result.data?.work;
  // Read optional fields once; compiled memo dependencies must not dereference an unloaded work.
  const favorite = work?.favorite ?? false;
  return (
    <main
      id="main"
      ref={surface}
      className={`reader ${search.immersive ? "immersive" : ""} ${chrome || !search.immersive ? "" : "chrome-hidden"} fit-${fit}`}
    >
      <header className="reader-toolbar">
        <div className="toolbar">
          <button
            onClick={() =>
              search.immersive
                ? exitImmersive()
                : origin
                  ? router.history.back()
                  : navigate({ to: "/" })
            }
          >
            {search.immersive ? "Exit immersive" : "← Close"}
          </button>
          <Link to="/" search={origin || {}}>
            Library
          </Link>
          <strong className="reader-title">{work?.title || "Reader"}</strong>
          <button onClick={immersive}>{search.immersive ? "Normal view" : "Immersive"}</button>
          <button onClick={fullscreen}>Browser fullscreen</button>
        </div>
        <div className="toolbar reader-options">
          <label>
            Mode
            <select
              value={mode}
              onChange={(e) => setMode(e.target.value === "scroll" ? "scroll" : "paged")}
            >
              <option value="paged">Paged</option>
              <option value="scroll">Continuous</option>
            </select>
          </label>
          <label>
            Layout
            <select
              value={layout}
              onChange={(e) => setLayout(e.target.value === "spread" ? "spread" : "single")}
            >
              <option value="single">Single</option>
              <option value="spread">Two-page spread</option>
            </select>
          </label>
          <label>
            Fit
            <select
              value={fit}
              onChange={(e) =>
                setFit(
                  e.target.value === "width"
                    ? "width"
                    : e.target.value === "original"
                      ? "original"
                      : "viewport",
                )
              }
            >
              <option value="viewport">Viewport</option>
              <option value="width">Width</option>
            </select>
          </label>
          <label>
            Zoom
            <select value={zoom} onChange={(e) => setZoom(Number(e.target.value))}>
              {[0.75, 1, 1.25, 1.5, 2, 3].map((value) => (
                <option key={value} value={value}>
                  {Math.round(value * 100)}%
                </option>
              ))}
            </select>
          </label>
          <label>
            Resize
            <select value={resize} onChange={(e) => setResize(Number(e.target.value))}>
              <option value="0">Original dimensions</option>
              {[800, 1200, 1600, 2400].map((value) => (
                <option key={value} value={value}>
                  {value}px wide
                </option>
              ))}
            </select>
          </label>
          <>
            <button onClick={preferences} disabled={action.isPending}>
              Save preferences
            </button>
            <button
              disabled={!work || action.isPending}
              onClick={() =>
                action.mutate({
                  path: `/works/${encoded(workId)}/favorite`,
                  method: "PUT",
                  body: { favorite: !favorite },
                  invalidate: [["work", workId], ["works"], ["works-infinite"]],
                })
              }
            >
              {favorite ? "Remove favorite" : "Add favorite"}
            </button>
          </>
          <button onClick={() => setMetadata((value) => !value)} aria-expanded={metadata}>
            Metadata
          </button>
          {pages.length > 0 && (
            <>
              <button
                onClick={() =>
                  action.mutate({
                    path: `/works/${encoded(workId)}/cover`,
                    method: "PUT",
                    invalidate: [["work", workId], ["works"], ["works-infinite"]],
                    body: { entry_id: pages[pageIndex]!.entry_id },
                  })
                }
              >
                Use page as cover
              </button>
              <button
                onClick={() =>
                  action.mutate({
                    path: `/works/${encoded(workId)}/cover`,
                    method: "PUT",
                    invalidate: [["work", workId], ["works"], ["works-infinite"]],
                    body: { entry_id: "" },
                  })
                }
              >
                Default cover
              </button>
            </>
          )}
        </div>
      </header>
      <button
        className="reveal-chrome"
        onClick={() => setChrome((value) => !value)}
        aria-label={chrome ? "Hide controls" : "Show reader controls"}
      >
        {chrome ? "Hide controls" : "☰ Controls / Exit"}
      </button>
      {fullscreenError && <p className="notice">{fullscreenError}</p>}
      <ErrorNotice error={result.error || action.error} />
      {result.isPending && <Pending>Indexing pages on demand…</Pending>}
      {metadata && (
        <section className="metadata-panel panel" role="dialog" aria-label="Work metadata">
          <button onClick={() => setMetadata(false)}>Close metadata</button>
          <WorkMetadata workId={workId} />
        </section>
      )}
      {result.error && user.admin && (
        <button
          onClick={() =>
            action.mutate({
              path: `/works/${encoded(workId)}/retry`,
              invalidate: [["work", workId], ["works"], ["works-infinite"]],
            })
          }
        >
          Retry page indexing
        </button>
      )}
      {pages.length ? (
        <>
          {mode === "scroll" ? (
            <ContinuousReader
              workId={workId}
              pages={pages}
              index={pageIndex}
              setIndex={setIndex}
              resize={resize}
              zoom={zoom}
              onDimensions={dimensionsLoaded}
              pointerDown={pointerDown}
              pointerUp={pointerUp}
              pointerCancel={pointerCancel}
            />
          ) : (
            <div
              className="paged-surface"
              onPointerDown={pointerDown}
              onPointerUp={pointerUp}
              onPointerCancel={pointerCancel}
              onLostPointerCapture={pointerCancel}
            >
              <div className={`spread ${shown.length === 2 ? "two" : ""}`} style={zoomStyle}>
                {shown.map((i) => (
                  <PageImage
                    key={`${workId}-${pages[i]!.entry_id}-${pages[i]!.ordinal}`}
                    workId={workId}
                    page={pages[i]!}
                    width={resize}
                    onDimensions={dimensionsLoaded}
                  />
                ))}
              </div>
              {layout === "spread" &&
                known(pages[pageIndex]) &&
                !wide(pages[pageIndex]) &&
                pages[pageIndex + 1] &&
                !known(pages[pageIndex + 1]) && (
                  <div className="dimension-probe" aria-hidden="true">
                    <PageImage
                      workId={workId}
                      page={pages[pageIndex + 1]!}
                      width={resize}
                      onDimensions={dimensionsLoaded}
                    />
                  </div>
                )}
            </div>
          )}
          <footer className="reader-footer">
            <button onClick={() => go("previous")} disabled={pageIndex === 0}>
              ← Previous
            </button>
            <label>
              Page
              <input
                type="number"
                min="1"
                max={pages.length}
                value={pageIndex + 1}
                onChange={(event) => {
                  const value = Number(event.target.value);
                  if (value >= 1 && value <= pages.length) setIndex(value - 1);
                }}
              />
            </label>
            <span role="status">
              {mode === "paged" && shown.length > 1 ? `–${shown.at(-1)! + 1} ` : ""}
              of {pages.length}
            </span>
            <button
              onClick={() => go("next")}
              disabled={(mode === "paged" ? shown.at(-1)! : pageIndex) >= pages.length - 1}
            >
              Next →
            </button>
          </footer>
        </>
      ) : (
        !result.isPending &&
        !result.error && <div className="empty">This work has no readable pages.</div>
      )}
    </main>
  );
}

function ContinuousReader(props: ContinuousReaderProps) {
  const container = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(800);
  useLayoutEffect(() => {
    const observer = new ResizeObserver((entries) => setWidth(entries[0]!.contentRect.width));
    if (container.current) observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  return (
    <VirtualContinuousPages
      pages={props.pages}
      index={props.index}
      setIndex={props.setIndex}
      zoom={props.zoom}
      pointerDown={props.pointerDown}
      pointerUp={props.pointerUp}
      pointerCancel={props.pointerCancel}
      container={container}
      width={width}
      renderPage={(page) => (
        <PageImage
          workId={props.workId}
          page={page}
          width={props.resize}
          onDimensions={props.onDimensions}
        />
      )}
    />
  );
}
