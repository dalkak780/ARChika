// Drops the oldest insertion once a bounded in-memory map grows past max.
export function trimOldest<K, V>(map: Map<K, V>, max: number) {
  if (map.size <= max) return;
  const oldest = map.keys().next();
  if (!oldest.done) map.delete(oldest.value);
}
