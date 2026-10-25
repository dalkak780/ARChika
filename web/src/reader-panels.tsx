import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { Page } from "./types.ts";
import { encoded } from "./api.tsx";

export function ReaderPanel({
  title,
  close,
  children,
}: {
  title: string;
  close: () => void;
  children: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const opener = document.activeElement;
    const element = dialog.current;
    element?.showModal();
    return () => {
      element?.close();
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    };
  }, []);
  return (
    <dialog
      ref={dialog}
      className="reader-dialog panel"
      aria-label={title}
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        const box = event.currentTarget.getBoundingClientRect();
        if (
          event.clientX < box.left ||
          event.clientX > box.right ||
          event.clientY < box.top ||
          event.clientY > box.bottom
        )
          close();
      }}
    >
      <div className="toolbar">
        <h2>{title}</h2>
        <button onClick={close}>Close</button>
      </div>
      {children}
    </dialog>
  );
}

export function PageThumbnail({ workId, page }: { workId: string; page: Page }) {
  const frame = useRef<HTMLSpanElement>(null);
  const [source, setSource] = useState<string>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    let objectURL: string | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let visible = false;
    let attempts = 0;
    let transient = true;
    let busy = false;
    let retryAt = 0;
    async function load() {
      if (!visible || busy || objectURL || attempts >= 20 || controller.signal.aborted) return;
      if (Date.now() < retryAt) {
        clearTimeout(timer);
        timer = setTimeout(() => {
          void load();
        }, retryAt - Date.now());
        return;
      }
      attempts++;
      busy = true;
      let delay = 2000;
      try {
        const response = await fetch(
          `/api/works/${encoded(workId)}/pages/${page.ordinal}/thumbnail`,
          { credentials: "same-origin", signal: controller.signal },
        );
        const after = response.headers.get("Retry-After");
        if (after) {
          const seconds = Number(after);
          delay = Number.isFinite(seconds)
            ? Math.max(0, seconds * 1000)
            : Math.max(0, Date.parse(after) - Date.now());
          if (!Number.isFinite(delay)) delay = 2000;
        }
        if (!response.ok) {
          // Only a busy server is worth retrying; missing or unsupported pages stay failed.
          transient = response.status === 503;
          throw new Error("Thumbnail unavailable");
        }
        const blob = await response.blob();
        if (controller.signal.aborted) return;
        objectURL = URL.createObjectURL(blob);
        const decoder = new Image();
        decoder.src = objectURL;
        await decoder.decode();
        if (!controller.signal.aborted) setSource(objectURL);
      } catch {
        if (objectURL) {
          URL.revokeObjectURL(objectURL);
          objectURL = undefined;
        }
        retryAt = Date.now() + delay;
        if (controller.signal.aborted) return;
        if (transient && attempts < 20)
          timer = setTimeout(() => {
            void load();
          }, delay);
        else setFailed(true);
      } finally {
        busy = false;
      }
    }
    const observer = new IntersectionObserver(([entry]) => {
      visible = entry?.isIntersecting ?? false;
      if (visible) void load();
    });
    if (frame.current) observer.observe(frame.current);
    return () => {
      controller.abort();
      observer.disconnect();
      clearTimeout(timer);
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [workId, page.ordinal]);
  return (
    <span
      ref={frame}
      className="page-thumbnail"
      style={{
        aspectRatio: page.width && page.height ? `${page.width}/${page.height}` : "2/3",
        width: page.width && page.height ? `${(165 * page.width) / page.height}px` : "110px",
      }}
    >
      {source && !failed ? (
        <img src={source} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />
      ) : (
        <span className="muted">{failed ? "Preview unavailable" : "Loading…"}</span>
      )}
    </span>
  );
}

export function PageIndicator({
  index,
  count,
  editing,
  setEditing,
  jump,
}: {
  index: number;
  count: number;
  editing: boolean;
  setEditing: (value: boolean) => void;
  jump: (index: number) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => {
    if (editing) {
      input.current?.focus();
      input.current?.select();
    }
  }, [editing]);
  return editing ? (
    <form
      className="page-jump"
      onSubmit={(event) => {
        event.preventDefault();
        const value = Number(input.current?.value);
        if (Number.isInteger(value) && value >= 1 && value <= count) {
          jump(value - 1);
          setEditing(false);
        }
      }}
    >
      <input
        ref={input}
        aria-label="Go to page"
        type="number"
        min={1}
        max={count}
        defaultValue={index + 1}
        required
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            setEditing(false);
          }
        }}
        onBlur={() => setEditing(false)}
      />
    </form>
  ) : (
    <button
      className="page-indicator"
      title="Go to page (G)"
      aria-label={`Page ${index + 1} of ${count}. Go to page`}
      onClick={() => setEditing(true)}
    >
      <span>{index + 1}</span>
      <span> / </span>
      <span>{count}</span>
    </button>
  );
}
