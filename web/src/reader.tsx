import i18n from "./i18n.ts";
import { useTranslation } from "react-i18next";
import type { CSSProperties, PointerEvent as ReactPointerEvent } from "react";
import type { Page, WorkDetail, LibrarySearch } from "./types.ts";
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
import { getRouteApi, useNavigate, useLocation, useRouter } from "@tanstack/react-router";
import { VirtualContinuousPages, VirtualPageOverview } from "./virtual.tsx";
import { trimOldest } from "./lru.ts";
import { useAPI, useAction, useAuth, api, encoded, ErrorNotice } from "./api.tsx";
import {
  visiblePages,
  navigatePage,
  keyAction,
  gestureAction,
  known,
  wide,
  ignoresKeys,
} from "./navigation.ts";
import { thumbnailURL } from "./images.ts";
import { pageCache, pageKey } from "./page-cache.ts";
import type { CachedPage } from "./page-cache.ts";
import { libraryOrigins } from "./library.tsx";
import { WorkMetadata } from "./admin.tsx";
import { IconButton } from "./icons.tsx";
import { ReaderPanel, PageThumbnail, PageIndicator } from "./reader-panels.tsx";

const defaultOrigin: LibrarySearch = {
  browse: "all",
  folder: "",
  q: "",
  sort: "recent",
  direction: "desc",
  root: "",
  page: 1,
  limit: 40,
  view: "pages",
};
type Neighbor = { id: string; title: string };
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
  onLoaded: (page: Page) => void;
  pointerDown: (event: ReactPointerEvent<HTMLDivElement>) => void;
  pointerUp: (event: ReactPointerEvent<HTMLDivElement>) => void;
  pointerCancel: () => void;
}
const route = getRouteApi("/read/$workId");
function isBrowserFullscreen() {
  return !!document.fullscreenElement || window.matchMedia("(display-mode: fullscreen)").matches;
}
function readAutoNext() {
  try {
    return sessionStorage.getItem("reader-auto") === "1";
  } catch {
    return false;
  }
}
function saveAutoNext(enabled: boolean) {
  try {
    if (enabled) sessionStorage.setItem("reader-auto", "1");
    else sessionStorage.removeItem("reader-auto");
  } catch {
    // Continue reading when browser storage is unavailable.
  }
}
const positions = new Map<string, number>();
const readerId =
  globalThis.crypto.randomUUID?.() ||
  Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)), (value) =>
    value.toString(16).padStart(2, "0"),
  ).join("");

function DelayedSpinner() {
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => setVisible(true), 500);
    return () => clearTimeout(timer);
  }, []);
  return visible ? <span className="reader-spinner" aria-hidden="true" /> : null;
}
function ReaderBackdrop({ workId }: { workId: string }) {
  const { t } = useTranslation();
  const [failed, setFailed] = useState(false);
  return (
    <div className="reader-backdrop" aria-busy="true" aria-label={t("reader.loadingReader")}>
      {!failed && <img src={thumbnailURL(workId)} alt="" onError={() => setFailed(true)} />}
      <DelayedSpinner />
    </div>
  );
}
const decodedPages = new WeakSet<CachedPage>();
async function decodedPage(workId: string, page: Page, width: number, signal: AbortSignal) {
  const formats = pageCache.get(pageKey(workId, page, width, true)) ? [true] : [false, true];
  for (const forcePNG of formats) {
    const key = pageKey(workId, page, width, forcePNG);
    const release = pageCache.retain(key);
    let image: CachedPage;
    try {
      image = await pageCache.load(workId, page, width, signal, forcePNG);
    } catch (error) {
      release();
      throw error;
    }
    try {
      if (!decodedPages.has(image)) {
        const decoder = new Image();
        decoder.src = image.url;
        await decoder.decode();
        decodedPages.add(image);
      }
      signal.throwIfAborted();
      return { image, key, release };
    } catch (error) {
      release();
      if (signal.aborted) throw error;
      if (forcePNG || image.compatible) break;
    }
  }
  throw new Error(i18n.t("reader.thisPageCouldNotBeDecodedIncludingItsCompatible"));
}
function PageImage({
  workId,
  page,
  width,
  onDimensions,
  className = "",
  onLoaded,
}: PageImageProps) {
  const { t } = useTranslation();
  const [display, setDisplay] = useState<{
    image: CachedPage;
    page: Page;
    key: string;
    cacheKey: string;
  } | null>(null);
  const [error, setError] = useState<unknown>(null),
    [retry, setRetry] = useState(0);
  const loadKey = `${pageKey(workId, page, width)}/${retry}`;
  const [previousLoadKey, setPreviousLoadKey] = useState(loadKey);
  if (previousLoadKey !== loadKey) {
    setPreviousLoadKey(loadKey);
    setError(null);
  }
  const pngKey = pageKey(workId, page, width, true),
    originalKey = pageKey(workId, page, width);
  const png = pageCache.get(pngKey),
    cached = png ?? pageCache.get(originalKey);
  const image =
    cached && decodedPages.has(cached)
      ? { image: cached, page, key: loadKey, cacheKey: png ? pngKey : originalKey }
      : display;
  const loading = image?.key !== loadKey;
  const notifyLoaded = useEffectEvent(() => onLoaded?.());
  useEffect(() => {
    if (!loading) notifyLoaded();
  }, [loading, loadKey]);
  const displayedKey = image?.cacheKey;
  useLayoutEffect(() => {
    if (!displayedKey) return;
    return pageCache.retain(displayedKey);
  }, [displayedKey]);
  const loadPage = useEffectEvent((signal: AbortSignal) =>
    decodedPage(workId, page, width, signal),
  );
  const showPage = useEffectEvent((value: CachedPage, cacheKey: string) =>
    setDisplay({ image: value, page, key: loadKey, cacheKey }),
  );
  useEffect(() => {
    const controller = new AbortController();
    let release: (() => void) | undefined;
    void loadPage(controller.signal)
      .then((result) => {
        if (controller.signal.aborted) {
          result.release();
          return;
        }
        release = result.release;
        showPage(result.image, result.key);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setError(error);
      });
    return () => {
      controller.abort();
      release?.();
    };
  }, [loadKey]);
  return (
    <figure
      className={`reader-image ${className}`}
      aria-busy={loading && !error}
      style={{
        aspectRatio:
          image?.page.width && image.page.height
            ? `${image.page.width}/${image.page.height}`
            : page.width && page.height
              ? `${page.width}/${page.height}`
              : "2/3",
      }}
    >
      {error ? (
        <div className="page-error">
          <ErrorNotice error={error} />
          <button onClick={() => setRetry((n) => n + 1)}>
            {t("reader.retryPage")}
            {page.ordinal + 1}
          </button>
        </div>
      ) : (
        <>
          {image && (
            <>
              <img
                src={image.image.url}
                alt={t("reader.pageNumber", { page: image.page.ordinal + 1 })}
                draggable="false"
                onLoad={(event) => {
                  onDimensions(
                    image.page.ordinal,
                    event.currentTarget.naturalWidth,
                    event.currentTarget.naturalHeight,
                  );
                }}
              />
              {image.image.fallback && (
                <figcaption>
                  {t("reader.stillFrameCompatibilityRepresentationAnimationIsNotPreserved")}
                </figcaption>
              )}
            </>
          )}
          {loading && <DelayedSpinner key={loadKey} />}
        </>
      )}
    </figure>
  );
}
export default function Reader() {
  const { t, i18n } = useTranslation();
  const { workId } = route.useParams(),
    search = route.useSearch(),
    searchStr = useLocation({ select: (location) => location.searchStr }),
    navigate = useNavigate({ from: "/read/$workId" }),
    router = useRouter();
  const { user } = useAuth(),
    action = useAction();
  const preferenceAction = useAction();
  const origin = libraryOrigins.get(workId) ?? defaultOrigin;
  const neighborQuery = new URLSearchParams({
    q: origin.q,
    sort: origin.sort,
    direction: origin.direction,
    root: origin.root,
    ...(origin.browse === "folders" && origin.root ? { folder: origin.folder } : {}),
  }).toString();
  const neighbors = useAPI<{ previous: Neighbor | null; next: Neighbor | null }>(
    ["neighbors", workId, neighborQuery],
    `/works/${encoded(workId)}/neighbors?${neighborQuery}`,
  );
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
  const [readingDirection, setReadingDirection] = useState(
    user.preferences?.reading_direction ?? "ltr",
  );
  const [preloadCount, setPreloadCount] = useState(user.preferences?.preload_count ?? 2);
  const [autoSeconds, setAutoSeconds] = useState(user.preferences?.auto_next_seconds ?? 10);
  const [auto, setAuto] = useState(readAutoNext);
  const [remaining, setRemaining] = useState(autoSeconds);
  const [overlay, setOverlay] = useState<"overview" | "options" | "help" | null>(null);
  const [editingPage, setEditingPage] = useState(false);
  const [hidden, setHidden] = useState(document.hidden);
  const resetCountdown = useCallback(() => setRemaining(autoSeconds), [autoSeconds]);
  const preferenceBody = {
    reader_mode: mode,
    layout,
    fit,
    resize_width: resize,
    reading_direction: readingDirection,
    preload_count: preloadCount,
    auto_next_seconds: autoSeconds,
  };
  const preferenceJSON = JSON.stringify(preferenceBody);
  const lastSavedPreferences = useRef(preferenceJSON);
  const savePreferences = useEffectEvent(() => {
    lastSavedPreferences.current = preferenceJSON;
    preferenceAction.mutate({
      path: "/preferences",
      method: "PUT",
      body: preferenceBody,
      invalidate: [],
    });
  });
  useEffect(() => {
    if (preferenceJSON === lastSavedPreferences.current || preferenceAction.isPending) return;
    const timer = setTimeout(() => savePreferences(), 500);
    return () => clearTimeout(timer);
  }, [preferenceJSON, preferenceAction.isPending]);
  useEffect(() => {
    const changed = () => setHidden(document.hidden);
    document.addEventListener("visibilitychange", changed);
    return () => document.removeEventListener("visibilitychange", changed);
  }, []);
  useEffect(() => {
    saveAutoNext(false);
  }, [workId]);
  const [chrome, setChrome] = useState(true),
    [metadata, setMetadata] = useState(false),
    [dimensions, setDimensions] = useState<Record<number, Dimensions>>({}),
    [browserFullscreen, setBrowserFullscreen] = useState(isBrowserFullscreen);
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
  const [loaded, setLoaded] = useState<Record<string, boolean>>({});
  const pageLoaded = useCallback(
    (page: Page) => {
      const key = pageKey(workId, page, resize);
      setLoaded((old) => (old[key] ? old : { ...old, [key]: true }));
    },
    [workId, resize],
  );
  const pageIndex = Math.max(0, Math.min(index, Math.max(0, pages.length - 1)));
  const shown = visiblePages(pages, pageIndex, layout);
  const readinessKey = JSON.stringify([workId, pageIndex, resize]);
  const [previousReadinessKey, setPreviousReadinessKey] = useState(readinessKey);
  if (previousReadinessKey !== readinessKey) {
    setPreviousReadinessKey(readinessKey);
    setLoaded({});
  }
  useEffect(() => {
    if (!pages.length) return;
    positions.set(workId, pageIndex);
    trimOldest(positions, 64);
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
      resetCountdown();
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
    [pages, pageIndex, layout, mode, resetCountdown],
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
  function jump(index: number) {
    resetCountdown();
    setIndex(index);
  }
  function backToLibrary() {
    void navigate({ to: "/", search: origin });
  }
  function openWork(neighbor: Neighbor | null | undefined, resume = false) {
    if (!neighbor) return;
    libraryOrigins.set(neighbor.id, origin);
    trimOldest(libraryOrigins, 64);
    setAuto(resume);
    resetCountdown();
    saveAutoNext(resume);
    void navigate({
      to: "/read/$workId",
      params: { workId: neighbor.id },
      search: { page: 1, immersive: search.immersive },
    });
  }
  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (overlay || metadata) return;
    if (event.key === "Escape" && !ignoresKeys(event.target)) {
      if (search.immersive) exitImmersive();
      else setChrome(true);
      return;
    }
    const action = keyAction(event, mode, readingDirection);
    if (!action) return;
    event.preventDefault();
    switch (action) {
      case "goto":
        setChrome(true);
        setEditingPage(true);
        break;
      case "fullscreen":
        void fullscreen();
        break;
      case "layout":
        setLayout((value) => (value === "single" ? "spread" : "single"));
        break;
      case "direction":
        setReadingDirection((value) => (value === "ltr" ? "rtl" : "ltr"));
        break;
      case "auto":
        resetCountdown();
        setAuto((value) => !value);
        break;
      case "overview":
      case "options":
      case "help":
        setOverlay(action);
        break;
      case "previousWork":
        openWork(neighbors.data?.previous);
        break;
      case "nextWork":
        openWork(neighbors.data?.next);
        break;
      case "back":
        backToLibrary();
        break;
      default:
        go(action);
    }
  });
  useEffect(() => {
    const key = (event: KeyboardEvent) => onKey(event);
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
  const autoAdvance = useEffectEvent(() => {
    if (remaining > 1) {
      setRemaining(remaining - 1);
      return;
    }
    if (!pages.length) {
      setAuto(false);
      return;
    }
    const last = mode === "scroll" ? pageIndex : Math.max(...shown);
    if (last < pages.length - 1) go("next");
    else if (neighbors.data?.next) openWork(neighbors.data.next, true);
    else {
      setAuto(false);
      saveAutoNext(false);
    }
  });
  const countdownKey = `${workId}:${pageIndex}:${autoSeconds}`;
  const [previousCountdownKey, setPreviousCountdownKey] = useState(countdownKey);
  if (previousCountdownKey !== countdownKey) {
    setPreviousCountdownKey(countdownKey);
    setRemaining(autoSeconds);
  }
  useEffect(() => {
    if (
      !auto ||
      hidden ||
      overlay ||
      metadata ||
      editingPage ||
      result.isPending ||
      neighbors.isPending
    )
      return;
    const timer = setInterval(() => autoAdvance(), 1000);
    return () => clearInterval(timer);
  }, [auto, hidden, overlay, metadata, editingPage, result.isPending, neighbors.isPending]);
  useEffect(() => () => pageCache.clearWork(workId), [workId]);
  useEffect(() => {
    const visible = mode === "scroll" ? [pageIndex] : shown;
    if (
      !visible.every((i) => {
        const page = pages[i];
        return page && loaded[pageKey(workId, page, resize)];
      })
    )
      return;
    const controller = new AbortController();
    const end = visible.at(-1) ?? pageIndex;
    const ahead = pages.slice(
      end + 1,
      end + 1 + preloadCount * (layout === "spread" && mode === "paged" ? 2 : 1),
    );
    const previous = pages[pageIndex - 1];
    if (previous && preloadCount > 0) ahead.push(previous);
    async function preload() {
      for (const page of ahead) {
        if (controller.signal.aborted) return;
        try {
          const result = await decodedPage(workId, page, resize, controller.signal);
          result.release();
        } catch {
          // Visible pages expose failures through their retry UI.
        }
      }
    }
    void preload();
    return () => controller.abort();
  }, [workId, pageIndex, mode, layout, shown, pages, resize, loaded, preloadCount]);
  const prefetchPage = useEffectEvent(() => pages[pageIndex]?.ordinal);
  useEffect(() => {
    const ordinal = prefetchPage();
    if (!pages.length || ordinal === undefined) return;
    const controller = new AbortController();
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
  useEffect(() => {
    // Covers both the Fullscreen API and browser-level fullscreen such as F11.
    const media = window.matchMedia("(display-mode: fullscreen)");
    function changed() {
      const next = isBrowserFullscreen();
      setBrowserFullscreen(next);
      setChrome(!next);
    }
    document.addEventListener("fullscreenchange", changed);
    media.addEventListener("change", changed);
    return () => {
      document.removeEventListener("fullscreenchange", changed);
      media.removeEventListener("change", changed);
    };
  }, []);
  async function fullscreen() {
    if (document.fullscreenElement) return document.exitFullscreen();
    if (search.immersive) return exitImmersive();
    const element = surface.current;
    try {
      if (element?.requestFullscreen) return await element.requestFullscreen();
    } catch {
      // Declined or unsupported for this element: fall back to in-page fullscreen.
    }
    immersive();
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
    const bounds = surface.current?.getBoundingClientRect();
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
        direction: readingDirection,
        zoom: zoom * (window.visualViewport?.scale || 1),
        viewportWidth: window.innerWidth,
        left: bounds?.left ?? 0,
        width: bounds?.width ?? window.innerWidth,
      },
    );
    if (action) go(action);
  }
  const fullscreenView = browserFullscreen || search.immersive;
  const zoomStyle: CSSProperties & { "--zoom": number } = { "--zoom": zoom };
  const work = result.data?.work;
  const currentPage = pages[pageIndex];
  const cachedPage = currentPage
    ? (pageCache.get(pageKey(workId, currentPage, resize, true)) ??
      pageCache.get(pageKey(workId, currentPage, resize)))
    : undefined;
  function cover(entryId: string) {
    action.mutate({
      path: `/works/${encoded(workId)}/cover`,
      method: "PUT",
      body: { entry_id: entryId },
      invalidate: [["work", workId], ["works"], ["works-infinite"]],
    });
  }
  // Read optional fields once; compiled memo dependencies must not dereference an unloaded work.
  const favorite = work?.favorite ?? false;
  const nextPage = pages[pageIndex + 1],
    lastShown = shown.at(-1) ?? pageIndex;
  return (
    <main
      id="main"
      ref={surface}
      className={`reader ${fullscreenView ? "immersive" : ""} ${chrome || !fullscreenView ? "" : "chrome-hidden"} fit-${fit}`}
    >
      <header className="reader-toolbar">
        <div className="toolbar reader-main-toolbar">
          <IconButton icon="close" label={t("reader.closeReader")} onClick={backToLibrary} />
          <strong className="reader-title" title={work?.title || t("reader.reader")}>
            {work?.title || t("reader.reader")}
          </strong>
          <IconButton
            icon={fullscreenView ? "exitFullscreen" : "fullscreen"}
            label={fullscreenView ? t("reader.exitFullscreenF") : t("reader.fullscreenF")}
            onClick={() => void fullscreen()}
          />
          <IconButton
            icon="overview"
            label={t("reader.pageOverviewQ")}
            onClick={() => setOverlay("overview")}
          />
          <IconButton
            icon="settings"
            label={t("reader.readerOptionsO")}
            onClick={() => setOverlay("options")}
          />
          <IconButton
            icon="help"
            label={t("reader.readerHelp")}
            onClick={() => setOverlay("help")}
          />
          <IconButton
            icon="favorite"
            className={favorite ? "primary" : ""}
            label={favorite ? t("library.removeFavorite") : t("library.addFavorite")}
            aria-pressed={favorite}
            disabled={!work || action.isPending}
            onClick={() =>
              action.mutate({
                path: `/works/${encoded(workId)}/favorite`,
                method: "PUT",
                body: { favorite: !favorite },
                invalidate: [["work", workId], ["works"], ["works-infinite"]],
              })
            }
          />
          <IconButton
            icon="metadata"
            label={t("reader.workMetadata")}
            onClick={() => setMetadata(true)}
          />
        </div>
      </header>
      {overlay === "options" && (
        <ReaderPanel title={t("reader.readerOptions")} close={() => setOverlay(null)}>
          <div className="reader-settings">
            <label>
              {t("reader.mode")}
              <select
                value={mode}
                onChange={(e) => setMode(e.target.value === "scroll" ? "scroll" : "paged")}
              >
                <option value="paged">{t("reader.paged")}</option>
                <option value="scroll">{t("reader.continuous")}</option>
              </select>
            </label>
            <label>
              {t("reader.layout")}
              <select
                value={layout}
                onChange={(e) => setLayout(e.target.value === "spread" ? "spread" : "single")}
              >
                <option value="single">{t("reader.single")}</option>
                <option value="spread">{t("reader.twoPageSpread")}</option>
              </select>
            </label>
            <label>
              {t("reader.fit")}
              <select
                value={fit}
                onChange={(e) =>
                  setFit(
                    e.target.value === "height"
                      ? "height"
                      : e.target.value === "width"
                        ? "width"
                        : e.target.value === "original"
                          ? "original"
                          : "viewport",
                  )
                }
              >
                <option value="viewport">{t("reader.viewport")}</option>
                <option value="width">{t("reader.width")}</option>
                <option value="height">{t("reader.height")}</option>
                <option value="original">{t("reader.original")}</option>
              </select>
            </label>
            <label>
              {t("reader.zoom")}
              <select value={zoom} onChange={(e) => setZoom(Number(e.target.value))}>
                {[0.75, 1, 1.25, 1.5, 2, 3].map((value) => (
                  <option key={value} value={value}>
                    {Math.round(value * 100)}%
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t("reader.resize")}
              <select value={resize} onChange={(e) => setResize(Number(e.target.value))}>
                <option value="0">{t("reader.originalDimensions")}</option>
                {[800, 1200, 1600, 2400].map((value) => (
                  <option key={value} value={value}>
                    {value}
                    {t("reader.pxWide")}
                  </option>
                ))}
              </select>
            </label>

            <label>
              {t("reader.readingDirection")}
              <select
                value={readingDirection}
                onChange={(event) =>
                  setReadingDirection(event.target.value === "rtl" ? "rtl" : "ltr")
                }
              >
                <option value="ltr">{t("reader.leftToRight")}</option>
                <option value="rtl">{t("reader.rightToLeft")}</option>
              </select>
            </label>
            <label>
              {t("reader.preloadPages")}
              <input
                type="number"
                min={0}
                max={10}
                value={preloadCount}
                onChange={(event) => {
                  const value = event.currentTarget.valueAsNumber;
                  if (Number.isInteger(value) && value >= 0 && value <= 10) setPreloadCount(value);
                }}
              />
            </label>
            <label>
              {t("reader.autoNextIntervalSeconds")}
              <input
                type="number"
                min={2}
                max={120}
                value={autoSeconds}
                onChange={(event) => {
                  const value = event.currentTarget.valueAsNumber;
                  if (Number.isInteger(value) && value >= 2 && value <= 120) setAutoSeconds(value);
                }}
              />
            </label>
          </div>
          <p className="muted small reader-options-status" role="status">
            {preferenceAction.isPending
              ? t("reader.savingPreferences")
              : t("reader.preferencesSaveAutomaticallyZoomAppliesToThisSession")}
          </p>
          <ErrorNotice error={preferenceAction.error} />
          {currentPage && (
            <a
              href={`/api/works/${encoded(workId)}/pages/${currentPage.ordinal}`}
              target="_blank"
              rel="noreferrer"
            >
              {t("reader.openOriginal")}
            </a>
          )}
        </ReaderPanel>
      )}
      {overlay === "overview" && (
        <ReaderPanel title={t("reader.pageOverview")} close={() => setOverlay(null)}>
          <button disabled={action.isPending} onClick={() => cover("")}>
            {t("reader.resetCover")}
          </button>
          <ErrorNotice error={action.error} />
          <VirtualPageOverview
            pages={pages}
            index={pageIndex}
            renderPage={(page, index) => (
              <div className={`overview-tile ${index === pageIndex ? "current" : ""}`}>
                <button
                  className="thumbnail-button"
                  aria-label={t("reader.goToPageNumber", { page: index + 1 })}
                  aria-current={index === pageIndex ? "page" : undefined}
                  onClick={() => {
                    jump(index);
                    setOverlay(null);
                  }}
                >
                  <PageThumbnail workId={workId} page={page} />
                  <span>{index + 1}</span>
                </button>
                <button
                  className="cover-button"
                  disabled={action.isPending}
                  onClick={() => cover(page.entry_id)}
                  aria-label={t("reader.usePageAsCover", { page: index + 1 })}
                >
                  {t("reader.useAsCover")}
                </button>
              </div>
            )}
          />
        </ReaderPanel>
      )}
      {overlay === "help" && (
        <ReaderPanel title={t("reader.readerHelpText")} close={() => setOverlay(null)}>
          <dl className="reader-shortcuts">
            <dt>← / →, A / D</dt>
            <dd>{t("reader.previousNextPageReversedInRightToLeftMode")}</dd>
            <dt>Space / Shift+Space</dt>
            <dd>{t("reader.nextPreviousPage")}</dd>
            <dt>Home / End</dt>
            <dd>{t("reader.firstLastPageShiftShiftAlsoJumpToThe")}</dd>
            <dt>G</dt>
            <dd>{t("reader.goToPageEnterJumpsEscapeCancels")}</dd>
            <dt>F / P / M / N</dt>
            <dd>{t("reader.fullscreenSingleOrSpreadReadingDirectionAutoNext")}</dd>
            <dt>Q / O / ?</dt>
            <dd>{t("reader.overviewOptionsHelpEscapeOrAClickOutsideCloses")}</dd>
            <dt>, / .</dt>
            <dd>{t("reader.previousNextWorkInTheLibrarySearch")}</dd>
            <dt>Backspace</dt>
            <dd>{t("reader.returnToTheLibrary")}</dd>
          </dl>
          <p>{t("reader.tapTheLeftOrRightEdgeToTurnPages")}</p>
          <p>{t("reader.autoNextPausesWhileAPanelIsOpenOr")}</p>
        </ReaderPanel>
      )}
      <button
        className="reveal-chrome"
        onClick={() => setChrome((value) => !value)}
        aria-label={chrome ? t("reader.hideControls") : t("reader.showReaderControls")}
      >
        {chrome ? t("reader.hideControls") : t("reader.controls")}
      </button>
      <ErrorNotice
        error={result.error || action.error || preferenceAction.error || neighbors.error}
      />
      {result.isPending && <ReaderBackdrop key={workId} workId={workId} />}
      {metadata && (
        <ReaderPanel title={t("reader.workMetadata")} close={() => setMetadata(false)}>
          <WorkMetadata workId={workId} />
        </ReaderPanel>
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
          {t("reader.retryPageIndexing")}
        </button>
      )}
      {pages.length ? (
        <>
          {mode === "scroll" ? (
            <ContinuousReader
              workId={workId}
              pages={pages}
              index={pageIndex}
              setIndex={jump}
              resize={resize}
              zoom={zoom}
              onDimensions={dimensionsLoaded}
              onLoaded={pageLoaded}
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
                {visiblePages(pages, pageIndex, layout, readingDirection).map((i, slot) => {
                  const page = pages[i];
                  return page ? (
                    <PageImage
                      key={`${workId}-${slot}`}
                      workId={workId}
                      page={page}
                      width={resize}
                      onDimensions={dimensionsLoaded}
                      onLoaded={() => pageLoaded(page)}
                    />
                  ) : null;
                })}
              </div>
              {layout === "spread" &&
                known(pages[pageIndex]) &&
                !wide(pages[pageIndex]) &&
                nextPage &&
                !known(nextPage) && (
                  <div className="dimension-probe" aria-hidden="true">
                    <PageImage
                      workId={workId}
                      page={nextPage}
                      width={resize}
                      onDimensions={dimensionsLoaded}
                    />
                  </div>
                )}
            </div>
          )}
          <footer className="reader-footer">
            <nav
              className="reader-navigation"
              aria-label={t("reader.pageNavigation")}
              dir={mode === "paged" ? readingDirection : "ltr"}
            >
              {neighbors.data?.previous && (
                <IconButton
                  icon="previousWork"
                  label={t("reader.previousWork", { title: neighbors.data.previous.title })}
                  onClick={() => openWork(neighbors.data?.previous)}
                />
              )}
              <IconButton
                icon="first"
                label={t("reader.firstPageHome")}
                disabled={pageIndex === 0}
                onClick={() => go("home")}
              />
              <IconButton
                icon="previous"
                label={t("reader.previousPage")}
                disabled={pageIndex === 0}
                onClick={() => go("previous")}
              />
              <PageIndicator
                index={pageIndex}
                count={pages.length}
                editing={editingPage}
                setEditing={setEditingPage}
                jump={jump}
              />
              <IconButton
                icon="next"
                label={t("reader.nextPage")}
                disabled={(mode === "paged" ? lastShown : pageIndex) >= pages.length - 1}
                onClick={() => go("next")}
              />
              <IconButton
                icon="last"
                label={t("reader.lastPageEnd")}
                disabled={(mode === "paged" ? lastShown : pageIndex) >= pages.length - 1}
                onClick={() => go("end")}
              />
              {neighbors.data?.next && (
                <IconButton
                  icon="nextWork"
                  label={t("reader.nextWork", { title: neighbors.data.next.title })}
                  onClick={() => openWork(neighbors.data?.next)}
                />
              )}
            </nav>
            <div className="reader-file-row">
              <small className="reader-file-info" title={currentPage?.entry}>
                {currentPage?.entry}
                {currentPage?.width && currentPage.height
                  ? ` · ${currentPage.width} × ${currentPage.height}`
                  : ""}
                {cachedPage
                  ? ` · ${(cachedPage.blob.size / 1024).toLocaleString(i18n.language, { minimumFractionDigits: 1, maximumFractionDigits: 1, useGrouping: false })} KiB`
                  : ""}
              </small>
              <IconButton
                icon={auto ? "pause" : "play"}
                label={auto ? t("reader.pauseAutoNextN") : t("reader.startAutoNextN")}
                aria-pressed={auto}
                onClick={() => {
                  resetCountdown();
                  setAuto((value) => !value);
                }}
              >
                {auto ? t("reader.seconds", { count: remaining }) : null}
              </IconButton>
            </div>
          </footer>
        </>
      ) : (
        !result.isPending &&
        !result.error && <div className="empty">{t("reader.thisWorkHasNoReadablePages")}</div>
      )}
    </main>
  );
}

function ContinuousReader(props: ContinuousReaderProps) {
  const container = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(800);
  useLayoutEffect(() => {
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry) setWidth(entry.contentRect.width);
    });
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
          onLoaded={() => props.onLoaded(page)}
        />
      )}
    />
  );
}
