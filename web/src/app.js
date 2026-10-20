// pg-dump-dashboard frontend.
//
// Vanilla ES2022, no build step beyond Tailwind CSS. All state lives in this
// module's memory: nothing is written to localStorage or sessionStorage, and
// the PostgreSQL password is cleared from the form as soon as a session exists.

const state = {
  meta: null,
  overview: null,
  session: null,
  filter: "",
  busy: false,
  downloadAt: 0,
};

const el = {
  meta: document.getElementById("meta"),
  footerVersion: document.getElementById("footer-version"),
  form: document.getElementById("connect-form"),
  user: document.getElementById("user"),
  password: document.getElementById("password"),
  database: document.getElementById("database"),
  connectButton: document.getElementById("connect-button"),
  disconnectButton: document.getElementById("disconnect-button"),
  sessionBadge: document.getElementById("session-badge"),
  alert: document.getElementById("alert"),
  summary: document.getElementById("summary"),
  summarySubtitle: document.getElementById("summary-subtitle"),
  metrics: document.getElementById("metrics"),
  countsNote: document.getElementById("counts-note"),
  refreshButton: document.getElementById("refresh-button"),
  databasesCard: document.getElementById("databases-card"),
  databasesBody: document.getElementById("databases-body"),
  emptyState: document.getElementById("empty-state"),
  filter: document.getElementById("filter"),
  toast: document.getElementById("download-toast"),
  toastText: document.getElementById("download-toast-text"),
  toastClose: document.getElementById("download-toast-close"),
};

// ---------------------------------------------------------------- API client

class ApiError extends Error {
  constructor(status, code, message) {
    super(message || "request failed");
    this.status = status;
    this.code = code;
  }
}

async function api(path, { method = "GET", body } = {}) {
  const init = { method, credentials: "same-origin", headers: { Accept: "application/json" } };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }

  let response;
  try {
    response = await fetch(path, init);
  } catch (err) {
    throw new ApiError(0, "network_error", "Cannot reach the dashboard server.");
  }

  if (response.status === 204) return null;

  let payload = null;
  const contentType = response.headers.get("content-type") || "";
  if (contentType.includes("application/json")) {
    try {
      payload = await response.json();
    } catch {
      payload = null;
    }
  }

  if (!response.ok) {
    const detail = payload && payload.error ? payload.error : {};
    throw new ApiError(response.status, detail.code || "error", detail.message || response.statusText);
  }
  return payload;
}

// ------------------------------------------------------------------ formatting

const numberFormat = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const exactFormat = new Intl.NumberFormat();

function compact(value) {
  if (value === null || value === undefined) return "—";
  if (typeof value !== "number") return String(value);
  return Math.abs(value) >= 10000 ? numberFormat.format(value) : exactFormat.format(value);
}

function bytes(value) {
  if (value === null || value === undefined) return "—";
  if (value < 0) return "n/a";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size >= 10 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`;
}

function relativeTime(iso) {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const diff = Math.max(0, Date.now() - then);
  const seconds = Math.round(diff / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  return hours < 24 ? `${hours}h ago` : `${Math.round(hours / 24)}d ago`;
}

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (character) => {
    switch (character) {
      case "&":
        return "&amp;";
      case "<":
        return "&lt;";
      case ">":
        return "&gt;";
      case '"':
        return "&quot;";
      default:
        return "&#39;";
    }
  });
}

// --------------------------------------------------------------------- alerts

function showAlert(kind, title, message, hint) {
  const palette = {
    error: "border-rose-200 bg-rose-50 text-rose-900",
    warning: "border-amber-200 bg-amber-50 text-amber-900",
    info: "border-brand-200 bg-brand-50 text-brand-900",
  }[kind] || "border-slate-200 bg-slate-50 text-slate-900";

  el.alert.className = `rounded-xl border px-4 py-3 text-sm shadow-sm ${palette}`;
  el.alert.innerHTML = `
    <p class="font-semibold">${escapeHtml(title)}</p>
    <p class="mt-0.5 break-words">${escapeHtml(message)}</p>
    ${hint ? `<p class="mt-1 text-xs opacity-80">${escapeHtml(hint)}</p>` : ""}
  `;
  el.alert.classList.remove("hidden");
}

function clearAlert() {
  el.alert.classList.add("hidden");
  el.alert.innerHTML = "";
}

// ------------------------------------------------------------------- rendering

function renderMeta() {
  if (!state.meta) return;
  const meta = state.meta;
  el.meta.innerHTML = `
    <span class="pill-muted" title="PostgreSQL server this dashboard is configured for">
      <span class="font-mono">${escapeHtml(meta.endpoint)}</span>
    </span>
    <span class="pill-muted" title="Where psql and pg_dump run">
      execution: <span class="font-mono">${escapeHtml(meta.executionContext)}</span>
    </span>
  `;
  el.footerVersion.textContent = `version ${meta.version}`;
  if (meta.defaultDatabase && !el.database.value) {
    el.database.placeholder = meta.defaultDatabase;
  }
}

function renderSession() {
  if (state.session) {
    el.sessionBadge.className = "pill-ok";
    el.sessionBadge.textContent = `connected as ${state.session.user}`;
    el.disconnectButton.classList.remove("hidden");
  } else {
    el.sessionBadge.className = "pill-muted";
    el.sessionBadge.textContent = "not connected";
    el.disconnectButton.classList.add("hidden");
  }
}

function renderSummary() {
  const overview = state.overview;
  if (!overview) {
    el.summary.classList.add("hidden");
    el.databasesCard.classList.add("hidden");
    el.emptyState.classList.remove("hidden");
    return;
  }

  el.emptyState.classList.add("hidden");
  el.summary.classList.remove("hidden");
  el.databasesCard.classList.remove("hidden");

  const totals = overview.totals;
  const metrics = [
    { label: "Databases", value: compact(totals.databaseCount), hint: `${totals.reachable} reachable` },
    { label: "Schemas", value: compact(totals.schemas), hint: "exact" },
    { label: "Tables", value: compact(totals.tables), hint: "exact" },
    { label: "Views", value: compact(totals.views), hint: "exact" },
    {
      label: "Est. rows",
      value: `≈ ${compact(totals.rowEstimate)}`,
      hint: "pg_class.reltuples",
    },
    { label: "Total size", value: bytes(totals.sizeBytes), hint: "on disk" },
  ];

  el.metrics.innerHTML = metrics
    .map(
      (metric) => `
      <div class="card px-4 py-3">
        <p class="metric-label">${escapeHtml(metric.label)}</p>
        <p class="metric-value">${escapeHtml(metric.value)}</p>
        <p class="mt-0.5 text-xs text-slate-500">${escapeHtml(metric.hint)}</p>
      </div>`,
    )
    .join("");

  const unreachable = totals.unreachable;
  el.summarySubtitle.textContent = [
    `${overview.connectedAs} @ ${overview.endpoint}`,
    `execution ${overview.executionContext}`,
    `collected ${relativeTime(overview.collectedAt)}`,
    unreachable > 0 ? `${unreachable} database(s) not inspectable` : null,
  ]
    .filter(Boolean)
    .join(" · ");

  el.countsNote.textContent =
    "Object counts are catalog queries (exact). Row counts are planner estimates, never full-table scans; " +
    "tables that were never analysed report 0.";
}

function statusPill(db) {
  switch (db.status) {
    case "connected":
      return '<span class="pill-ok"><span class="size-1.5 rounded-full bg-emerald-500"></span>connected</span>';
    case "restricted":
      return '<span class="pill-warn">restricted</span>';
    default:
      return '<span class="pill-error">error</span>';
  }
}

function rowCountCell(db) {
  if (db.rowCountSource === "estimate") {
    return `<span title="Estimated from pg_class.reltuples">≈ ${escapeHtml(compact(db.rowEstimate))}</span>`;
  }
  return `<span class="text-slate-400" title="No statistics available">—</span>`;
}

function dumpActions(db) {
  if (!db.eligible) {
    const reason = db.error || "not available";
    return `<span class="text-xs text-slate-400" title="${escapeHtml(reason)}">unavailable</span>`;
  }
  return `
    <div class="flex flex-wrap items-center gap-1.5">
      <button
        type="button"
        class="btn-primary px-2.5 py-1 text-xs"
        data-dump="${escapeHtml(db.name)}"
        data-format="custom"
        title="Compressed custom archive for pg_restore"
      >
        .dump
      </button>
      <button
        type="button"
        class="btn-secondary px-2.5 py-1 text-xs"
        data-dump="${escapeHtml(db.name)}"
        data-format="plain"
        title="Plain SQL text for psql"
      >
        .sql
      </button>
    </div>`;
}

function detailsRow(db) {
  const counts = db.objectCounts;
  const tables = (db.largestTables || [])
    .map(
      (table) => `
        <tr class="border-t border-slate-100">
          <td class="py-1.5 pr-4 font-mono text-xs text-slate-700">${escapeHtml(table.schema)}.${escapeHtml(table.name)}</td>
          <td class="py-1.5 pr-4 text-right font-mono text-xs text-slate-600">≈ ${escapeHtml(compact(table.rows))}</td>
          <td class="py-1.5 text-right font-mono text-xs text-slate-600">${escapeHtml(bytes(table.sizeBytes))}</td>
        </tr>`,
    )
    .join("");

  const facts = [
    ["Owner", db.owner || "—"],
    ["Encoding", db.encoding || "—"],
    ["Collate / CType", `${db.collate || "—"} / ${db.ctype || "—"}`],
    ["Connections", exactFormat.format(db.connections || 0)],
    [
      "Objects",
      `${compact(counts.tables)} tables · ${compact(counts.views)} views · ${compact(counts.indexes)} indexes · ` +
        `${compact(counts.sequences)} sequences · ${compact(counts.functions)} functions`,
    ],
  ];

  return `
    <div class="grid gap-4 border-t border-slate-100 bg-slate-50/60 px-5 py-4 lg:grid-cols-2">
      <div>
        <p class="metric-label mb-2">Catalog facts</p>
        <dl class="grid grid-cols-1 gap-1 text-sm">
          ${facts
            .map(
              ([key, value]) => `
              <div class="flex justify-between gap-4 border-b border-slate-100 py-1 last:border-0">
                <dt class="text-slate-500">${escapeHtml(key)}</dt>
                <dd class="text-right font-mono text-xs text-slate-700">${escapeHtml(value)}</dd>
              </div>`,
            )
            .join("")}
        </dl>
        ${db.error ? `<p class="mt-2 text-xs text-rose-600">${escapeHtml(db.error)}</p>` : ""}
      </div>
      <div>
        <p class="metric-label mb-2">Largest relations</p>
        ${
          tables
            ? `<table class="w-full text-left">
                 <thead>
                   <tr class="text-xs text-slate-500">
                     <th class="pb-1 font-medium">relation</th>
                     <th class="pb-1 text-right font-medium">est. rows</th>
                     <th class="pb-1 text-right font-medium">size</th>
                   </tr>
                 </thead>
                 <tbody>${tables}</tbody>
               </table>`
            : '<p class="text-xs text-slate-500">No user relations, or statistics are not readable.</p>'
        }
      </div>
    </div>`;
}

function renderDatabases() {
  const overview = state.overview;
  if (!overview) {
    el.databasesBody.innerHTML = "";
    return;
  }

  const needle = state.filter.trim().toLowerCase();
  const databases = overview.databases.filter((db) => !needle || db.name.toLowerCase().includes(needle));

  if (databases.length === 0) {
    el.databasesBody.innerHTML = `
      <div class="px-5 py-10 text-center text-sm text-slate-500">
        ${
          overview.databases.length === 0
            ? "No databases are visible to this role."
            : `No database matches “${escapeHtml(state.filter)}”.`
        }
      </div>`;
    return;
  }

  const rows = databases
    .map((db) => {
      const counts = db.objectCounts || {};
      return `
        <tr class="border-t border-slate-100 align-middle hover:bg-slate-50/60">
          <td class="px-5 py-3">
            <div class="flex items-center gap-2">
              <button
                type="button"
                class="font-mono text-sm font-medium text-slate-900 underline decoration-slate-300 decoration-dotted underline-offset-4 hover:text-brand-700"
                data-name="${escapeHtml(db.name)}"
                aria-expanded="false"
                title="Show catalog details"
              >
                ${escapeHtml(db.name)}
              </button>
              ${statusPill(db)}
            </div>
            <p class="mt-0.5 text-xs text-slate-500">${escapeHtml(db.owner || "")}</p>
          </td>
          <td class="hidden px-3 py-3 text-right font-mono text-xs text-slate-600 md:table-cell">${escapeHtml(bytes(db.sizeBytes))}</td>
          <td class="hidden px-3 py-3 text-right font-mono text-xs text-slate-600 lg:table-cell">${escapeHtml(compact(counts.schemas || 0))}</td>
          <td class="hidden px-3 py-3 text-right font-mono text-xs text-slate-600 sm:table-cell">${escapeHtml(compact(counts.tables || 0))}</td>
          <td class="hidden px-3 py-3 text-right font-mono text-xs text-slate-600 lg:table-cell">${escapeHtml(compact(counts.views || 0))}</td>
          <td class="hidden px-3 py-3 text-right font-mono text-xs text-slate-600 xl:table-cell">${escapeHtml(compact(counts.indexes || 0))}</td>
          <td class="px-3 py-3 text-right font-mono text-xs text-slate-600">${rowCountCell(db)}</td>
          <td class="px-5 py-3 text-right">${dumpActions(db)}</td>
        </tr>
        <tr class="hidden" data-details="${escapeHtml(db.name)}">
          <td colspan="8" class="p-0">${detailsRow(db)}</td>
        </tr>`;
    })
    .join("");

  el.databasesBody.innerHTML = `
    <table class="w-full border-collapse text-left">
      <thead class="table-head">
        <tr>
          <th class="px-5 py-2.5 font-semibold">database</th>
          <th class="hidden px-3 py-2.5 text-right font-semibold md:table-cell">size</th>
          <th class="hidden px-3 py-2.5 text-right font-semibold lg:table-cell">schemas</th>
          <th class="hidden px-3 py-2.5 text-right font-semibold sm:table-cell">tables</th>
          <th class="hidden px-3 py-2.5 text-right font-semibold lg:table-cell">views</th>
          <th class="hidden px-3 py-2.5 text-right font-semibold xl:table-cell">indexes</th>
          <th class="px-3 py-2.5 text-right font-semibold">est. rows</th>
          <th class="px-5 py-2.5 text-right font-semibold">dump</th>
        </tr>
      </thead>
      <tbody>${rows}</tbody>
    </table>
    <div class="border-t border-slate-100 px-5 py-2 text-xs text-slate-500">
      Showing ${databases.length} of ${overview.databases.length} databases. Click a database name to expand details.
    </div>`;
}

function render() {
  renderSession();
  renderSummary();
  renderDatabases();
}

// ------------------------------------------------------------------ operations

function setBusy(busy, button, label) {
  state.busy = busy;
  const target = button || el.connectButton;
  const spinner = target.querySelector('[data-role="spinner"]');
  const text = target.querySelector('[data-role="label"]');
  target.disabled = busy;
  if (spinner) spinner.classList.toggle("hidden", !busy);
  if (text && label) text.textContent = label;
}

function applyServerError(err, context) {
  if (err instanceof ApiError && err.code === "no_session") {
    state.session = null;
    state.overview = null;
    render();
    showAlert("warning", "Session expired", "Your dashboard session ended. Connect again to continue.");
    return;
  }
  const hint =
    context ||
    (err.code === "client_missing"
      ? "Install the PostgreSQL client tools in the execution context, or check -psql-path / -pg-dump-path."
      : "");
  showAlert("error", "Request failed", err.message || "Unexpected error", hint);
}

async function loadMeta() {
  try {
    state.meta = await api("/api/v1/meta");
    renderMeta();
  } catch {
    el.meta.innerHTML = '<span class="pill-error">deployment info unavailable</span>';
  }
}

async function connect(event) {
  event.preventDefault();
  clearAlert();

  const user = el.user.value.trim();
  const password = el.password.value;
  const database = el.database.value.trim();

  if (!user) {
    showAlert("error", "User required", "Enter the PostgreSQL user to connect with.");
    el.user.focus();
    return;
  }

  setBusy(true, el.connectButton, "Connecting…");
  try {
    const payload = await api("/api/v1/connect", {
      method: "POST",
      body: { user, password, database: database || undefined },
    });
    state.session = payload.session;
    state.overview = payload.overview;
    el.password.value = "";
    render();
    showAlert(
      "info",
      "Connected",
      `${payload.overview.databases.length} database(s) visible as ${payload.session.user}.`,
    );
  } catch (err) {
    applyServerError(err);
  } finally {
    setBusy(false, el.connectButton, "Connect");
  }
}

async function refresh() {
  clearAlert();
  setBusy(true, el.refreshButton, "Refreshing…");
  try {
    const payload = await api("/api/v1/overview/refresh", { method: "POST" });
    state.session = payload.session;
    state.overview = payload.overview;
    render();
  } catch (err) {
    applyServerError(err, "Metadata refresh uses the credentials from your current session.");
  } finally {
    setBusy(false, el.refreshButton, "Refresh metadata");
  }
}

async function disconnect() {
  try {
    await api("/api/v1/session", { method: "DELETE" });
  } catch {
    // The session is gone either way; fall through and clear local state.
  }
  state.session = null;
  state.overview = null;
  el.password.value = "";
  clearAlert();
  render();
  showAlert("info", "Disconnected", "The stored credentials were discarded.");
}

function startDownload(name, format) {
  const url = `/api/v1/databases/${encodeURIComponent(name)}/dump?format=${encodeURIComponent(format)}`;

  // Native browser download: the response is streamed straight to disk, so the
  // page never buffers a multi-gigabyte archive and the user can cancel it from
  // the browser's download manager.
  const link = document.createElement("a");
  link.href = url;
  link.rel = "noopener";
  link.download = "";
  document.body.appendChild(link);
  link.click();
  link.remove();

  state.downloadAt = Date.now();
  el.toastText.textContent = `${name} (${format === "plain" ? ".sql" : ".dump"}) — check your browser downloads`;
  el.toast.classList.remove("opacity-0");
  el.toast.classList.add("opacity-100");
  window.setTimeout(hideToast, 8000);
}

function hideToast() {
  el.toast.classList.remove("opacity-100");
  el.toast.classList.add("opacity-0");
}

// --------------------------------------------------------------------- wiring

el.form.addEventListener("submit", connect);
el.disconnectButton.addEventListener("click", disconnect);
el.refreshButton.addEventListener("click", refresh);
el.toastClose.addEventListener("click", hideToast);
el.filter.addEventListener("input", (event) => {
  state.filter = event.target.value;
  renderDatabases();
});

el.databasesBody.addEventListener("click", (event) => {
  const button = event.target.closest("[data-dump]");
  if (button) {
    startDownload(button.dataset.dump, button.dataset.format);
    return;
  }

  const nameCell = event.target.closest("[data-name]");
  if (nameCell) {
    toggleDetails(nameCell.dataset.name);
  }
});

function toggleDetails(name) {
  const row = el.databasesBody.querySelector(`[data-details="${CSS.escape(name)}"]`);
  if (!row) return;
  const hidden = row.classList.toggle("hidden");
  const trigger = el.databasesBody.querySelector(`[data-name="${CSS.escape(name)}"]`);
  if (trigger) trigger.setAttribute("aria-expanded", String(!hidden));
}

// Restore an existing session (for example after a page reload) without
// requiring the password again.
async function bootstrap() {
  await loadMeta();
  try {
    const payload = await api("/api/v1/session");
    if (payload && payload.overview) {
      state.session = payload.session;
      state.overview = payload.overview;
      render();
      return;
    }
  } catch {
    // No session yet: expected on first load.
  }
  render();
}

bootstrap();
