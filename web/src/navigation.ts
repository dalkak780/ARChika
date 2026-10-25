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
export function visiblePages(pages: readonly Dimensions[], index: number, layout = "single") {
  if (!pages.length) return [];
  const first = Math.max(0, Math.min(index, pages.length - 1));
  // Unknown dimensions consume one page: a slow image can never skip its successor.
  return layout === "spread" &&
    known(pages[first]) &&
    !wide(pages[first]) &&
    known(pages[first + 1]) &&
    !wide(pages[first + 1])
    ? [first, first + 1]
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
  if (action === "next") return Math.min(pages.length - 1, (shown.at(-1) ?? index) + 1);
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
) {
  if (
    event.altKey ||
    event.ctrlKey ||
    event.metaKey ||
    event.shiftKey ||
    ignoresKeys(event.target) ||
    mode === "scroll"
  )
    return null;
  const actions: Record<string, NavigationAction> = {
    ArrowRight: "next",
    PageDown: "next",
    ArrowLeft: "previous",
    PageUp: "previous",
    Home: "home",
    End: "end",
  };
  return actions[event.key] ?? null;
}
export function gestureAction(
  start: GesturePoint | null,
  end: GesturePoint,
  { mode = "paged", zoom = 1, viewportWidth = 1000 } = {},
) {
  if (
    !start ||
    start.cancelled ||
    start.touches > 1 ||
    end.touches > 1 ||
    zoom !== 1 ||
    start.x < 28 ||
    start.x > viewportWidth - 28
  )
    return null;
  const dx = end.x - start.x,
    dy = end.y - start.y;
  if (Math.abs(dx) < 12 && Math.abs(dy) < 12 && end.time - start.time < 500) return "chrome";
  if (
    mode === "scroll" ||
    end.time - start.time > 1000 ||
    Math.abs(dx) < 55 ||
    Math.abs(dx) < Math.abs(dy) * 1.7
  )
    return null;
  return dx < 0 ? "next" : "previous";
}
