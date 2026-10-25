// Response shapes from internal/app handlers and internal/archive.Entry.
export interface Preferences {
  language?: string;
  reader_mode?: "paged" | "scroll";
  layout?: "single" | "spread";
  fit?: "viewport" | "width" | "height" | "original";
  reading_direction?: "ltr" | "rtl";
  preload_count?: number;
  auto_next_seconds?: number;
  resize_width?: number;
}
export interface User {
  id: string;
  name: string;
  admin: boolean;
  must_change_password: boolean;
  csrf?: string;
  preferences?: Preferences;
}
export interface Work {
  alias_id: number;
  id: string;
  root_id: string;
  root_name: string;
  relative_path: string;
  original_name: string;
  title: string;
  kind: string;
  size: number;
  mtime: number;
  version: number;
  registered_at: number;
  index_status: string;
  error_code?: string;
  cover: string;
  cover_unresolved: boolean;
  favorite: boolean;
}
export interface Page {
  entry: string;
  entry_id: string;
  ordinal: number;
  size: number;
  mime: string;
  width: number;
  height: number;
}
export interface WorksPage {
  items: Work[];
  total: number;
  next_cursor: string;
  generation: string;
  page: number;
  limit: number;
}
export interface WorkDetail {
  work: Work;
  pages: Page[];
  capabilities: {
    random_access_cost: string;
    solid: string;
    session_reuse: boolean;
    engine: string;
  } | null;
  metadata: Metadata | null;
  cover_unresolved: boolean;
}
export interface Root {
  id: string;
  key: string;
  name: string;
  path?: string;
  watch: boolean;
  auto_crawl: boolean;
  providers: string[] | null;
  quiet_seconds: number;
  parser_enabled: boolean;
  parser_regex: string;
  status: string;
  pending_path?: string;
  warning?: string;
  trust_identity: boolean;
}
export interface Job {
  id: string;
  type: string;
  root_id: string;
  work_id: string;
  provider: string;
  actor: string;
  state: string;
  origin: string;
  payload: string;
  checkpoint: string;
  done: number;
  failed: number;
  total: number;
  error: string;
  created: number;
  updated: number;
  attempts: number;
}
export interface SavedSearch {
  id: string;
  name: string;
  query: string;
  sort: string;
}
export interface Metadata {
  fields: Record<string, { value: string; origin: string; manual: boolean }>;
  tags: string[];
  providers: {
    id: string;
    provider: string;
    source_id: string;
    source_url: string;
    fields: Record<string, string> | null;
    tags: string[] | null;
    fetched_at: number;
    version: string;
    evidence: string[] | null;
  }[];
  candidates: {
    id: string;
    provider: string;
    source_id: string;
    source_url: string;
    title: string;
    confidence: number;
    evidence: string[] | null;
  }[];
}
export interface AdminUser extends Pick<User, "id" | "name" | "admin"> {
  disabled: boolean;
  roots: string[];
}
export interface ConfigPreview {
  changes: { key: string; name: string; path: string; existing_id: string; action: string }[];
}
export interface Provider {
  id: string;
  name: string;
  version: string;
  kind: string;
  enabled: boolean;
  status: string;
  config: unknown;
  credential_configured: boolean;
  priority: number;
}
// The provider list currently omits the manifest; the UI also accepts it when present.
export interface Plugin extends Provider {
  manifest?: unknown;
}
export interface Providers {
  providers: Provider[];
  plugins: Plugin[];
  network: { dns_mode: string; doh_url: string; proxy_url: string };
}
export interface Move {
  id: string;
  from_work: string;
  to_work: string;
  from_root: string;
  to_root: string;
  from_path: string;
  to_path: string;
  evidence: string;
}
export interface OfflineWork extends Pick<
  Work,
  "id" | "title" | "root_id" | "relative_path" | "index_status" | "size" | "mtime"
> {
  error_code: string;
  root_status: string;
  alias_status: string;
  checked_at: number;
}
export interface OfflinePage {
  items: OfflineWork[];
  total: number;
}
export interface CleanupPreview {
  count: number;
  eligible: string[];
  blocked: string[];
}
export type OfflineActionResult =
  | CleanupPreview
  | { count: number; blocked?: string[] }
  | { ok: boolean };
export interface AuditRecord {
  id: number;
  actor: string;
  action: string;
  root_id: string;
  detail: string;
  created: number;
}
export interface ErrorBody {
  error: { code: string; message: string };
}
export interface Folder {
  name: string;
  path: string;
  works: number;
  cover_work_id: string;
}
export interface FolderListing {
  root_id: string;
  root_name: string;
  path: string;
  works: number;
  folders: Folder[];
}
export interface LibrarySearch {
  browse: string;
  folder: string;
  q: string;
  sort: string;
  direction: string;
  page: number;
  limit: number;
  root: string;
  view: string;
}
