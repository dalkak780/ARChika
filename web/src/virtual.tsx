import type { PointerEvent as ReactPointerEvent, ReactNode, RefObject } from "react";
import type { Page, Work } from "./types.ts";
import React, { Fragment, useEffect, useEffectEvent, useLayoutEffect, useRef } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

// TanStack Virtual's useVirtualizer returns mutable methods that React Compiler cannot
// memoize safely. Its callers live here, opted out with "use no memo", so the rest of the
// app stays compiled. vite.config.ts disables react/incompatible-library for this file only.

export function VirtualLibraryRows({
  container,
  items,
  columns,
  initialOffset,
  renderItem,
  hasNextPage,
  isFetching,
  error,
  fetchNextPage,
}: {
  container: RefObject<HTMLDivElement | null>;
  items: Work[];
  columns: number;
  initialOffset: number;
  renderItem: (work: Work) => ReactNode;
  hasNextPage: boolean;
  isFetching: boolean;
  error: Error | null;
  fetchNextPage: () => Promise<unknown>;
}) {
  // TanStack Virtual exposes mutable methods that React Compiler cannot safely memoize.
  "use no memo";
  const virtual = useVirtualizer({
    count: Math.ceil(items.length / columns),
    getScrollElement: () => container.current,
    estimateSize: () => 312,
    overscan: 2,
    initialOffset,
  });
  const rows = virtual.getVirtualItems();
  const lastRowIndex = rows.at(-1)?.index;
  const fetchMore = useEffectEvent(fetchNextPage);
  useEffect(() => {
    if (
      (lastRowIndex ?? -Infinity) >= Math.ceil(items.length / columns) - 2 &&
      hasNextPage &&
      !isFetching &&
      !error
    )
      void fetchMore();
  }, [lastRowIndex, items.length, columns, hasNextPage, isFetching, error]);
  return (
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
          {items.slice(row.index * columns, (row.index + 1) * columns).map((work) => (
            <Fragment key={work.id}>{renderItem(work)}</Fragment>
          ))}
        </div>
      ))}
    </div>
  );
}

export function VirtualContinuousPages({
  pages,
  index,
  setIndex,
  zoom,
  pointerDown,
  pointerUp,
  pointerCancel,
  container,
  width,
  renderPage,
}: {
  pages: Page[];
  index: number;
  setIndex: (index: number) => void;
  zoom: number;
  pointerDown: (event: ReactPointerEvent<HTMLDivElement>) => void;
  pointerUp: (event: ReactPointerEvent<HTMLDivElement>) => void;
  pointerCancel: () => void;
  container: RefObject<HTMLDivElement | null>;
  width: number;
  renderPage: (page: Page) => ReactNode;
}) {
  // TanStack Virtual exposes mutable methods that React Compiler cannot safely memoize.
  "use no memo";
  const programmatic = useRef(false),
    lastReported = useRef(index);
  const virtual = useVirtualizer({
    count: pages.length,
    getScrollElement: () => container.current,
    estimateSize: (i) => {
      const page = pages[i];
      return Math.max(
        window.innerHeight * 0.35,
        page?.width && page.height ? (width * zoom * page.height) / page.width + 16 : width * 1.4,
      );
    },
    overscan: 1,
    getItemKey: (i) => {
      const page = pages[i];
      return page ? page.entry_id || page.ordinal : i;
    },
  });
  const scrollToCurrentIndex = useEffectEvent(() => {
    virtual.scrollToIndex(index, { align: "start" });
  });
  useLayoutEffect(() => {
    scrollToCurrentIndex();
  }, []);
  useEffect(() => {
    if (lastReported.current !== index) {
      programmatic.current = true;
      scrollToCurrentIndex();
      lastReported.current = index;
      requestAnimationFrame(() => {
        programmatic.current = false;
      });
    }
  }, [index]);
  const rows = virtual.getVirtualItems();
  function scroll() {
    if (programmatic.current) return;
    const element = container.current;
    if (!element) return;
    const offset = element.scrollTop;
    const current = rows.find((row) => row.end > offset + 30);
    if (current && current.index !== lastReported.current) {
      lastReported.current = current.index;
      setIndex(current.index);
    }
  }
  return (
    <div
      className="continuous-surface"
      ref={container}
      onScroll={scroll}
      onPointerDown={pointerDown}
      onPointerUp={pointerUp}
      onPointerCancel={pointerCancel}
      onLostPointerCapture={pointerCancel}
      tabIndex={0}
      aria-label="Continuous reading surface"
    >
      <div
        style={{
          height: virtual.getTotalSize(),
          position: "relative",
          width: `${zoom * 100}%`,
        }}
      >
        {rows.map((row) => {
          const page = pages[row.index];
          return (
            <div
              key={row.key}
              data-index={row.index}
              ref={virtual.measureElement}
              className="continuous-page"
              style={{ transform: `translateY(${row.start}px)` }}
            >
              {page ? renderPage(page) : null}
              <span className="page-number">{row.index + 1}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
