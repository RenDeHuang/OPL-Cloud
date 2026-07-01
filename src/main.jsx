import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Copy,
  CreditCard,
  Database,
  HardDrive,
  Link as LinkIcon,
  Play,
  RefreshCw,
  RotateCw,
  Server,
  ShieldCheck,
  Square,
  Trash2
} from "lucide-react";
import "./styles.css";

const accountId = "pi-alpha";

async function api(path, body) {
  const response = await fetch(path, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body)
  });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "request_failed");
  return payload;
}

function money(value) {
  return `¥${Number(value || 0).toFixed(2)}`;
}

function statusLabel(workspace) {
  if (!workspace) return "No workspace";
  const labels = {
    running: "Running",
    stopped_server_disk_retained: "Stopped, disk retained",
    server_destroyed_disk_retained: "Server destroyed, disk retained",
    destroyed: "Destroyed",
    failed: "Failed"
  };
  return labels[workspace.state] || workspace.state;
}

function App() {
  const [state, setState] = useState(null);
  const [readiness, setReadiness] = useState(null);
  const [productionReadiness, setProductionReadiness] = useState(null);
  const [selectedId, setSelectedId] = useState("");
  const [error, setError] = useState("");

  async function refresh() {
    const [stateResponse, readinessResponse, productionReadinessResponse] = await Promise.all([
      fetch(`/api/state?accountId=${accountId}`),
      fetch("/api/runtime/readiness"),
      fetch("/api/production/readiness")
    ]);
    const next = await stateResponse.json();
    const nextReadiness = await readinessResponse.json();
    const nextProductionReadiness = await productionReadinessResponse.json();
    setState(next);
    setReadiness(nextReadiness);
    setProductionReadiness(nextProductionReadiness);
    setSelectedId((current) => current || next.workspaces[0]?.id || "");
  }

  async function run(action) {
    try {
      setError("");
      await action();
      await refresh();
    } catch (err) {
      setError(err.message);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  const selected = useMemo(() => state?.workspaces.find((item) => item.id === selectedId) || state?.workspaces[0], [state, selectedId]);

  if (!state) return <div className="loading">Loading OPL Console...</div>;

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brandIcon">OPL</div>
          <div>
            <strong>OPL Console</strong>
            <span>OPL Cloud management</span>
          </div>
        </div>
        <nav>
          <a href="#workspaces">Workspaces</a>
          <a href="#create">Create</a>
          <a href="#access">URL & Token</a>
          <a href="#resources">Server & Disk</a>
          <a href="#billing">Billing</a>
          <a href="#audit">Audit</a>
        </nav>
        <div className="sidebarNote">1 Workspace = 1 server + 1 Docker + 1 cloud disk + 1 URL</div>
      </aside>

      <main className="main">
        <header className="topbar">
          <div>
            <p className="eyebrow">OPL Cloud</p>
            <h1>Workspace distribution for PI labs</h1>
          </div>
          <button onClick={() => run(() => api("/api/accounts/credit", { accountId, amount: 200, reason: "owner_credit" }))}>
            <CreditCard size={16} /> Grant ¥200
          </button>
        </header>

        {error && <div className="error">{error}</div>}

        {readiness && (
          <section className={`readiness ${readiness.ready ? "ready" : "blocked"}`}>
            <div>
              <strong>{readiness.provider}</strong>
              <span>{readiness.ready ? "Ready for runtime execution" : "Runtime setup incomplete"}</span>
            </div>
            {!readiness.ready && (
              <code>
                {[...readiness.missingEnv, ...readiness.missingTools.map((tool) => `tool:${tool}`)].join(" · ")}
              </code>
            )}
          </section>
        )}

        {productionReadiness && (
          <section className={`readiness ${productionReadiness.ready ? "ready" : "blocked"}`}>
            <div>
              <strong>production</strong>
              <span>{productionReadiness.ready ? "Ready for production launch" : "Production launch blockers"}</span>
            </div>
            {!productionReadiness.ready && (
              <code>
                {[
                  ...productionReadiness.failedChecks,
                  ...productionReadiness.missingEnv,
                  ...productionReadiness.missingTools.map((tool) => `tool:${tool}`)
                ].join(" · ")}
              </code>
            )}
          </section>
        )}

        <section className="metrics">
          <Metric label="Account" value={state.account.id} />
          <Metric label="Balance" value={money(state.account.balance)} />
          <Metric label="Frozen" value={money(state.account.frozen)} />
          <Metric label="Workspaces" value={state.workspaces.length} />
        </section>

        <section id="create" className="band">
          <div className="sectionHeader">
            <div>
              <h2>Create OPL Workspace</h2>
              <p>Creates one server, one cloud disk, one Docker runtime, and one URL.</p>
            </div>
          </div>
          <div className="planGrid">
            {state.packages.map((plan) => (
              <article className="plan" key={plan.id}>
                <span>{plan.id === "basic" ? "Default" : "Larger lab"}</span>
                <h3>{plan.name}</h3>
                <p>{plan.server} server + {plan.diskGb}GB cloud disk</p>
                <button
                  className="primary"
                  onClick={() => run(() => api("/api/workspaces", {
                    accountId,
                    workspaceName: plan.id === "basic" ? "Grant Lab" : "Protein Lab",
                    packageId: plan.id
                  }))}
                >
                  <Play size={16} /> Create {plan.id}
                </button>
              </article>
            ))}
          </div>
        </section>

        <section id="workspaces" className="band">
          <div className="sectionHeader">
            <div>
              <h2>Workspaces</h2>
              <p>Each URL can be copied and shared with members. Members do not log in.</p>
            </div>
          </div>
          <div className="workspaceList">
            {state.workspaces.length === 0 && <div className="empty">No OPL Workspace yet. Grant balance, then create Basic or Pro.</div>}
            {state.workspaces.map((workspace) => (
              <button
                className={`workspaceRow ${workspace.id === selected?.id ? "active" : ""}`}
                key={workspace.id}
                onClick={() => setSelectedId(workspace.id)}
              >
                <span>{workspace.name}</span>
                <strong>{statusLabel(workspace)}</strong>
              </button>
            ))}
          </div>
        </section>

        <section id="access" className="band">
          <div className="sectionHeader">
            <div>
              <h2>Workspace URL</h2>
              <p>Permanent token until owner resets or deletes it.</p>
            </div>
            <LinkIcon />
          </div>
          {selected ? (
            <div className="urlBox">
              <code>{selected.url}</code>
              <button onClick={() => navigator.clipboard?.writeText(selected.url)}><Copy size={16} /> Copy</button>
              <button onClick={() => run(() => api("/api/workspaces/reset-token", { accountId, workspaceId: selected.id }))}><RefreshCw size={16} /> Reset token</button>
              <button className="danger" onClick={() => run(() => api("/api/workspaces/delete-token", { accountId, workspaceId: selected.id }))}><Trash2 size={16} /> Delete token</button>
            </div>
          ) : <div className="empty">Create a Workspace to get a URL.</div>}
        </section>

        <section id="resources" className="band">
          <div className="sectionHeader">
            <div>
              <h2>Server & Disk</h2>
              <p>Server lifecycle is separate from cloud disk lifecycle.</p>
            </div>
            <Server />
          </div>
          {selected ? (
            <>
              <div className="resourceGrid">
                <ResourceCard icon={<Server />} title="Server" value={`${selected.server.spec} / ${selected.server.status}`} billing={selected.server.billingStatus} />
                <ResourceCard icon={<HardDrive />} title="Cloud disk" value={`${selected.disk.sizeGb}GB / ${selected.disk.status}`} billing={selected.disk.billingStatus} />
                <ResourceCard icon={<Database />} title="Docker" value={selected.docker.image} billing={selected.docker.status} />
                <ResourceCard icon={<ShieldCheck />} title="Access" value={selected.access.tokenStatus} billing="No login required" />
              </div>
              <div className="actions">
                <button onClick={() => run(() => api("/api/workspaces/stop-server", { accountId, workspaceId: selected.id, confirm: true }))}><Square size={16} /> Stop server</button>
                <button onClick={() => run(() => api("/api/workspaces/restart-server", { accountId, workspaceId: selected.id }))}><RotateCw size={16} /> Restart</button>
                <button className="danger" onClick={() => run(() => api("/api/workspaces/destroy-server", { accountId, workspaceId: selected.id, confirm: true }))}><Trash2 size={16} /> Destroy server</button>
                <button className="danger strong" onClick={() => run(() => api("/api/workspaces/destroy-disk", { accountId, workspaceId: selected.id, confirmDataLoss: true }))}><Trash2 size={16} /> Destroy disk</button>
                <button onClick={() => run(() => api("/api/billing/settle", {
                  accountId,
                  workspaceId: selected.id,
                  hours: 1,
                  sourceEventId: `console_meter_tick_${Date.now()}`
                }))}><CreditCard size={16} /> Settle 1h</button>
              </div>
            </>
          ) : <div className="empty">No resource binding yet.</div>}
        </section>

        <section id="billing" className="band">
          <div className="sectionHeader">
            <div>
              <h2>Billing Ledger</h2>
              <p>Hourly server billing, storage billing, and 7-day storage hold.</p>
            </div>
          </div>
          <EventList events={state.billingLedger} />
        </section>

        <section id="audit" className="band">
          <div className="sectionHeader">
            <div>
              <h2>Audit Receipts</h2>
              <p>Lifecycle receipts for workspace operations.</p>
            </div>
          </div>
          <EventList events={state.audit} />
        </section>
      </main>
    </div>
  );
}

function Metric({ label, value }) {
  return <div className="metric"><span>{label}</span><strong>{value}</strong></div>;
}

function ResourceCard({ icon, title, value, billing }) {
  return (
    <article className="resourceCard">
      <div>{icon}<span>{title}</span></div>
      <strong>{value}</strong>
      <small>{billing}</small>
    </article>
  );
}

function EventList({ events }) {
  if (!events.length) return <div className="empty">No events yet.</div>;
  return (
    <div className="eventList">
      {events.slice().reverse().map((event) => (
        <article className="event" key={event.id}>
          <strong>{event.type}</strong>
          <span>{event.workspaceId}</span>
          <code>{event.sourceEventId}</code>
          <em>{event.amount !== undefined ? money(event.amount) : event.createdAt}</em>
        </article>
      ))}
    </div>
  );
}

createRoot(document.getElementById("root")).render(<App />);
