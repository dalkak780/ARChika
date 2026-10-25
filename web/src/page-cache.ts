import type { Page } from "./types.ts";
import { pageBlob } from "./images.ts";

export type CachedPage = Awaited<ReturnType<typeof pageBlob>> & { url: string };
export function pageKey(workId: string, page: Page, width: number, forcePNG = false) {
  return JSON.stringify([workId, page.entry_id, page.ordinal, width, forcePNG]);
}

export class PageCache {
  private entries = new Map<string, { workId: string; value: CachedPage }>();
  private flights = new Map<
    string,
    { workId: string; controller: AbortController; promise: Promise<CachedPage> }
  >();
  private pins = new Map<string, number>();
  private bytes = 0;

  constructor(
    private maxBytes = 96 * 1024 * 1024,
    private maxEntries = 128,
    private fetchPage = pageBlob,
  ) {}

  get(key: string) {
    const entry = this.entries.get(key);
    if (!entry) return undefined;
    this.entries.delete(key);
    this.entries.set(key, entry);
    return entry.value;
  }

  retain(key: string) {
    this.pins.set(key, (this.pins.get(key) ?? 0) + 1);
    return () => {
      const count = (this.pins.get(key) ?? 1) - 1;
      if (count) this.pins.set(key, count);
      else this.pins.delete(key);
      this.trim();
    };
  }

  private remove(key: string) {
    const entry = this.entries.get(key);
    if (!entry) return;
    this.bytes -= entry.value.blob.size;
    URL.revokeObjectURL(entry.value.url);
    this.entries.delete(key);
  }

  private trim() {
    for (const key of this.entries.keys()) {
      if (this.bytes <= this.maxBytes && this.entries.size <= this.maxEntries) break;
      // Visible and decoding pages remain alive until their consumers release them.
      if (!this.pins.has(key)) this.remove(key);
    }
  }

  load(
    workId: string,
    page: Page,
    width: number,
    signal?: AbortSignal,
    forcePNG = false,
  ): Promise<CachedPage> {
    if (signal?.aborted) return Promise.reject(new DOMException("Aborted", "AbortError"));
    const key = pageKey(workId, page, width, forcePNG);
    const cached = this.get(key);
    let promise = cached ? Promise.resolve(cached) : this.flights.get(key)?.promise;
    if (!promise) {
      const controller = new AbortController();
      promise = this.fetchPage(workId, page, width, controller.signal, forcePNG)
        .then((result) => {
          controller.signal.throwIfAborted();
          const value = { ...result, url: URL.createObjectURL(result.blob) };
          this.entries.set(key, { workId, value });
          this.bytes += value.blob.size;
          this.trim();
          return value;
        })
        .finally(() => {
          if (this.flights.get(key)?.controller === controller) this.flights.delete(key);
        });
      this.flights.set(key, { workId, controller, promise });
    }
    if (!signal) return promise;
    return new Promise((resolve, reject) => {
      const abort = () => reject(new DOMException("Aborted", "AbortError"));
      signal.addEventListener("abort", abort, { once: true });
      promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
    });
  }

  async preload(workId: string, pages: Page[], width: number, signal: AbortSignal) {
    for (const page of pages) {
      if (signal.aborted) return;
      try {
        await this.load(workId, page, width, signal);
      } catch {
        // A speculative failure must not prevent reading or preloading later pages.
      }
    }
  }

  clearWork(workId: string) {
    for (const [key, flight] of this.flights) {
      if (flight.workId !== workId) continue;
      flight.controller.abort();
      this.flights.delete(key);
    }
    for (const [key, entry] of this.entries) {
      if (entry.workId === workId) this.remove(key);
    }
  }
}

export const pageCache = new PageCache();
