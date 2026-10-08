"use client";
import Link from "next/link";
import { useState } from "react";
import { directions, request, Status, validateStatus } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";
import { Badge, Lamp, Notice } from "./shared";

export function Overview() {
  const polling = usePoll(
    async (signal) =>
      (await request<Status[]>("/junctions", undefined, signal)).map(
        validateStatus,
      ),
    "overview",
  );
  const [name, setName] = useState("");
  const [id, setId] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const items = polling.data || [];
  const queue = items.reduce(
    (total, item) =>
      total + Object.values(item.queues).reduce((sum, n) => sum + (n || 0), 0),
    0,
  );
  async function create(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    try {
      const template = items.find((j) => j.junction_id === "A")?.config;
      if (!template)
        throw new Error(
          "Junction A configuration is unavailable. Wait for the backend connection.",
        );
      await request("/junctions", { ...template, id, name });
      setId("");
      setName("");
      setMessage(
        "Junction created. Connect simulated devices to begin reconciliation.",
      );
      polling.refresh();
    } catch (error) {
      setMessage(
        error instanceof Error ? error.message : "Unable to create junction.",
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">NETWORK OVERVIEW</div>
          <h1>Keep the factory moving.</h1>
          <p>
            Live junction state, vehicle demand, and exceptions in one place.
          </p>
        </div>
        <span className={`connection ${polling.error ? "stale" : ""}`}>
          <i />
          {polling.error
            ? "Connection interrupted"
            : polling.updated
              ? "Live · refreshes every second"
              : "Connecting"}
        </span>
      </div>
      {polling.error && (
        <Notice
          message={`${polling.data ? "Showing last received data. " : ""}${polling.error}`}
          retry={polling.refresh}
        />
      )}
      <div className="metrics">
        <div>
          <span>Configured junctions</span>
          <strong>{polling.data ? items.length : "—"}</strong>
        </div>
        <div>
          <span>Vehicles waiting</span>
          <strong>{polling.data ? queue : "—"}</strong>
        </div>
        <div>
          <span>Active emergencies</span>
          <strong>{items.reduce((n, j) => n + j.emergencies.length, 0)}</strong>
        </div>
        <div>
          <span>Junctions needing attention</span>
          <strong>
            {
              items.filter((j) => j.mode === "FAILURE" || j.alerts.length)
                .length
            }
          </strong>
        </div>
      </div>
      <div className="overview-grid">
        {!polling.data && !polling.error && (
          <div className="card empty">Loading junctions…</div>
        )}
        {items.map((item) => (
          <Link
            className="card junction-card"
            href={`/junctions/${encodeURIComponent(item.junction_id)}`}
            key={item.junction_id}
          >
            <div className="card-top">
              <span className="junction-icon">{item.junction_id}</span>
              <Badge value={item.mode} />
            </div>
            <h2>{item.name}</h2>
            <p>
              {item.phase
                ? item.phase.replaceAll("_", " + ")
                : "Awaiting safe phase"}
            </p>
            <div className="direction-summary">
              {directions.map((d) => (
                <div key={d}>
                  <span>{d.slice(0, 1)}</span>
                  <Lamp signal={item.actual_signals[d]} small />
                  <strong>{item.queues[d] ?? "?"}</strong>
                </div>
              ))}
            </div>
            <div className="junction-card-footer">
              <span>Controller · {item.controller_status.toLowerCase()}</span>
              <strong>Open junction ↗</strong>
            </div>
          </Link>
        ))}
      </div>
      <section className="card create-card">
        <div>
          <div className="eyebrow">EXPAND THE NETWORK</div>
          <h2>Add a junction</h2>
          <p>Use Junction A’s configured phases and operating policy.</p>
        </div>
        <form onSubmit={create}>
          <label>
            Junction ID
            <input
              value={id}
              onChange={(e) => setId(e.target.value)}
              placeholder="B"
              required
              maxLength={128}
              pattern="[A-Za-z0-9._-]+"
            />
          </label>
          <label>
            Name
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Junction B"
              required
              maxLength={200}
            />
          </label>
          <button className="button primary" disabled={busy || !items.length}>
            {busy ? "Creating…" : "Create junction"}
          </button>
        </form>
        {message && (
          <p className="form-message" role="status">
            {message}
          </p>
        )}
      </section>
    </>
  );
}
