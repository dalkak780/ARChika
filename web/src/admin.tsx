import type { FormEvent, ReactNode } from "react";
import type {
  Root,
  ConfigPreview,
  Providers,
  Provider,
  Plugin,
  AdminUser,
  Job,
  OfflinePage,
  Move,
  Metadata,
  AuditRecord,
  OfflineActionResult,
} from "./types.ts";
import React, { useState } from "react";
import { Link, getRouteApi, useNavigate } from "@tanstack/react-router";
import { useAuth, useAPI, useAction, encoded, ErrorNotice, Pending } from "./api.tsx";
const route = getRouteApi("/admin");
const formText = (value: FormDataEntryValue | null) => {
  if (value !== null && typeof value !== "string") throw new Error("Expected a text form field");
  return value || "";
};
const list = (value: FormDataEntryValue | null) =>
  formText(value)
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
const json = (value: FormDataEntryValue | null, fallback: unknown = {}): unknown =>
  formText(value).trim() ? JSON.parse(formText(value)) : fallback;
const text = (data: FormData, key: string) => formText(data.get(key));
function JSONField({
  name,
  label,
  value = {},
  required = false,
  rows = 5,
}: {
  name: string;
  label: string;
  value?: unknown;
  required?: boolean;
  rows?: number;
}) {
  return (
    <label>
      {label}
      <textarea
        name={name}
        rows={rows}
        defaultValue={typeof value === "string" ? value : JSON.stringify(value, null, 2)}
        spellCheck="false"
        required={required}
      />
    </label>
  );
}
function Check({
  name,
  children,
  checked = false,
  value,
}: {
  name: string;
  children: ReactNode;
  checked?: boolean;
  value?: string;
}) {
  return (
    <label className="check">
      <input type="checkbox" name={name} defaultChecked={checked} value={value} />
      {children}
    </label>
  );
}
function Result({ value }: { value: unknown }) {
  return value ? (
    <pre className="result" role="status">
      {JSON.stringify(value, null, 2)}
    </pre>
  ) : null;
}
function ConfirmButton({
  children,
  message,
  onConfirm,
  disabled,
}: {
  children: ReactNode;
  message: string;
  onConfirm: () => void;
  disabled: boolean;
}) {
  return (
    <button
      className="danger"
      disabled={disabled}
      onClick={() => {
        if (window.confirm(message)) onConfirm();
      }}
    >
      {children}
    </button>
  );
}
export default function Admin() {
  const { user } = useAuth(),
    { tab } = route.useSearch();
  if (!user.admin)
    return (
      <main id="main">
        <h1>Administrator access required</h1>
        <Link to="/">Library</Link>
      </main>
    );
  return (
    <main id="main" className="admin">
      <div className="page-heading">
        <div>
          <div className="eyebrow">SYSTEM & CATALOG</div>
          <h1>Administration</h1>
        </div>
        <span className="badge">Administrator</span>
      </div>
      <nav className="tabs" aria-label="Administration sections">
        {["roots", "users", "jobs", "offline", "providers", "plugins", "audit"].map((name) => (
          <Link
            key={name}
            to="/admin"
            search={{ tab: name }}
            activeOptions={{ exact: true, includeSearch: true }}
            className={tab === name ? "selected" : ""}
          >
            {name}
          </Link>
        ))}
      </nav>
      {tab === "roots" ? (
        <Roots />
      ) : tab === "users" ? (
        <Users />
      ) : tab === "jobs" ? (
        <Jobs />
      ) : tab === "offline" ? (
        <>
          <Moves />
          <Offline />
        </>
      ) : tab === "providers" ? (
        <Providers />
      ) : tab === "plugins" ? (
        <Plugins />
      ) : (
        <Audit />
      )}
    </main>
  );
}
function Roots() {
  const roots = useAPI<Root[]>(["roots"], "/roots"),
    preview = useAPI<ConfigPreview>(["config-preview"], "/admin/config/preview"),
    providers = useAPI<Providers>(["providers"], "/admin/providers");
  const action = useAction();
  return (
    <>
      <p className="notice">
        Original files are read-only. Adding a root does not start a scan. Paths belong to the
        server/container filesystem; explicitly enqueue discovery in Jobs.
      </p>
      <ErrorNotice error={roots.error || preview.error || action.error || providers.error} />
      <section className="panel">
        <h2>Configuration bootstrap</h2>
        <p>
          Review configured roots, then apply only the selected changes. Existing catalog entries
          are not silently replaced.
        </p>
        {preview.data && (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              action.mutate({
                path: "/admin/config/apply",
                body: {
                  keys: new FormData(event.currentTarget).getAll("keys"),
                },
              });
            }}
          >
            {preview.data.changes.map((change) => (
              <Check key={change.key} name="keys" value={change.key}>
                {change.name} — {change.path} · {change.action}
                {change.existing_id ? ` (${change.existing_id})` : ""}
              </Check>
            ))}
            {!preview.data.changes.length && (
              <p className="muted">No pending configuration changes.</p>
            )}
            <button disabled={action.isPending || !preview.data.changes.length}>
              Apply selected changes
            </button>
          </form>
        )}
      </section>
      {roots.isPending && <Pending />}
      {roots.data?.map((root) => (
        <RootEditor key={root.id} root={root} providers={providers.data?.providers || []} />
      ))}
      <details className="panel">
        <summary>Add read-only root</summary>
        <RootForm providers={providers.data?.providers || []} />
      </details>
    </>
  );
}
function RootForm({ root = {}, providers = [] }: { root?: Partial<Root>; providers?: Provider[] }) {
  const action = useAction(),
    [error, setError] = useState<unknown>(null),
    [result, setResult] = useState<unknown>(null);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setError(null);
    const body: Record<string, unknown> = {
      key: text(data, "key"),
      name: text(data, "name"),
      path: text(data, "path"),
      watch: data.has("watch"),
      auto_crawl: data.has("auto_crawl"),
      providers: data.getAll("providers"),
      quiet_seconds: Number(data.get("quiet_seconds")),
      parser_enabled: data.has("parser_enabled"),
      parser_regex: text(data, "parser_regex"),
      trust_identity: data.has("trust_identity"),
    };
    action.mutate(
      {
        path: `/admin/roots${root.id ? `/${encoded(root.id)}` : ""}`,
        method: root.id ? "PUT" : "POST",
        body,
      },
      { onSuccess: setResult },
    );
  }
  return (
    <form onSubmit={submit}>
      <div className="form-grid">
        <label>
          Stable configuration key
          <input name="key" defaultValue={root.key} required pattern="[A-Za-z0-9_.-]+" />
        </label>
        <label>
          Display name
          <input name="name" defaultValue={root.name} required />
        </label>
        <label className="full">
          Server path
          <input name="path" defaultValue={root.path} required />
        </label>
        <label>
          Watcher quiet period (seconds)
          <input
            name="quiet_seconds"
            type="number"
            min="1"
            max="86400"
            defaultValue={root.quiet_seconds || 10}
            required
          />
        </label>
        <label>
          Filename parsing regex
          <input
            name="parser_regex"
            defaultValue={root.parser_regex || ""}
            placeholder="Named captures produce tags, not titles"
          />
        </label>
      </div>
      <div className="toolbar">
        <Check name="watch" checked={root.watch}>
          Watch changes
        </Check>
        <Check name="auto_crawl" checked={root.auto_crawl}>
          Automatically crawl changed works
        </Check>
        <Check name="parser_enabled" checked={root.parser_enabled}>
          Parse filename tags
        </Check>
        <Check name="trust_identity" checked={root.trust_identity}>
          Trust filesystem identity
        </Check>
      </div>
      <fieldset>
        <legend>Allowed crawler providers</legend>
        {providers.map((provider) => (
          <Check
            key={provider.id}
            name="providers"
            value={provider.id}
            checked={root.providers?.includes(provider.id)}
          >
            {provider.name}
          </Check>
        ))}
      </fieldset>
      <p className="muted small">
        A path change performs bounded relocation verification. Review findings before forcing
        adoption or explicitly reconciling. The server does not move originals.
      </p>
      <button className="primary" disabled={action.isPending}>
        {root.id ? "Save root / verify relocation" : "Add root"}
      </button>
      <ErrorNotice error={error || action.error} />
      <Result value={result} />
    </form>
  );
}
function RootEditor({ root, providers }: { root: Root; providers: Provider[] }) {
  const action = useAction(),
    [preview, setPreview] = useState<unknown>(null);
  return (
    <details className="panel">
      <summary>
        {root.name} <span className="badge">{root.status}</span>{" "}
        <span className="muted">{root.path}</span>
      </summary>
      {root.warning && <p className="notice error">{root.warning}</p>}
      {root.pending_path && (
        <div className="notice">
          <strong>Pending relocation: {root.pending_path}</strong>
          <p>
            Force may adopt the wrong collection. Reconcile explicitly traverses this root; neither
            action moves originals.
          </p>
          <ConfirmButton
            message="Force this pending root relocation? Verify the target is the same collection."
            disabled={action.isPending}
            onConfirm={() =>
              action.mutate({
                path: `/admin/roots/${encoded(root.id)}/relocation`,
                body: { action: "force", confirm: true },
              })
            }
          >
            Force relocation
          </ConfirmButton>{" "}
          <ConfirmButton
            message="Start explicit reconciliation of the pending root?"
            disabled={action.isPending}
            onConfirm={() =>
              action.mutate({
                path: `/admin/roots/${encoded(root.id)}/relocation`,
                body: { action: "reconcile", confirm: true },
              })
            }
          >
            Reconcile target
          </ConfirmButton>
        </div>
      )}
      <RootForm root={root} providers={providers} />
      <hr />
      <button
        onClick={() =>
          action.mutate(
            {
              path: `/admin/roots/${encoded(root.id)}?preview=true`,
              method: "DELETE",
            },
            { onSuccess: setPreview },
          )
        }
      >
        Preview removing root mappings
      </button>
      {Boolean(preview) && (
        <>
          <Result value={preview} />
          <ConfirmButton
            message={`Remove ${root.name} mappings and grants? Original files remain unchanged.`}
            disabled={action.isPending}
            onConfirm={() =>
              action.mutate({
                path: `/admin/roots/${encoded(root.id)}`,
                method: "DELETE",
                body: { confirm: true },
              })
            }
          >
            Remove root mappings
          </ConfirmButton>
        </>
      )}
      <ErrorNotice error={action.error} />
    </details>
  );
}
function Users() {
  const users = useAPI<AdminUser[]>(["users"], "/admin/users"),
    roots = useAPI<Root[]>(["roots"], "/roots");
  return (
    <>
      <p className="muted">
        Root grants are independent. Administrator accounts have administrative access. The server
        prevents disabling or demoting the final administrator.
      </p>
      <ErrorNotice error={users.error || roots.error} />
      {users.isPending && <Pending />}
      {users.data?.map((user) => (
        <details className="panel" key={user.id}>
          <summary>
            {user.name}{" "}
            <span className="badge">
              {user.admin ? "admin" : "reader"}
              {user.disabled ? " · disabled" : ""}
            </span>
          </summary>
          <UserForm user={user} roots={roots.data || []} />
        </details>
      ))}
      <section className="panel">
        <h2>Create user</h2>
        <UserForm roots={roots.data || []} />
      </section>
    </>
  );
}
function UserForm({ user = {}, roots }: { user?: Partial<AdminUser>; roots: Root[] }) {
  const action = useAction();
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget,
      data = new FormData(form);
    const body: Record<string, unknown> = {
      admin: data.has("admin"),
      roots: data.getAll("roots"),
      disabled: data.has("disabled"),
    };
    if (!user.id) body.name = text(data, "name");
    if (data.get("password")) body.password = text(data, "password");
    action.mutate(
      {
        path: `/admin/users${user.id ? `/${encoded(user.id)}` : ""}`,
        method: user.id ? "PUT" : "POST",
        body,
      },
      {
        onSuccess: () => {
          const input = form.elements.namedItem("password");
          if (input instanceof HTMLInputElement) input.value = "";
          if (!user.id) form.reset();
        },
      },
    );
  }
  return (
    <form onSubmit={submit}>
      <div className="form-grid">
        {!user.id && (
          <label>
            Username
            <input name="name" autoComplete="off" required />
          </label>
        )}
        <label>
          {user.id ? "New password (blank keeps current)" : "Password"}
          <input
            name="password"
            type="password"
            autoComplete="new-password"
            required={!user.id}
            minLength={12}
          />
        </label>
      </div>
      <div className="toolbar">
        <Check name="admin" checked={user.admin}>
          Administrator
        </Check>
        {user.id && (
          <Check name="disabled" checked={user.disabled}>
            Disabled
          </Check>
        )}
      </div>
      <fieldset>
        <legend>Root access</legend>
        {roots.map((root) => (
          <Check key={root.id} name="roots" value={root.id} checked={user.roots?.includes(root.id)}>
            {root.name}
          </Check>
        ))}
      </fieldset>
      <button disabled={action.isPending}>{user.id ? "Update account" : "Create account"}</button>
      <ErrorNotice error={action.error} />
      {action.isSuccess && <p role="status">Account saved.</p>}
    </form>
  );
}

function Jobs() {
  const jobs = useAPI<Job[]>(["jobs"], "/admin/jobs", { refetchInterval: 3000 }),
    roots = useAPI<Root[]>(["roots"], "/roots");
  const action = useAction(),
    [preview, setPreview] = useState<unknown>(null),
    [error, setError] = useState<unknown>(null);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    const data = new FormData(event.currentTarget),
      isPreview =
        (event.nativeEvent instanceof SubmitEvent &&
        event.nativeEvent.submitter instanceof HTMLButtonElement
          ? event.nativeEvent.submitter.value
          : undefined) === "preview";
    const body: Record<string, unknown> = {
      type: text(data, "type"),
      ...(data.get("root_id") ? { root_id: text(data, "root_id") } : {}),
      ...(data.get("work_ids") ? { work_ids: list(data.get("work_ids")) } : {}),
      ...(data.get("query") ? { query: text(data, "query") } : {}),
      ...(data.get("provider") ? { provider: text(data, "provider") } : {}),
      ...(isPreview ? { preview: true } : {}),
    };
    if (
      !isPreview &&
      !window.confirm(
        "Enqueue this explicit background job? It may traverse the selected root or many works.",
      )
    )
      return;
    action.mutate({ path: "/admin/jobs", body }, { onSuccess: setPreview });
  }
  return (
    <>
      <section className="panel">
        <h2>Explicit background work</h2>
        <p>
          No startup scan or library-wide preconversion is performed. Preview scope before
          submitting expensive work.
        </p>
        <form onSubmit={submit}>
          <div className="form-grid">
            <label>
              Job type
              <select name="type">
                <option value="scan">Discover / scan</option>
                <option value="reconcile">Reconcile filesystem</option>
                <option value="index_bulk">Index page lists</option>
                <option value="thumbnail">Generate thumbnails</option>
                <option value="crawl_bulk">Crawl metadata</option>
              </select>
            </label>
            <label>
              Root
              <select name="root_id">
                <option value="">All permitted roots</option>
                {roots.data?.map((root) => (
                  <option key={root.id} value={root.id}>
                    {root.name} · {root.status}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Work IDs (comma or newline)
              <textarea name="work_ids" rows={2} />
            </label>
            <label>
              Search scope
              <input name="query" placeholder="Optional advanced query" />
            </label>
            <label>
              Provider ID
              <input name="provider" placeholder="Required for crawl jobs" />
            </label>
          </div>
          <div className="toolbar">
            <button name="action" value="preview" disabled={action.isPending}>
              Preview count
            </button>
            <button className="primary" name="action" value="enqueue" disabled={action.isPending}>
              Enqueue job
            </button>
          </div>
        </form>
        <Result value={preview} />
      </section>
      <div className="toolbar">
        <button
          onClick={() => action.mutate({ path: "/admin/jobs/pause", body: { paused: true } })}
        >
          Pause all jobs
        </button>
        <button
          onClick={() =>
            action.mutate({
              path: "/admin/jobs/pause",
              body: { paused: false },
            })
          }
        >
          Resume all jobs
        </button>
      </div>
      <ErrorNotice error={jobs.error || roots.error || action.error || error} />
      {jobs.isPending && <Pending />}
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Job</th>
              <th>State</th>
              <th>Progress</th>
              <th>Scope / error</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {jobs.data?.map((job) => (
              <tr key={job.id}>
                <td>
                  <strong>{job.type}</strong>
                  <small>
                    {job.id}
                    <br />
                    {job.created}
                  </small>
                </td>
                <td>{job.state}</td>
                <td>
                  {job.done} / {job.total}
                  <br />
                  <small>{job.failed} failed</small>
                </td>
                <td>
                  {job.root_id || job.work_id || "Bulk"}
                  <small className="error-text">{job.error}</small>
                </td>
                <td>
                  <div className="toolbar">
                    {["pause", "resume", "cancel", "retry"].map((command) => (
                      <button
                        key={command}
                        disabled={action.isPending}
                        onClick={() =>
                          action.mutate({
                            path: `/admin/jobs/${encoded(job.id)}`,
                            body: { action: command },
                          })
                        }
                      >
                        {command}
                      </button>
                    ))}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
function Offline() {
  const offline = useAPI<OfflinePage>(["offline"], "/admin/offline"),
    action = useAction<OfflineActionResult>();
  const [selected, setSelected] = useState<string[]>([]),
    [preview, setPreview] = useState<OfflineActionResult | null>(null),
    [result, setResult] = useState<unknown>(null);
  function toggle(id: string) {
    setSelected((old) => (old.includes(id) ? old.filter((value) => value !== id) : [...old, id]));
    setPreview(null);
  }
  return (
    <>
      <p className="notice">
        An unavailable root is not proof that its works were deleted. Cleanup removes catalog/cache
        records only when the source is still missing on an online, accessible root. Originals are
        never deleted.
      </p>
      <ErrorNotice error={offline.error || action.error} />
      <div className="toolbar">
        <button
          onClick={() => {
            setSelected(offline.data?.items.map((item) => item.id) || []);
            setPreview(null);
          }}
        >
          Select listed
        </button>
        <button
          onClick={() => {
            setSelected([]);
            setPreview(null);
          }}
        >
          Clear selection
        </button>
        <button
          disabled={!selected.length || action.isPending}
          onClick={() =>
            action.mutate(
              { path: "/admin/restore", body: { ids: selected } },
              { onSuccess: setResult },
            )
          }
        >
          Verify / restore selected
        </button>
        <button
          disabled={!selected.length || action.isPending}
          onClick={() =>
            action.mutate(
              {
                path: "/admin/cleanup",
                body: { ids: selected, preview: true },
              },
              { onSuccess: setPreview },
            )
          }
        >
          Preview cleanup
        </button>
      </div>
      {preview && "eligible" in preview && (
        <section className="panel">
          <h2>Cleanup preview</h2>
          <Result value={preview} />
          <ConfirmButton
            disabled={!preview.eligible?.length || action.isPending}
            message="Remove the selected still-missing catalog entries and caches? Originals are untouched; eligibility is checked again."
            onConfirm={() =>
              action.mutate(
                {
                  path: "/admin/cleanup",
                  body: { ids: preview.eligible, confirm: true },
                },
                {
                  onSuccess: (value) => {
                    setResult(value);
                    setSelected([]);
                    setPreview(null);
                  },
                },
              )
            }
          >
            Confirm eligible cleanup
          </ConfirmButton>
        </section>
      )}
      <Result value={result} />
      <p className="muted">
        {offline.data?.total ?? "…"} offline / missing records. This is a bounded list; verify or
        clean selected items to reveal further records.
      </p>
      {offline.isPending && <Pending />}
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Select</th>
              <th>Work</th>
              <th>Status</th>
              <th>Index only</th>
            </tr>
          </thead>
          <tbody>
            {offline.data?.items.map((work) => (
              <tr key={work.id}>
                <td>
                  <input
                    type="checkbox"
                    aria-label={`Select ${work.title}`}
                    checked={selected.includes(work.id)}
                    onChange={() => toggle(work.id)}
                  />
                </td>
                <td>
                  {work.title}
                  <small>
                    {work.root_id} · {work.relative_path}
                    <br />
                    {work.id}
                  </small>
                </td>
                <td>
                  {work.index_status}
                  <small className="error-text">{work.error_code}</small>
                </td>
                <td>
                  <ConfirmButton
                    disabled={action.isPending}
                    message={`Delete only the index/cache for ${work.title}? The original remains untouched.`}
                    onConfirm={() =>
                      action.mutate({
                        path: `/admin/works/${encoded(work.id)}`,
                        method: "DELETE",
                        body: { confirm: true },
                      })
                    }
                  >
                    Remove index/cache
                  </ConfirmButton>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function Moves() {
  const moves = useAPI<Move[]>(["moves"], "/admin/moves"),
    action = useAction();
  return (
    <section className="panel">
      <h2>Possible cross-root moves</h2>
      <p>
        Equal size and mtime are only candidate signals, not proof. Confirmation keeps the old work
        ID and metadata, adopts destination permissions, and reindexes only that work.
      </p>
      <ErrorNotice error={moves.error || action.error} />
      {moves.data?.map((move) => (
        <div key={move.id} className="panel">
          <p>
            <code>
              {move.from_root} / {move.from_path}
            </code>{" "}
            →{" "}
            <code>
              {move.to_root} / {move.to_path}
            </code>
          </p>
          <ConfirmButton
            disabled={action.isPending}
            message="Confirm these are the same logical work? Destination metadata must be empty. Originals remain unchanged."
            onConfirm={() =>
              action.mutate({
                path: `/admin/moves/${encoded(move.id)}`,
                body: { action: "approve", confirm: true },
              })
            }
          >
            Confirm move
          </ConfirmButton>{" "}
          <button
            disabled={action.isPending}
            onClick={() =>
              action.mutate({
                path: `/admin/moves/${encoded(move.id)}`,
                body: { action: "reject" },
              })
            }
          >
            Keep separate
          </button>
        </div>
      ))}
      {moves.data?.length === 0 && <p className="muted">No pending move candidates.</p>}
    </section>
  );
}
function Providers() {
  const providers = useAPI<Providers>(["providers"], "/admin/providers");
  return (
    <>
      <ErrorNotice error={providers.error} />
      <section className="panel">
        <h2>Network routing</h2>
        <p>
          Provider requests use the server's configured DNS/DoH and proxy policy. No credentials are
          returned to this browser.
        </p>
        <dl>
          {Object.entries(providers.data?.network || {}).map(([key, value]) => (
            <React.Fragment key={key}>
              <dt>{key}</dt>
              <dd>{String(value || "Not configured")}</dd>
            </React.Fragment>
          ))}
        </dl>
      </section>
      {providers.isPending && <Pending />}
      {providers.data?.providers.map((provider) => (
        <ProviderEditor key={provider.id} provider={provider} />
      ))}
      <CrawlForm providers={providers.data?.providers || []} />
    </>
  );
}
function ProviderEditor({ provider }: { provider: Provider }) {
  const action = useAction(),
    [error, setError] = useState<unknown>(null);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget,
      data = new FormData(form);
    setError(null);
    try {
      const body: Record<string, unknown> = {
        enabled: data.has("enabled"),
        priority: Number(data.get("priority")),
      };
      if (text(data, "config").trim()) body.config = json(data.get("config"));
      if (data.get("credentials")) body.credentials = json(data.get("credentials"));
      action.mutate(
        {
          path: `/admin/providers/${encoded(provider.id)}`,
          method: "PUT",
          body,
        },
        {
          onSuccess: () => {
            const input = form.elements.namedItem("credentials");
            if (input instanceof HTMLInputElement) input.value = "";
          },
        },
      );
    } catch (error) {
      setError(error);
    }
  }
  return (
    <details className="panel">
      <summary>
        {provider.name}{" "}
        <span className="badge">
          {provider.enabled ? "enabled" : "disabled"} · {provider.status}
        </span>
      </summary>
      <p className="muted">
        {provider.id} · {provider.kind} · {provider.version} · Credentials{" "}
        {provider.credential_configured ? "configured" : "not configured"}
      </p>
      <form onSubmit={submit}>
        <Check name="enabled" checked={provider.enabled}>
          Enable provider
        </Check>
        <label>
          Field priority (lower wins)
          <input
            type="number"
            name="priority"
            min="0"
            max="1000"
            defaultValue={provider.priority ?? 100}
            required
          />
        </label>
        <JSONField
          name="config"
          label="Provider configuration (JSON object; blank preserves current)"
          value={provider.config || ""}
        />
        <label>
          Replace credentials (JSON object; blank preserves stored credentials)
          <input
            type="password"
            name="credentials"
            autoComplete="off"
            placeholder='{"cookie":"…"}'
          />
        </label>
        <p className="small muted">
          For e-hentai, supply the provider's cookie credentials; nhentai / chaika may accept
          cookies or tokens. Stored secret values are never shown. Local JSON needs no credentials.
        </p>
        <button disabled={action.isPending}>Save provider</button>
      </form>
      <ConfirmButton
        disabled={action.isPending}
        message={`Clear stored credentials for ${provider.name}?`}
        onConfirm={() =>
          action.mutate({
            path: `/admin/providers/${encoded(provider.id)}`,
            method: "PUT",
            body: { clear_credentials: true },
          })
        }
      >
        Clear credentials
      </ConfirmButton>
      <ErrorNotice error={error || action.error} />
      {action.isSuccess && <p role="status">Provider updated.</p>}
    </details>
  );
}
function CrawlForm({ providers, workId }: { providers: Provider[]; workId?: string }) {
  const roots = useAPI<Root[]>(["roots"], "/roots"),
    action = useAction(),
    [error, setError] = useState<unknown>(null),
    [result, setResult] = useState<unknown>(null);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    const data = new FormData(event.currentTarget);
    try {
      const body: Record<string, unknown> = { provider: text(data, "provider") };
      if (workId) body.work_ids = [workId];
      else {
        if (data.get("work_ids")) body.work_ids = list(data.get("work_ids"));
        if (data.get("root_id")) body.root_id = text(data, "root_id");
        if (data.get("query")) body.query = text(data, "query");
      }
      if (data.get("local_json")) body.local_json = json(data.get("local_json"));
      if (
        (event.nativeEvent instanceof SubmitEvent &&
        event.nativeEvent.submitter instanceof HTMLButtonElement
          ? event.nativeEvent.submitter.value
          : undefined) === "preview"
      )
        body.preview = true;
      if (
        !body.preview &&
        !workId &&
        !window.confirm("Enqueue crawler work for this selected scope?")
      )
        return;
      action.mutate({ path: "/admin/crawl", body }, { onSuccess: setResult });
    } catch (error) {
      setError(error);
    }
  }
  return (
    <section className="panel">
      <h2>{workId ? "Crawl this work / import JSON" : "Crawl or import local JSON"}</h2>
      <form onSubmit={submit}>
        <div className="form-grid">
          <label>
            Provider
            <select name="provider" required>
              {providers.map((provider) => (
                <option key={provider.id} value={provider.id}>
                  {provider.name} ({provider.id})
                </option>
              ))}
            </select>
          </label>
          {!workId && (
            <>
              <label>
                Root
                <select name="root_id">
                  <option value="">Unrestricted scope</option>
                  {roots.data?.map((root) => (
                    <option key={root.id} value={root.id}>
                      {root.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Work IDs
                <textarea name="work_ids" rows={2} />
              </label>
              <label>
                Search scope
                <input name="query" />
              </label>
            </>
          )}
        </div>
        <JSONField
          name="local_json"
          label="Local JSON metadata (only for local-json; leave blank for network providers)"
          value=""
          rows={6}
        />
        <p className="small muted">
          Normalized import: fields object (title, description, …), tags array (namespace:value),
          optional source_id and source_url. Ambiguous matches remain candidates until reviewed.
        </p>
        <div className="toolbar">
          <button name="action" value="preview" disabled={action.isPending}>
            Preview count
          </button>
          <button name="action" value="enqueue" disabled={action.isPending}>
            Enqueue crawl / import
          </button>
        </div>
      </form>
      <ErrorNotice error={error || action.error || roots.error} />
      <Result value={result} />
    </section>
  );
}
function Plugins() {
  const providers = useAPI<Providers>(["providers"], "/admin/providers"),
    action = useAction(),
    [error, setError] = useState<unknown>(null);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget,
      data = new FormData(form);
    setError(null);
    try {
      if (!data.has("trusted")) throw new Error("Explicit trust confirmation is required.");
      action.mutate(
        {
          path: "/admin/plugins",
          body: {
            manifest: json(data.get("manifest")),
            executable: text(data, "executable"),
            confirm_trusted: true,
          },
        },
        { onSuccess: () => form.reset() },
      );
    } catch (error) {
      setError(error);
    }
  }
  return (
    <>
      <p className="notice error">
        <strong>Trusted executable plugins only.</strong> Plugins execute server-side with the
        server user's privileges. They are not a security sandbox. Review the executable, manifest,
        declared hosts and permissions before installation.
      </p>
      <ErrorNotice error={providers.error || action.error || error} />
      {providers.data?.plugins?.map((plugin) => (
        <PluginEditor key={plugin.id} plugin={plugin} />
      ))}
      <section className="panel">
        <h2>Install trusted plugin</h2>
        <form onSubmit={submit}>
          <label>
            Server executable path
            <input name="executable" required placeholder="/opt/archika/plugins/provider" />
          </label>
          <JSONField
            name="manifest"
            label="Plugin manifest (JSON)"
            required
            value={{
              id: "",
              version: "1.0.0",
              protocol_version: 1,
              capabilities: ["metadata"],
              hosts: [],
              permissions: [],
              config_schema: {},
            }}
            rows={12}
          />
          <Check name="trusted">
            I reviewed this executable and trust it to run without a sandbox.
          </Check>
          <button className="danger" disabled={action.isPending}>
            Install trusted executable
          </button>
        </form>
      </section>
    </>
  );
}
function PluginEditor({ plugin }: { plugin: Plugin }) {
  const action = useAction(),
    [error, setError] = useState<unknown>(null);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    const data = new FormData(event.currentTarget);
    try {
      action.mutate({
        path: `/admin/plugins/${encoded(plugin.id)}`,
        method: "PUT",
        body: {
          enabled: data.has("enabled"),
          config: json(data.get("config")),
        },
      });
    } catch (error) {
      setError(error);
    }
  }
  return (
    <details className="panel">
      <summary>
        {plugin.id} · {plugin.version} · {plugin.enabled ? "enabled" : "disabled"}
      </summary>
      <Result value={plugin.manifest} />
      <form onSubmit={submit}>
        <Check name="enabled" checked={plugin.enabled}>
          Enable plugin
        </Check>
        <JSONField name="config" label="Plugin configuration (JSON)" value={plugin.config || {}} />
        <button disabled={action.isPending}>Save plugin</button>
      </form>
      <ConfirmButton
        disabled={action.isPending}
        message={`Uninstall plugin ${plugin.id}?`}
        onConfirm={() =>
          action.mutate({
            path: `/admin/plugins/${encoded(plugin.id)}`,
            method: "DELETE",
            body: { confirm: true },
          })
        }
      >
        Uninstall plugin
      </ConfirmButton>
      <ErrorNotice error={error || action.error} />
    </details>
  );
}
function Audit() {
  const audit = useAPI<AuditRecord[]>(["audit"], "/admin/audit");
  return (
    <section className="panel">
      <h2>Recent audit records</h2>
      <p className="muted">Bounded server audit log; credentials are not displayed.</p>
      <ErrorNotice error={audit.error} />
      {audit.isPending ? <Pending /> : <Result value={audit.data} />}
    </section>
  );
}

export function WorkMetadata({ workId }: { workId: string }) {
  const { user } = useAuth(),
    metadata = useAPI<Metadata>(["metadata", workId], `/works/${encoded(workId)}/metadata`);
  const providers = useAPI<Providers>(["providers"], "/admin/providers", {
    enabled: !!user.admin,
  });
  const action = useAction(),
    [error, setError] = useState<unknown>(null);
  const navigate = useNavigate();
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setError(null);
    try {
      action.mutate({
        path: `/works/${encoded(workId)}/metadata`,
        method: "PUT",
        body: {
          fields: json(data.get("fields")),
          reset_fields: list(data.get("reset_fields")),
          add_tags: list(data.get("add_tags")),
          remove_tags: list(data.get("remove_tags")),
          reset_tags: list(data.get("reset_tags")),
        },
      });
    } catch (error) {
      setError(error);
    }
  }
  return (
    <>
      <h2>Metadata & provenance</h2>
      <ErrorNotice error={metadata.error || action.error || error} />
      {metadata.isPending && <Pending />}
      <dl>
        {Object.entries(metadata.data?.fields || {}).map(([field, detail]) => (
          <React.Fragment key={field}>
            <dt>{field}</dt>
            <dd>
              {typeof detail === "object" ? (
                <>
                  {String(detail.value ?? "")}
                  <small>
                    {detail.origin || "unknown source"}
                    {detail.manual ? " · manual override" : ""}
                  </small>
                </>
              ) : (
                String(detail)
              )}
            </dd>
          </React.Fragment>
        ))}
      </dl>
      <div className="chips">
        {metadata.data?.tags?.map((tag) => (
          <span key={tag}>{tag}</span>
        ))}
      </div>
      <details>
        <summary>Provider snapshots</summary>
        <Result value={metadata.data?.providers} />
      </details>
      {user.admin && (
        <>
          <details className="panel">
            <summary>Edit manual fields & tags</summary>
            <p className="small muted">
              Manual overrides take precedence. Use null to clear a field or reset its override by
              name to restore provider data. Filename display titles are never rewritten by filename
              parsing.
            </p>
            <form onSubmit={submit}>
              <JSONField
                name="fields"
                label="Field overrides (JSON object; string or null values)"
                value={{}}
              />
              <label>
                Reset field overrides (comma or newline)
                <textarea name="reset_fields" rows={2} />
              </label>
              <div className="form-grid">
                <label>
                  Add tags
                  <textarea name="add_tags" rows={3} />
                </label>
                <label>
                  Remove tags
                  <textarea name="remove_tags" rows={3} />
                </label>
                <label>
                  Reset tag overrides
                  <textarea name="reset_tags" rows={3} />
                </label>
              </div>
              <button disabled={action.isPending}>Save metadata changes</button>
            </form>
          </details>
          <section>
            <h3>Match candidates</h3>
            {!metadata.data?.candidates?.length && (
              <p className="muted">No candidates awaiting review.</p>
            )}
            {metadata.data?.candidates?.map((candidate) => (
              <article className="panel" key={candidate.id}>
                <h4>{candidate.title}</h4>
                <p>
                  {candidate.provider} · {candidate.source_id} · confidence {candidate.confidence}
                </p>
                {/^https?:\/\//.test(candidate.source_url || "") && (
                  <a href={candidate.source_url} rel="noreferrer" target="_blank">
                    View source ↗
                  </a>
                )}
                <Result value={candidate.evidence} />
                <div className="toolbar">
                  <button
                    disabled={action.isPending}
                    onClick={() =>
                      action.mutate({
                        path: `/admin/candidates/${encoded(candidate.id)}`,
                        body: { action: "approve" },
                      })
                    }
                  >
                    Approve match
                  </button>
                  <button
                    disabled={action.isPending}
                    onClick={() =>
                      action.mutate({
                        path: `/admin/candidates/${encoded(candidate.id)}`,
                        body: { action: "reject" },
                      })
                    }
                  >
                    Reject match
                  </button>
                </div>
              </article>
            ))}
          </section>
          <CrawlForm providers={providers.data?.providers || []} workId={workId} />
          <hr />
          <p className="small muted">
            Remove this work from the index and cache only. No original file is modified or deleted.
          </p>
          <ConfirmButton
            disabled={action.isPending}
            message="Remove this work's catalog entry and cached representations? The original is untouched."
            onConfirm={() =>
              action.mutate(
                {
                  path: `/admin/works/${encoded(workId)}`,
                  method: "DELETE",
                  body: { confirm: true },
                },
                { onSuccess: () => navigate({ to: "/" }) },
              )
            }
          >
            Remove index / cache
          </ConfirmButton>
        </>
      )}
    </>
  );
}
