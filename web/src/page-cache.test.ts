import { afterEach, expect, test, vi } from "vite-plus/test";
import { PageCache, pageKey } from "./page-cache.ts";
import type { Page } from "./types.ts";

const page = (ordinal: number): Page => ({
  entry: `${ordinal}.jpg`,
  entry_id: String(ordinal),
  ordinal,
  size: 4,
  mime: "image/jpeg",
  width: 600,
  height: 900,
});
const result = () => ({ blob: new Blob(["1234"]), fallback: false, compatible: false });
function setup(bytes = 8, count = 128) {
  let serial = 0;
  vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:${++serial}`);
  const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const fetchPage = vi.fn(async () => result());
  return { cache: new PageCache(bytes, count, fetchPage), fetchPage, revoke };
}
afterEach(() => vi.restoreAllMocks());

test("byte eviction uses LRU order and revokes the evicted URL", async () => {
  const { cache, revoke } = setup();
  const first = await cache.load("work", page(0), 0);
  const second = await cache.load("work", page(1), 0);
  expect(cache.get(pageKey("work", page(0), 0))).toBe(first);
  await cache.load("work", page(2), 0);
  expect(cache.get(pageKey("work", page(1), 0))).toBeUndefined();
  expect(revoke).toHaveBeenCalledExactlyOnceWith(second.url);
});

test("entry limit and display retention release URLs safely", async () => {
  const { cache, revoke } = setup(100, 1);
  const release = cache.retain(pageKey("work", page(0), 0));
  const first = await cache.load("work", page(0), 0);
  const second = await cache.load("work", page(1), 0);
  expect(revoke).toHaveBeenCalledWith(second.url);
  expect(cache.get(pageKey("work", page(0), 0))).toBe(first);
  release();
  await cache.load("work", page(2), 0);
  expect(revoke).toHaveBeenCalledWith(first.url);
});

test("same key shares a promise; width and forced PNG have separate keys", async () => {
  const { cache, fetchPage } = setup(100);
  const first = cache.load("work", page(0), 0);
  expect(cache.load("work", page(0), 0)).toBe(first);
  await first;
  await cache.load("work", page(0), 800);
  await cache.load("work", page(0), 0, undefined, true);
  expect(fetchPage).toHaveBeenCalledTimes(3);
});

test("aborting one consumer leaves another consumer's request alive", async () => {
  const { cache, fetchPage } = setup();
  const controller = new AbortController();
  const aborted = cache.load("work", page(0), 0, controller.signal);
  const shared = cache.load("work", page(0), 0);
  controller.abort();
  await expect(aborted).rejects.toMatchObject({ name: "AbortError" });
  await expect(shared).resolves.toMatchObject({ url: "blob:1" });
  expect(fetchPage).toHaveBeenCalledTimes(1);
});

test("clearWork revokes only that work and drops in-flight results", async () => {
  const { cache, revoke } = setup(100);
  const first = await cache.load("first", page(0), 0);
  const other = await cache.load("other", page(0), 0);
  const pending = cache.load("first", page(1), 0);
  cache.clearWork("first");
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  expect(revoke).toHaveBeenCalledExactlyOnceWith(first.url);
  expect(cache.get(pageKey("first", page(0), 0))).toBeUndefined();
  expect(cache.get(pageKey("other", page(0), 0))).toBe(other);
  await expect(cache.load("first", page(1), 0)).resolves.toMatchObject({ url: "blob:3" });
});

test("preloads follow list order and stop scheduling after navigation", async () => {
  const { cache, fetchPage } = setup(100);
  const controller = new AbortController();
  fetchPage.mockImplementationOnce(async () => {
    controller.abort();
    return result();
  });
  await cache.preload("work", [page(2), page(3), page(0)], 0, controller.signal);
  expect(fetchPage).toHaveBeenCalledTimes(1);
});
