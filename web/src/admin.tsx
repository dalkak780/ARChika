import type { TFunction } from "i18next";
import i18n, { statusLabel, isErrorCode, serverText } from "./i18n.ts";
import { Trans, useTranslation } from "react-i18next";
import { en } from "./locales/en.ts";
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
function providerName(provider: Provider, t: TFunction) {
  return provider.kind === "builtin" && provider.id === "local-json"
    ? t("admin.localJson")
    : provider.name;
}
function adminTabs(t: TFunction) {
  return [
    {
      id: "roots",
      label: t("library.roots"),
      description: t("admin.manageLibraryFoldersAndTheirFilesystemSettings"),
    },
    {
      id: "users",
      label: t("admin.users"),
      description: t("admin.createAccountsAndGrantAccessToLibraryRoots"),
    },
    {
      id: "jobs",
      label: t("admin.jobs"),
      description: t("admin.runDiscoveryIndexingThumbnailsAndMetadataJobs"),
    },
    {
      id: "offline",
      label: t("admin.offline"),
      description: t("admin.reviewMovedOrMissingItemsBeforeCleaningUpThe"),
    },
    {
      id: "providers",
      label: t("admin.providers"),
      description: t("admin.configureMetadataSourcesAndFetchOrImportMetadata"),
    },
    {
      id: "plugins",
      label: t("admin.plugins"),
      description: t("admin.installAndConfigureTrustedServerPlugins"),
    },
    {
      id: "audit",
      label: t("admin.audit"),
      description: t("admin.reviewRecentAdministrativeActivity"),
    },
  ];
}
function availableJobTypes(t: TFunction) {
  return [
    {
      id: "scan",
      label: t("admin.discoverScan"),
      help: t("admin.findNewOrChangedArchivesInARoot"),
    },
    {
      id: "reconcile",
      label: t("admin.reconcileFilesystem"),
      help: t("admin.verifyTheCatalogAgainstTheFilesystemAndMarkMissing"),
    },
    {
      id: "index_bulk",
      label: t("admin.indexPageLists"),
      help: t("admin.readPageListsForTheSelectedWorks"),
    },
    {
      id: "thumbnail",
      label: t("admin.generateThumbnails"),
      help: t("admin.preGenerateCoverThumbnailsForTheSelectedWorks"),
    },
    {
      id: "crawl_bulk",
      label: t("admin.crawlMetadata"),
      help: t("admin.fetchMetadataFromAProvider"),
    },
  ];
}
export function jobActions(state: string) {
  switch (state) {
    case "queued":
    case "running":
      return ["pause", "cancel"];
    case "paused":
      return ["resume", "cancel"];
    case "failed":
    case "cancelled":
      return ["retry"];
    default:
      return [];
  }
}
function relativeTime(timestamp: number, language: string) {
  const seconds = Math.round((timestamp - Date.now()) / 1000);
  const format = new Intl.RelativeTimeFormat(language, { numeric: "auto" });
  if (Math.abs(seconds) < 60) return format.format(seconds, "second");
  if (Math.abs(seconds) < 3600) return format.format(Math.round(seconds / 60), "minute");
  if (Math.abs(seconds) < 86400) return format.format(Math.round(seconds / 3600), "hour");
  return format.format(Math.round(seconds / 86400), "day");
}
const formText = (value: FormDataEntryValue | null) => {
  if (value !== null && typeof value !== "string")
    throw new Error(i18n.t("admin.expectedATextFormField"));
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
function resultLabel(key: string, t: TFunction) {
  return isResultKey(key) ? t(`result.${key}`) : key;
}
function isResultKey(key: string): key is keyof typeof en.result {
  return Object.hasOwn(en.result, key);
}
function localizedResult(value: unknown, language: string, t: TFunction, field = ""): unknown {
  if (language !== "ko") return value;
  if (["config", "manifest", "fields", "tags"].includes(field)) return value;
  if (Array.isArray(value)) return value.map((item) => localizedResult(item, language, t, field));
  if (value && typeof value === "object")
    return Object.fromEntries(
      Object.entries(value).map(([key, child]) => [
        resultLabel(key, t),
        localizedResult(child, language, t, key),
      ]),
    );
  if (typeof value === "number" && ["created", "updated"].includes(field))
    return new Date(value).toLocaleString(language);
  if (typeof value === "string") {
    if (field === "detail") {
      try {
        const detail: unknown = JSON.parse(value);
        return localizedResult(detail, language, t);
      } catch {
        return value;
      }
    }
    if (
      ["state", "status", "index_status", "origin", "kind", "type", "action", "mode"].includes(
        field,
      )
    )
      return statusLabel(value, t);
    if (["warning", "findings"].includes(field)) return serverText(value, t);
  }
  return value;
}
export function Result({
  value,
  job = false,
  raw = false,
}: {
  value: unknown;
  job?: boolean;
  raw?: boolean;
}) {
  const { t, i18n } = useTranslation();
  if (value && typeof value === "object") {
    let message = "";
    if (job && "count" in value && typeof value.count === "number")
      message = t("admin.jobCount", { count: value.count });
    else if (job && "id" in value && typeof value.id === "string") message = t("admin.jobQueued");
    else if ("ok" in value && value.ok === true && Object.keys(value).length === 1)
      message = t("admin.saved");
    if (message)
      return (
        <p className="notice" role="status">
          {message}
        </p>
      );
  }
  return value ? (
    <pre className="result" role="status">
      {JSON.stringify(raw ? value : localizedResult(value, i18n.language, t), null, 2)}
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
  const { t } = useTranslation();
  const { user } = useAuth(),
    { tab } = route.useSearch();
  if (!user.admin)
    return (
      <main id="main">
        <h1>{t("admin.administratorAccessRequired")}</h1>
        <Link to="/">{t("nav.library")}</Link>
      </main>
    );
  return (
    <main id="main" className="admin">
      <div className="page-heading">
        <div>
          <div className="eyebrow">{t("admin.systemCatalog")}</div>
          <h1>{t("nav.administration")}</h1>
        </div>
        <span className="badge">{t("admin.administrator")}</span>
      </div>
      <nav className="tabs" aria-label={t("admin.administrationSections")}>
        {adminTabs(t).map(({ id: name, label }) => (
          <Link
            key={name}
            to="/admin"
            search={{ tab: name }}
            activeOptions={{ exact: true, includeSearch: true }}
            className={tab === name ? "selected" : ""}
          >
            {label}
          </Link>
        ))}
      </nav>
      <p className="muted">{adminTabs(t).find((item) => item.id === tab)?.description}</p>
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
  const { t } = useTranslation();
  const roots = useAPI<Root[]>(["roots"], "/roots"),
    preview = useAPI<ConfigPreview>(["config-preview"], "/admin/config/preview"),
    providers = useAPI<Providers>(["providers"], "/admin/providers");
  const action = useAction();
  return (
    <>
      <section className="panel">
        <h2>{t("admin.gettingStarted")}</h2>
        <p className="muted">{t("admin.setUpYourLibraryInThisOrder")}</p>
        <ol className="getting-started">
          <li>
            <Trans
              i18nKey="admin.setupRoots"
              values={{
                configured: roots.data ? t("admin.configured", { count: roots.data.length }) : "",
              }}
              components={{ link: <Link to="/admin" search={{ tab: "roots" }} /> }}
            />
          </li>
          <li>
            <Trans
              i18nKey="admin.setupJobs"
              components={{ link: <Link to="/admin" search={{ tab: "jobs" }} /> }}
            />
          </li>
          <li>
            <Trans
              i18nKey="admin.setupUsers"
              components={{ link: <Link to="/admin" search={{ tab: "users" }} /> }}
            />
          </li>
          <li>
            <Trans
              i18nKey="admin.setupProviders"
              components={{ link: <Link to="/admin" search={{ tab: "providers" }} /> }}
            />
          </li>
          <li>
            <Trans
              i18nKey="admin.setupOffline"
              components={{ link: <Link to="/admin" search={{ tab: "offline" }} /> }}
            />
          </li>
        </ol>
      </section>
      <p className="notice">{t("admin.originalFilesAreReadOnlyAddingARootDoes")}</p>
      <ErrorNotice error={roots.error || preview.error || action.error || providers.error} />
      <section className="panel">
        <h2>{t("admin.changesFromConfigFile")}</h2>
        <p className="muted">{t("admin.theseAreDifferencesBetweenArchikaYamlAndTheCatalog")}</p>
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
                <span className="config-change" title={change.existing_id || change.key}>
                  <strong>{change.name}</strong>
                  <span className="muted">{change.path}</span>
                  <span className="badge">{statusLabel(change.action, t)}</span>
                </span>
              </Check>
            ))}
            {!preview.data.changes.length && (
              <p className="muted">{t("admin.catalogMatchesTheConfigFile")}</p>
            )}
            <button disabled={action.isPending || !preview.data.changes.length}>
              {t("admin.applySelectedChanges")}
            </button>
            <Result value={action.data} />
          </form>
        )}
      </section>
      {roots.isPending && <Pending />}
      {roots.data?.map((root) => (
        <RootEditor key={root.id} root={root} providers={providers.data?.providers || []} />
      ))}
      <details className="panel">
        <summary>{t("admin.addReadOnlyRoot")}</summary>
        <RootForm providers={providers.data?.providers || []} />
      </details>
    </>
  );
}
function RootForm({ root = {}, providers = [] }: { root?: Partial<Root>; providers?: Provider[] }) {
  const { t } = useTranslation();
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
          {t("admin.stableConfigurationKey")}
          <input name="key" defaultValue={root.key} required pattern="[A-Za-z0-9_.-]+" />
        </label>
        <label>
          {t("admin.displayName")}
          <input name="name" defaultValue={root.name} required />
        </label>
        <label className="full">
          {t("admin.serverPath")}
          <input name="path" defaultValue={root.path} required />
        </label>
        <label>
          {t("admin.watcherQuietPeriodSeconds")}
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
          {t("admin.filenameParsingRegex")}
          <input
            name="parser_regex"
            defaultValue={root.parser_regex || ""}
            placeholder={t("admin.namedCapturesProduceTagsNotTitles")}
          />
        </label>
      </div>
      <div className="toolbar">
        <Check name="watch" checked={root.watch}>
          {t("admin.watchChanges")}
        </Check>
        <Check name="auto_crawl" checked={root.auto_crawl}>
          {t("admin.automaticallyCrawlChangedWorks")}
        </Check>
        <Check name="parser_enabled" checked={root.parser_enabled}>
          {t("admin.parseFilenameTags")}
        </Check>
        <Check name="trust_identity" checked={root.trust_identity}>
          {t("admin.trustFilesystemIdentity")}
        </Check>
      </div>
      <fieldset>
        <legend>{t("admin.allowedCrawlerProviders")}</legend>
        {providers.map((provider) => (
          <Check
            key={provider.id}
            name="providers"
            value={provider.id}
            checked={root.providers?.includes(provider.id)}
          >
            {providerName(provider, t)}
          </Check>
        ))}
      </fieldset>
      <p className="muted small">
        {t("admin.aPathChangePerformsBoundedRelocationVerificationReviewFindings")}
      </p>
      <button className="primary" disabled={action.isPending}>
        {root.id ? t("admin.saveRootVerifyRelocation") : t("admin.addRoot")}
      </button>
      <ErrorNotice error={error || action.error} />
      <Result value={result} />
    </form>
  );
}
function RootEditor({ root, providers }: { root: Root; providers: Provider[] }) {
  const { t } = useTranslation();
  const action = useAction(),
    [preview, setPreview] = useState<unknown>(null);
  return (
    <details className="panel">
      <summary>
        {root.name} <span className="badge">{statusLabel(root.status, t)}</span>{" "}
        <span className="muted">{root.path}</span>
      </summary>
      {root.warning && <p className="notice error">{serverText(root.warning, t)}</p>}
      {root.pending_path && (
        <div className="notice">
          <strong>
            {t("admin.pendingRelocation")}
            {root.pending_path}
          </strong>
          <p>{t("admin.forceMayAdoptTheWrongCollectionReconcileExplicitlyTraverses")}</p>
          <ConfirmButton
            message={t("admin.forceThisPendingRootRelocationVerifyTheTargetIs")}
            disabled={action.isPending}
            onConfirm={() =>
              action.mutate({
                path: `/admin/roots/${encoded(root.id)}/relocation`,
                body: { action: "force", confirm: true },
              })
            }
          >
            {t("admin.forceRelocation")}
          </ConfirmButton>{" "}
          <ConfirmButton
            message={t("admin.startExplicitReconciliationOfThePendingRoot")}
            disabled={action.isPending}
            onConfirm={() =>
              action.mutate({
                path: `/admin/roots/${encoded(root.id)}/relocation`,
                body: { action: "reconcile", confirm: true },
              })
            }
          >
            {t("admin.reconcileTarget")}
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
        {t("admin.previewRemovingRootMappings")}
      </button>
      {Boolean(preview) && (
        <>
          <Result value={preview} />
          <ConfirmButton
            message={t("admin.removeRoot", { name: root.name })}
            disabled={action.isPending}
            onConfirm={() =>
              action.mutate({
                path: `/admin/roots/${encoded(root.id)}`,
                method: "DELETE",
                body: { confirm: true },
              })
            }
          >
            {t("admin.removeRootMappings")}
          </ConfirmButton>
        </>
      )}
      <ErrorNotice error={action.error} />
    </details>
  );
}
function Users() {
  const { t } = useTranslation();
  const users = useAPI<AdminUser[]>(["users"], "/admin/users"),
    roots = useAPI<Root[]>(["roots"], "/roots");
  return (
    <>
      <section className="panel">
        <h2>{t("admin.userAccounts")}</h2>
        <p className="muted">
          {t("admin.rootGrantsAreIndependentAdministratorAccountsHaveAdministrativeAccess")}
        </p>
      </section>
      <ErrorNotice error={users.error || roots.error} />
      {users.isPending && <Pending />}
      {users.data?.map((user) => (
        <details className="panel" key={user.id}>
          <summary>
            {user.name}{" "}
            <span className="badge">
              {statusLabel(user.admin ? "admin" : "reader", t)}
              {user.disabled ? ` · ${statusLabel("disabled", t)}` : ""}
            </span>
          </summary>
          <UserForm user={user} roots={roots.data || []} />
        </details>
      ))}
      <section className="panel">
        <h2>{t("admin.createUser")}</h2>
        <p className="muted">{t("admin.chooseAnAccountRoleAndTheRootsThisUser")}</p>
        <UserForm roots={roots.data || []} />
      </section>
    </>
  );
}
function UserForm({ user = {}, roots }: { user?: Partial<AdminUser>; roots: Root[] }) {
  const { t } = useTranslation();
  const action = useAction();
  const [savedName, setSavedName] = useState("");
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
          if (!user.id) {
            setSavedName(text(data, "name"));
            form.reset();
          }
        },
      },
    );
  }
  return (
    <form onSubmit={submit}>
      <div className="form-grid">
        {!user.id && (
          <label>
            {t("auth.username")}
            <input name="name" autoComplete="off" required />
          </label>
        )}
        <label>
          {user.id ? t("admin.newPasswordBlankKeepsCurrent") : t("auth.password")}
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
          {t("admin.administrator")}
        </Check>
        {user.id && (
          <Check name="disabled" checked={user.disabled}>
            {t("admin.disabled")}
          </Check>
        )}
      </div>
      <fieldset>
        <legend>{t("admin.rootAccess")}</legend>
        {roots.map((root) => (
          <Check key={root.id} name="roots" value={root.id} checked={user.roots?.includes(root.id)}>
            {root.name}
          </Check>
        ))}
      </fieldset>
      <button className="primary" disabled={action.isPending}>
        {user.id ? t("admin.updateAccount") : t("admin.createAccount")}
      </button>
      <ErrorNotice error={action.error} />
      {action.isSuccess && (
        <p className="notice" role="status">
          {user.id ? t("admin.accountSaved") : t("admin.createdUser", { name: savedName })}
        </p>
      )}
    </form>
  );
}

// Child jobs inherit the parent's origin, so only top-level types count as direct work.
const directJobTypes = new Set([
  "scan",
  "reconcile",
  "index_bulk",
  "thumbnail",
  "crawl_bulk",
  "metadata_remerge",
  "filename_reparse",
]);
function Jobs() {
  const { t, i18n } = useTranslation();
  const jobs = useAPI<Job[]>(["jobs"], "/admin/jobs", { refetchInterval: 3000 }),
    roots = useAPI<Root[]>(["roots"], "/roots"),
    providers = useAPI<Providers>(["providers"], "/admin/providers");
  const action = useAction(),
    [preview, setPreview] = useState<unknown>(null),
    [error, setError] = useState<unknown>(null);
  const [type, setType] = useState("scan"),
    [showAutomatic, setShowAutomatic] = useState(false);
  const isDirect = (job: Job) => job.origin === "manual" && directJobTypes.has(job.type);
  const hidden = jobs.data?.filter((job) => !isDirect(job)).length ?? 0;
  const visible = jobs.data?.filter((job) => showAutomatic || isDirect(job));
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
    if (!isPreview && !window.confirm(t("admin.enqueueThisExplicitBackgroundJobItMayTraverseThe")))
      return;
    action.mutate({ path: "/admin/jobs", body }, { onSuccess: setPreview });
  }
  return (
    <>
      <section className="panel">
        <h2>{t("admin.explicitBackgroundWork")}</h2>
        <p className="muted">{t("admin.noStartupScanOrLibraryWidePreconversionIsPerformed")}</p>
        <form onSubmit={submit}>
          <div className="form-grid">
            <label>
              {t("admin.jobType")}
              <select
                name="type"
                value={type}
                onChange={(event) => {
                  setType(event.target.value);
                  setPreview(null);
                }}
              >
                {availableJobTypes(t).map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.label}
                  </option>
                ))}
              </select>
              <small>{availableJobTypes(t).find((item) => item.id === type)?.help}</small>
            </label>
            <label>
              {t("library.root")}
              <select name="root_id">
                <option value="">{t("admin.allPermittedRoots")}</option>
                {roots.data?.map((root) => (
                  <option key={root.id} value={root.id}>
                    {root.name} · {statusLabel(root.status, t)}
                  </option>
                ))}
              </select>
            </label>
            {type !== "scan" && type !== "reconcile" && (
              <>
                <label>
                  {t("admin.workIdsCommaOrNewline")}
                  <textarea name="work_ids" rows={2} />
                </label>
                <label>
                  {t("admin.searchScope")}
                  <input name="query" placeholder={t("admin.optionalAdvancedQuery")} />
                </label>
              </>
            )}
            {type === "crawl_bulk" && (
              <label>
                {t("admin.provider")}
                <select name="provider" required>
                  <option value="">{t("admin.selectProvider")}</option>
                  {providers.data?.providers.map((provider) => (
                    <option key={provider.id} value={provider.id}>
                      {providerName(provider, t)}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
          <div className="toolbar">
            <button name="action" value="preview" disabled={action.isPending}>
              {t("admin.previewCount")}
            </button>
            <button className="primary" name="action" value="enqueue" disabled={action.isPending}>
              {t("admin.enqueueJob")}
            </button>
          </div>
        </form>
        <Result value={preview} job />
      </section>
      <section className="panel">
        <div className="queue-heading">
          <div>
            <h2>{t("admin.jobQueue")}</h2>
            <p className="muted">{t("admin.monitorProgressAndManageQueuedWork")}</p>
          </div>
          <div className="toolbar">
            <button
              disabled={action.isPending}
              onClick={() => action.mutate({ path: "/admin/jobs/pause", body: { paused: true } })}
            >
              {t("admin.pauseAllJobs")}
            </button>
            <button
              disabled={action.isPending}
              onClick={() =>
                action.mutate({
                  path: "/admin/jobs/pause",
                  body: { paused: false },
                })
              }
            >
              {t("admin.resumeAllJobs")}
            </button>
          </div>
        </div>
        <label className="check">
          <input
            type="checkbox"
            checked={showAutomatic}
            onChange={(event) => setShowAutomatic(event.target.checked)}
          />
          {t("admin.showAutomaticJobs")}
          <span className="muted small">
            {t("admin.hidden", { count: showAutomatic ? 0 : hidden })}
          </span>
        </label>
        <ErrorNotice
          error={jobs.error || roots.error || providers.error || action.error || error}
        />
        {jobs.isPending && <Pending />}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t("admin.job")}</th>
                <th>{t("admin.state")}</th>
                <th>{t("admin.progress")}</th>
                <th>{t("admin.scopeError")}</th>
                <th>{t("admin.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {visible?.map((job) => (
                <tr key={job.id}>
                  <td>
                    <strong title={job.id}>
                      {availableJobTypes(t).find((item) => item.id === job.type)?.label ||
                        statusLabel(job.type, t)}
                    </strong>
                    <small>{new Date(job.created).toLocaleString(i18n.language)}</small>
                    <small title={new Date(job.updated).toLocaleString(i18n.language)}>
                      {t("admin.updated")}
                      {relativeTime(job.updated, i18n.language)}
                    </small>
                  </td>
                  <td>{statusLabel(job.state, t)}</td>
                  <td>
                    <progress
                      aria-label={t("admin.jobProgress")}
                      value={job.done}
                      max={Math.max(1, job.total)}
                    />
                    <small>
                      {job.done} / {job.total}
                    </small>
                    {job.failed > 0 && (
                      <small className="error-text">
                        {job.failed}
                        {t("admin.failed")}
                      </small>
                    )}
                  </td>
                  <td>
                    {roots.data?.find((root) => root.id === job.root_id)?.name ||
                      (job.root_id ? t("admin.unknownRoot") : job.work_id || t("admin.bulk"))}
                    <small className="error-text">{job.error}</small>
                  </td>
                  <td>
                    <div className="toolbar">
                      {jobActions(job.state).map((command) => (
                        <button
                          key={command}
                          className={command === "cancel" ? "danger" : ""}
                          disabled={action.isPending}
                          onClick={() =>
                            action.mutate({
                              path: `/admin/jobs/${encoded(job.id)}`,
                              body: { action: command },
                            })
                          }
                        >
                          {statusLabel(command, t)}
                        </button>
                      ))}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {visible?.length === 0 && (
          <p className="muted">
            {t("admin.noJobs", { scope: showAutomatic ? "" : t("admin.manualJobs") })}
          </p>
        )}
      </section>
    </>
  );
}
function Offline() {
  const { t, i18n } = useTranslation();
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
    <section className="panel">
      <h2>{t("admin.missingItems")}</h2>
      <p className="muted">{t("admin.verifyMissingFilesOrPreviewRemovalOfTheirCatalog")}</p>
      <p className="notice">{t("admin.anUnavailableRootIsNotProofThatItsWorks")}</p>
      <ErrorNotice error={offline.error || action.error} />
      <div className="toolbar">
        <button
          onClick={() => {
            setSelected(offline.data?.items.map((item) => item.id) || []);
            setPreview(null);
          }}
        >
          {t("admin.selectListed")}
        </button>
        <button
          onClick={() => {
            setSelected([]);
            setPreview(null);
          }}
        >
          {t("admin.clearSelection")}
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
          {t("admin.verifyRestoreSelected")}
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
          {t("admin.previewCleanup")}
        </button>
      </div>
      {preview && "eligible" in preview && (
        <section className="panel">
          <h2>{t("admin.cleanupPreview")}</h2>
          <Result value={preview} />
          <ConfirmButton
            disabled={!preview.eligible?.length || action.isPending}
            message={t("admin.removeTheSelectedStillMissingCatalogEntriesAndCaches")}
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
            {t("admin.confirmEligibleCleanup")}
          </ConfirmButton>
        </section>
      )}
      <Result value={result} />
      <p className="muted">
        {offline.data?.total ?? "…"}
        {t("admin.offlineMissingRecordsThisIsABoundedListVerify")}
      </p>
      {offline.isPending && <Pending />}
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{t("admin.select")}</th>
              <th>{t("admin.work")}</th>
              <th>{t("admin.status")}</th>
              <th>{t("admin.indexOnly")}</th>
            </tr>
          </thead>
          <tbody>
            {offline.data?.items.map((work) => (
              <tr key={work.id}>
                <td>
                  <input
                    type="checkbox"
                    aria-label={t("admin.selectWork", { title: work.title })}
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
                  {statusLabel(work.index_status, t)}
                  <small className="error-text">
                    {i18n.resolvedLanguage === "ko" && isErrorCode(work.error_code)
                      ? t(`errors.${work.error_code}`)
                      : work.error_code}
                  </small>
                </td>
                <td>
                  <ConfirmButton
                    disabled={action.isPending}
                    message={t("admin.removeWork", { title: work.title })}
                    onConfirm={() =>
                      action.mutate({
                        path: `/admin/works/${encoded(work.id)}`,
                        method: "DELETE",
                        body: { confirm: true },
                      })
                    }
                  >
                    {t("admin.removeIndexCache")}
                  </ConfirmButton>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Moves() {
  const { t } = useTranslation();
  const moves = useAPI<Move[]>(["moves"], "/admin/moves"),
    action = useAction();
  return (
    <section className="panel">
      <h2>{t("admin.possibleCrossRootMoves")}</h2>
      <p className="muted">{t("admin.equalSizeAndMtimeAreOnlyCandidateSignalsNot")}</p>
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
            message={t("admin.confirmTheseAreTheSameLogicalWorkDestinationMetadata")}
            onConfirm={() =>
              action.mutate({
                path: `/admin/moves/${encoded(move.id)}`,
                body: { action: "approve", confirm: true },
              })
            }
          >
            {t("admin.confirmMove")}
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
            {t("admin.keepSeparate")}
          </button>
        </div>
      ))}
      {moves.data?.length === 0 && <p className="muted">{t("admin.noPendingMoveCandidates")}</p>}
    </section>
  );
}
function Providers() {
  const { t } = useTranslation();
  const providers = useAPI<Providers>(["providers"], "/admin/providers");
  return (
    <>
      <ErrorNotice error={providers.error} />
      <section className="panel">
        <h2>{t("admin.networkRouting")}</h2>
        <p className="muted">{t("admin.providerRequestsUseTheServerSConfiguredDnsDoh")}</p>
        <dl>
          {Object.entries(providers.data?.network || {}).map(([key, value]) => (
            <React.Fragment key={key}>
              <dt>{key}</dt>
              <dd>{value ? statusLabel(String(value), t) : t("admin.notConfigured")}</dd>
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
  const { t } = useTranslation();
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
        {providerName(provider, t)}{" "}
        <span className="badge">
          {statusLabel(provider.enabled ? "enabled" : "disabled", t)} ·{" "}
          {statusLabel(provider.status, t)}
        </span>
      </summary>
      <p className="muted">
        {provider.id} · {statusLabel(provider.kind, t)} · {provider.version}
        {t("admin.credentials")}{" "}
        {provider.credential_configured
          ? statusLabel("configured", t)
          : t("admin.notConfiguredText")}
      </p>
      <form onSubmit={submit}>
        <Check name="enabled" checked={provider.enabled}>
          {t("admin.enableProvider")}
        </Check>
        <label>
          {t("admin.fieldPriorityLowerWins")}
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
          label={t("admin.providerConfigurationJsonObjectBlankPreservesCurrent")}
          value={provider.config || ""}
        />
        <label>
          {t("admin.replaceCredentialsJsonObjectBlankPreservesStoredCredentials")}
          <input
            type="password"
            name="credentials"
            autoComplete="off"
            placeholder='{"cookie":"…"}'
          />
        </label>
        <p className="small muted">{t("admin.forEHentaiSupplyTheProviderSCookieCredentials")}</p>
        <button disabled={action.isPending}>{t("admin.saveProvider")}</button>
      </form>
      <ConfirmButton
        disabled={action.isPending}
        message={t("admin.clearCredentialsConfirm", { name: provider.name })}
        onConfirm={() =>
          action.mutate({
            path: `/admin/providers/${encoded(provider.id)}`,
            method: "PUT",
            body: { clear_credentials: true },
          })
        }
      >
        {t("admin.clearCredentials")}
      </ConfirmButton>
      <ErrorNotice error={error || action.error} />
      {action.isSuccess && <p role="status">{t("admin.providerUpdated")}</p>}
    </details>
  );
}
function CrawlForm({ providers, workId }: { providers: Provider[]; workId?: string }) {
  const { t } = useTranslation();
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
        !window.confirm(t("admin.enqueueCrawlerWorkForThisSelectedScope"))
      )
        return;
      action.mutate({ path: "/admin/crawl", body }, { onSuccess: setResult });
    } catch (error) {
      setError(error);
    }
  }
  return (
    <section className="panel">
      <h2>{workId ? t("admin.crawlThisWorkImportJson") : t("admin.crawlOrImportLocalJson")}</h2>
      <p className="muted">{t("admin.selectAProviderAndPreviewTheScopeBeforeFetching")}</p>
      <form onSubmit={submit}>
        <div className="form-grid">
          <label>
            {t("admin.provider")}
            <select name="provider" required>
              {providers.map((provider) => (
                <option key={provider.id} value={provider.id}>
                  {providerName(provider, t)} ({provider.id})
                </option>
              ))}
            </select>
          </label>
          {!workId && (
            <>
              <label>
                {t("library.root")}
                <select name="root_id">
                  <option value="">{t("admin.unrestrictedScope")}</option>
                  {roots.data?.map((root) => (
                    <option key={root.id} value={root.id}>
                      {root.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                {t("admin.workIds")}
                <textarea name="work_ids" rows={2} />
              </label>
              <label>
                {t("admin.searchScope")}
                <input name="query" />
              </label>
            </>
          )}
        </div>
        <JSONField
          name="local_json"
          label={t("admin.localJsonMetadataOnlyForLocalJsonLeaveBlank")}
          value=""
          rows={6}
        />
        <p className="small muted">
          {t("admin.normalizedImportFieldsObjectTitleDescriptionTagsArrayNamespace")}
        </p>
        <div className="toolbar">
          <button name="action" value="preview" disabled={action.isPending}>
            {t("admin.previewCount")}
          </button>
          <button name="action" value="enqueue" disabled={action.isPending}>
            {t("admin.enqueueCrawlImport")}
          </button>
        </div>
      </form>
      <ErrorNotice error={error || action.error || roots.error} />
      <Result value={result} job />
    </section>
  );
}
function Plugins() {
  const { t } = useTranslation();
  const providers = useAPI<Providers>(["providers"], "/admin/providers"),
    action = useAction(),
    [error, setError] = useState<unknown>(null);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget,
      data = new FormData(form);
    setError(null);
    try {
      if (!data.has("trusted")) throw new Error(t("admin.explicitTrustConfirmationIsRequired"));
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
        <Trans i18nKey="admin.pluginTrust" components={{ strong: <strong /> }} />
      </p>
      <ErrorNotice error={providers.error || action.error || error} />
      {providers.data?.plugins?.map((plugin) => (
        <PluginEditor key={plugin.id} plugin={plugin} />
      ))}
      <section className="panel">
        <h2>{t("admin.installTrustedPlugin")}</h2>
        <p className="muted">{t("admin.registerAReviewedExecutableAndItsManifest")}</p>
        <form onSubmit={submit}>
          <label>
            {t("admin.serverExecutablePath")}
            <input name="executable" required placeholder="/opt/archika/plugins/provider" />
          </label>
          <JSONField
            name="manifest"
            label={t("admin.pluginManifestJson")}
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
          <Check name="trusted">{t("admin.iReviewedThisExecutableAndTrustItToRun")}</Check>
          <button className="danger" disabled={action.isPending}>
            {t("admin.installTrustedExecutable")}
          </button>
        </form>
      </section>
    </>
  );
}
function PluginEditor({ plugin }: { plugin: Plugin }) {
  const { t } = useTranslation();
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
        {plugin.id} · {plugin.version} · {statusLabel(plugin.enabled ? "enabled" : "disabled", t)}
      </summary>
      <Result value={plugin.manifest} raw />
      <form onSubmit={submit}>
        <Check name="enabled" checked={plugin.enabled}>
          {t("admin.enablePlugin")}
        </Check>
        <JSONField
          name="config"
          label={t("admin.pluginConfigurationJson")}
          value={plugin.config || {}}
        />
        <button disabled={action.isPending}>{t("admin.savePlugin")}</button>
      </form>
      <ConfirmButton
        disabled={action.isPending}
        message={t("admin.uninstall", { id: plugin.id })}
        onConfirm={() =>
          action.mutate({
            path: `/admin/plugins/${encoded(plugin.id)}`,
            method: "DELETE",
            body: { confirm: true },
          })
        }
      >
        {t("admin.uninstallPlugin")}
      </ConfirmButton>
      <ErrorNotice error={error || action.error} />
    </details>
  );
}
function Audit() {
  const { t } = useTranslation();
  const audit = useAPI<AuditRecord[]>(["audit"], "/admin/audit");
  return (
    <section className="panel">
      <h2>{t("admin.recentAuditRecords")}</h2>
      <p className="muted">{t("admin.boundedServerAuditLogCredentialsAreNotDisplayed")}</p>
      <ErrorNotice error={audit.error} />
      {audit.isPending ? <Pending /> : <Result value={audit.data} />}
    </section>
  );
}

export function WorkMetadata({ workId }: { workId: string }) {
  const { t } = useTranslation();
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
      <h2>{t("admin.metadataProvenance")}</h2>
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
                    {detail.origin ? statusLabel(detail.origin, t) : t("admin.unknownSource")}
                    {detail.manual ? t("admin.manualOverride") : ""}
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
        <summary>{t("admin.providerSnapshots")}</summary>
        <Result value={metadata.data?.providers} />
      </details>
      {user.admin && (
        <>
          <details className="panel">
            <summary>{t("admin.editManualFieldsTags")}</summary>
            <p className="small muted">{t("admin.manualOverridesTakePrecedenceUseNullToClearA")}</p>
            <form onSubmit={submit}>
              <JSONField
                name="fields"
                label={t("admin.fieldOverridesJsonObjectStringOrNullValues")}
                value={{}}
              />
              <label>
                {t("admin.resetFieldOverridesCommaOrNewline")}
                <textarea name="reset_fields" rows={2} />
              </label>
              <div className="form-grid">
                <label>
                  {t("admin.addTags")}
                  <textarea name="add_tags" rows={3} />
                </label>
                <label>
                  {t("admin.removeTags")}
                  <textarea name="remove_tags" rows={3} />
                </label>
                <label>
                  {t("admin.resetTagOverrides")}
                  <textarea name="reset_tags" rows={3} />
                </label>
              </div>
              <button disabled={action.isPending}>{t("admin.saveMetadataChanges")}</button>
            </form>
          </details>
          <section>
            <h3>{t("admin.matchCandidates")}</h3>
            {!metadata.data?.candidates?.length && (
              <p className="muted">{t("admin.noCandidatesAwaitingReview")}</p>
            )}
            {metadata.data?.candidates?.map((candidate) => (
              <article className="panel" key={candidate.id}>
                <h4>{candidate.title}</h4>
                <p>
                  {candidate.provider} · {candidate.source_id}
                  {t("admin.confidence")}
                  {candidate.confidence}
                </p>
                {/^https?:\/\//.test(candidate.source_url || "") && (
                  <a href={candidate.source_url} rel="noreferrer" target="_blank">
                    {t("admin.viewSource")}
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
                    {t("admin.approveMatch")}
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
                    {t("admin.rejectMatch")}
                  </button>
                </div>
              </article>
            ))}
          </section>
          <CrawlForm providers={providers.data?.providers || []} workId={workId} />
          <hr />
          <p className="small muted">{t("admin.removeThisWorkFromTheIndexAndCacheOnly")}</p>
          <ConfirmButton
            disabled={action.isPending}
            message={t("admin.removeThisWorkSCatalogEntryAndCachedRepresentations")}
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
            {t("admin.removeIndexCacheText")}
          </ConfirmButton>
        </>
      )}
    </>
  );
}
