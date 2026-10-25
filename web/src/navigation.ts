export interface Dimensions {
  width?: number;
  height?: number;
}
export interface KeyTarget {
  type?: string;
  isContentEditable?: boolean;
  closest?: (selector: string) => KeyTarget | null;
}
export interface GesturePoint {
  x: number;
  y: number;
  time: number;
  touches: number;
  cancelled?: boolean;
}
export type NavigationAction = "next" | "previous" | "home" | "end";
export const WIDE_RATIO = 1;
export function wide(page: Dimensions | undefined) {
  return !!(page?.width && page?.height && page.width / page.height >= WIDE_RATIO);
}
export function known(page: Dimensions | undefined) {
  return !!(page?.width && page?.height);
}
export function visiblePages(
  pages: readonly Dimensions[],
  index: number,
  layout = "single",
  direction = "ltr",
) {
  if (!pages.length) return [];
  const first = Math.max(0, Math.min(index, pages.length - 1));
  // Unknown dimensions consume one page: a slow image can never skip its successor.
  return layout === "spread" &&
    known(pages[first]) &&
    !wide(pages[first]) &&
    known(pages[first + 1]) &&
    !wide(pages[first + 1])
    ? direction === "rtl"
      ? [first + 1, first]
      : [first, first + 1]
    : [first];
}
export function navigatePage(
  pages: readonly Dimensions[],
  index: number,
  action: NavigationAction,
  layout = "single",
  shown = visiblePages(pages, index, layout),
) {
  if (!pages.length) return 0;
  if (action === "home") return 0;
  if (action === "end") return pages.length - 1;
  if (action === "next") return Math.min(pages.length - 1, Math.max(index, ...shown) + 1);
  if (action === "previous") {
    if (layout !== "spread") return Math.max(0, index - 1);
    // Group backward from the actual current boundary, not from a changing global partition.
    const previous = index - 1;
    return previous > 0 &&
      known(pages[previous]) &&
      known(pages[previous - 1]) &&
      !wide(pages[previous]) &&
      !wide(pages[previous - 1])
      ? previous - 1
      : Math.max(0, previous);
  }
  return index;
}
export function ignoresKeys(target: KeyTarget | EventTarget | null | undefined) {
  if (!target || !("closest" in target || "isContentEditable" in target)) return false;
  const input = "closest" in target ? target.closest?.("input") : null;
  if (input) return !["button", "submit", "reset", "image", "checkbox"].includes(input.type || "");
  return !!target.isContentEditable || !!target.closest?.("textarea, select");
}
export function keyAction(
  event: {
    key: string;
    target?: KeyTarget | EventTarget | null;
    altKey?: boolean;
    ctrlKey?: boolean;
    metaKey?: boolean;
    shiftKey?: boolean;
  },
  mode = "paged",
  direction = "ltr",
):
  | NavigationAction
  | "goto"
  | "fullscreen"
  | "layout"
  | "direction"
  | "auto"
  | "overview"
  | "options"
  | "help"
  | "previousWork"
  | "nextWork"
  | "back"
  | null {
  if (event.altKey || event.ctrlKey || event.metaKey || ignoresKeys(event.target)) return null;
  const key = event.key.toLowerCase();
  const controls = {
    g: "goto",
    f: "fullscreen",
    p: "layout",
    m: "direction",
    n: "auto",
    q: "overview",
    o: "options",
    "?": "help",
    ",": "previousWork",
    ".": "nextWork",
    backspace: "back",
  } as const;
  if (key in controls) return controls[key as keyof typeof controls];
  if (mode === "scroll") return null;
  const forward = direction === "rtl" ? "previous" : "next";
  const backward = direction === "rtl" ? "next" : "previous";
  if (key === "arrowright" || key === "arrowleft") {
    const action = key === "arrowright" ? forward : backward;
    return event.shiftKey ? (action === "next" ? "end" : "home") : action;
  }
  if (key === " ") return event.shiftKey ? "previous" : "next";
  if (event.shiftKey) return null;
  if (key === "d") return forward;
  if (key === "a") return backward;
  if (key === "pagedown") return "next";
  if (key === "pageup") return "previous";
  if (key === "home") return "home";
  if (key === "end") return "end";
  return null;
}

export function gestureAction(
  start: GesturePoint | null,
  end: GesturePoint,
  {
    mode = "paged",
    zoom = 1,
    viewportWidth = 1000,
    left = 0,
    width,
    direction = "ltr",
  }: {
    mode?: string;
    zoom?: number;
    viewportWidth?: number;
    left?: number;
    width?: number;
    direction?: string;
  } = {},
): NavigationAction | "chrome" | null {
  const next = direction === "rtl" ? "previous" : "next";
  const previous = direction === "rtl" ? "next" : "previous";
  const zoneWidth = width ?? viewportWidth;
  if (!start || start.cancelled || start.touches > 1 || end.touches > 1) return null;
  const dx = end.x - start.x,
    dy = end.y - start.y;
  if (Math.abs(dx) < 12 && Math.abs(dy) < 12 && end.time - start.time < 500) {
    // Taps use left/centre/right zones for mouse, touch and pen alike.
    if (mode === "scroll" || zoom !== 1 || zoneWidth <= 0) return "chrome";
    const position = (end.x - left) / zoneWidth;
    return position < 0.23 ? previous : position > 0.77 ? next : "chrome";
  }
  // Swipes ignore screen edges so OS back/forward gestures keep working.
  if (
    mode === "scroll" ||
    zoom !== 1 ||
    start.x < 28 ||
    start.x > viewportWidth - 28 ||
    end.time - start.time > 1000 ||
    Math.abs(dx) < 55 ||
    Math.abs(dx) < Math.abs(dy) * 1.7
  )
    return null;
  return dx < 0 ? next : previous;
}
