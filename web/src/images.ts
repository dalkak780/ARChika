import type { Page } from "./types.ts";
// Decode generated 2x2 solid-colour samples; never infer codec support from User-Agent.
const samples = {
  "image/webp": "UklGRh4AAABXRUJQVlA4TBEAAAAvAUAAAAfQq160sv+BiOh/AAA=",
  "image/avif":
    "AAAAHGZ0eXBhdmlmAAAAAG1pZjFhdmlmbWlhZgAAARdtZXRhAAAAAAAAACFoZGxyAAAAAAAAAABwaWN0AAAAAAAAAAAAAAAAAAAAADRpbG9jAAAAAERAAAIAAQAAAAABOwABAAAAAAAAACAAAgAAAAABWwABAAAAAAAAAL4AAAA4aWluZgAAAAAAAgAAABVpbmZlAgAAAAABAABhdjAxAAAAABVpbmZlAgAAAQACAABFeGlmAAAAAA5waXRtAAAAAAABAAAAVmlwcnAAAAA4aXBjbwAAAAxhdjFDgQAMAAAAABRpc3BlAAAAAAAAAAIAAAACAAAAEHBpeGkAAAAAAwgICAAAABZpcG1hAAAAAAAAAAEAAQOBAgMAAAAaaXJlZgAAAAAAAAAOY2RzYwACAAEAAQAAAOZtZGF0EgAKCBgANggIaDQgMhITQAIIIIQAAGjTCqXykjvskGAAAAAGRXhpZgAASUkqAAgAAAAGABIBAwABAAAAAQAAABoBBQABAAAAVgAAABsBBQABAAAAXgAAACgBAwABAAAAAgAAABMCAwABAAAAAQAAAGmHBAABAAAAZgAAAAAAAABIAAAAAQAAAEgAAAABAAAABgAAkAcABAAAADAyMTABkQcABAAAAAECAwAAoAcABAAAADAxMDABoAMAAQAAAP//AAACoAQAAQAAAAIAAAADoAQAAQAAAAIAAAAAAAAA",
  "image/jxl":
    "AAAADEpYTCANCocKAAAAFGZ0eXBqeGwgAAAAAGp4bCAAAAASanhscAAAAAD/CggQEAkAAAB3YnJvYkV4aWYbtwAAhAe+q/2ZmzcrkF40m1ALEoYXUVpgA2Srk1P4ATjLAkrb7QoZxLmNIX4RpWjcPrBtFx3VEpLwFWA4hiLdcep64LxnEYcIDPh8cz1Z3maRz8qvyRjXdYQ/KcTEtLGOEeZV2nfi3BEJDQAAACdqeGxwgAAAAQgEAQBUAEsSxYKFJNIj8yYAAES4H4Bj+F3OBA==",
};
let capabilities: Promise<Record<string, boolean>> | undefined;
export function imageCapabilities() {
  return (capabilities ||= Promise.all(
    Object.entries(samples).map(
      ([mime, value]) =>
        new Promise<[string, boolean]>((resolve) => {
          const image = new Image();
          const timer = setTimeout(() => resolve([mime, false]), 2000);
          image.onload = () => {
            clearTimeout(timer);
            resolve([mime, image.naturalWidth > 0]);
          };
          image.onerror = () => {
            clearTimeout(timer);
            resolve([mime, false]);
          };
          image.src = `data:${mime};base64,${value}`;
        }),
    ),
  ).then(Object.fromEntries));
}
export async function pageBlob(
  workId: string,
  page: Page,
  width: number,
  signal: AbortSignal,
  forcePNG = false,
) {
  const support = await imageCapabilities();
  const format = forcePNG || support[page.mime] === false ? "png" : undefined;
  const params = new URLSearchParams();
  if (format) params.set("format", format);
  if (width) params.set("width", String(width));
  const response = await fetch(
    `/api/works/${encodeURIComponent(workId)}/pages/${page.ordinal}${params.toString() ? `?${params}` : ""}`,
    { signal, credentials: "same-origin" },
  );
  if (!response.ok) {
    const value: unknown = await response.json().catch(() => null);
    const detail = value && typeof value === "object" && "error" in value ? value.error : null;
    const message =
      detail &&
      typeof detail === "object" &&
      "message" in detail &&
      typeof detail.message === "string"
        ? detail.message
        : undefined;
    const code = detail && typeof detail === "object" && "code" in detail ? detail.code : undefined;
    const error = Object.assign(new Error(message || `Page unavailable (${response.status})`), {
      code,
    });
    throw error;
  }
  return {
    blob: await response.blob(),
    fallback: response.headers.get("X-ARChika-Still-Fallback") === "true",
    compatible: format === "png",
  };
}
