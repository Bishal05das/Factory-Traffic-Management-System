"use client";
import Link from "next/link";
import { useState } from "react";
import {
  Activity,
  clock,
  Direction,
  directions,
  request,
  Status,
  title,
  validateStatus,
} from "@/lib/api";
import { usePoll } from "@/lib/use-poll";
import { Intersection } from "./intersection";
import { Badge, Lamp, Notice, SectionHeading } from "./shared";
import { TrafficSimulation } from "./traffic-simulation";

export function JunctionDashboard({ junctionId }: { junctionId: string }) {
  const path = `/junctions/${encodeURIComponent(junctionId)}`;
  const polling = usePoll(async (signal) => {
    const [status, history] = await Promise.all([
      request<Status>(`${path}/status`, undefined, signal),
      request<Activity[]>(`${path}/history?limit=30`, undefined, signal),
    ]);
    return {
      status: validateStatus(status),
      history: Array.isArray(history) ? history : [],
    };
  }, junctionId);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [actionError, setActionError] = useState("");
  const [deviceType, setDeviceType] = useState("SIGNAL_CONTROLLER");
  const [deviceDirection, setDeviceDirection] = useState<Direction>("NORTH");
  const [deviceStatus, setDeviceStatus] = useState("OFFLINE");
  const status = polling.data?.status;
  async function action(
    label: string,
    target: string,
    body: unknown,
  ): Promise<boolean> {
    setBusy(true);
    setMessage("");
    setActionError("");
    try {
      const result = await request<{ outcome?: string }>(target, body);
      setMessage(`${label}: ${title(result.outcome || "accepted")}.`);
      polling.refresh();
      return true;
    } catch (cause) {
      setActionError(
        cause instanceof Error ? cause.message : "Operation failed.",
      );
      return false;
    } finally {
      setBusy(false);
    }
  }
  async function acknowledgeAll() {
    setBusy(true);
    setActionError("");
    setMessage("");
    try {
      const fresh = await request<Status>(`${path}/status`);
      const commands =
        fresh.pending_batch?.commands.filter((c) => c.status === "PENDING") ||
        [];
      if (!commands.length)
        throw new Error("There are no pending controller commands.");
      for (const command of commands)
        await request("/controller-events", {
          command_id: command.command_id,
          junction_id: junctionId,
          status: "ACK",
          actual_state: command.requested_state,
          timestamp: new Date().toISOString(),
        });
      setMessage(
        "Controller batch acknowledged. Backend confirmation updated.",
      );
      polling.refresh();
    } catch (cause) {
      setActionError(
        cause instanceof Error ? cause.message : "Acknowledgement failed.",
      );
      polling.refresh();
    } finally {
      setBusy(false);
    }
  }
  const stale = !!polling.error;
  const disabled = busy || stale;
  const pending =
    status?.pending_batch?.commands.filter((c) => c.status === "PENDING") || [];
  const healthy =
    status?.devices.length === 9 &&
    status.devices.every((d) => d.status === "ONLINE");
  const seconds = status?.transition_deadline
    ? Math.max(
        0,
        Math.ceil((Date.parse(status.transition_deadline) - Date.now()) / 1000),
      )
    : null;
  return (
    <>
      <Link className="back-link" href="/">
        ← Network overview
      </Link>
      <div className="page-heading">
        <div>
          <div className="eyebrow">JUNCTION CONTROL</div>
          <h1>{status?.name || `Junction ${junctionId}`}</h1>
          <p>
            Monitor physical signals, manage traffic intent, and simulate road
            events.
          </p>
        </div>
        <span className={`connection ${stale ? "stale" : ""}`}>
          <i />
          {stale
            ? "Stale data"
            : polling.updated
              ? `Updated ${clock(polling.updated)}`
              : "Connecting"}
        </span>
      </div>
      {polling.error && (
        <Notice
          message={`${status ? "Showing last received data. " : ""}${polling.error}`}
          retry={polling.refresh}
        />
      )}
      {actionError && <Notice message={actionError} />}
      {message && (
        <div className="success-message" role="status">
          {message}
        </div>
      )}
      {!status && !polling.error && (
        <section className="card empty">Loading junction state…</section>
      )}
      {status && (
        <>
          <div className="metrics detail-metrics">
            <div>
              <span>Control mode</span>
              <strong className="metric-label">
                <Badge value={status.mode || "UNKNOWN"} />
              </strong>
            </div>
            <div>
              <span>Current phase</span>
              <strong className="metric-label">
                {status.phase
                  ? status.phase.replaceAll("_", " + ")
                  : "All red / unverified"}
              </strong>
            </div>
            <div>
              <span>Vehicles waiting</span>
              <strong>{status.vehicles.length}</strong>
            </div>
            <div>
              <span>Controller</span>
              <strong className="metric-label">
                <Badge value={status.controller_status || "UNKNOWN"} />
              </strong>
            </div>
          </div>
          {!!status.emergencies.length && (
            <div className="emergency-banner">
              <span className="alert-symbol">!</span>
              <div>
                <strong>Emergency priority active</strong>
                <p>
                  {status.emergencies
                    .map(
                      (v) =>
                        `${v.vehicle_id} approaching from ${title(v.direction)}`,
                    )
                    .join(" · ")}
                  . {title(status.stage)} — safe clearance still applies.
                </p>
              </div>
            </div>
          )}
          {(status.mode === "FAILURE" || status.alerts.length > 0) && (
            <div className="failure-banner">
              <strong>
                {status.stage === "RECOVERING"
                  ? "Physical reconciliation required"
                  : "Junction needs attention"}
              </strong>
              <p>
                {status.alerts.length
                  ? status.alerts
                      .map((a) => a.message.replaceAll("_", " "))
                      .join(" · ")
                  : "Confirm device health and a fresh all-red batch before traffic can resume."}
              </p>
            </div>
          )}
          <div className="dashboard-grid">
            <Intersection status={status} />
            <div className="control-column">
              <section className="card">
                <SectionHeading
                  eyebrow="ADMINISTRATOR CONTROLS"
                  title="Manual traffic intent"
                />
                <p className="card-description">
                  Request a compatible phase. The backend completes yellow and
                  all-red clearance before granting green.
                </p>
                <div className="manual-buttons">
                  {directions.map((d) => (
                    <button
                      className="button secondary"
                      key={d}
                      disabled={
                        disabled || !healthy || status.mode === "FAILURE"
                      }
                      onClick={() =>
                        void action(
                          `${title(d)} manual request`,
                          `${path}/commands`,
                          { command: "MANUAL_GREEN_REQUEST", direction: d },
                        )
                      }
                    >
                      {title(d)} ↗
                    </button>
                  ))}
                </div>
                {status.manual && (
                  <div className="manual-note">
                    Manual request:{" "}
                    <strong>{title(status.manual.direction)}</strong>
                    <span>
                      Expires {clock(status.manual.expires_at)}
                      {status.mode === "EMERGENCY"
                        ? " · Deferred by emergency"
                        : ""}
                    </span>
                  </div>
                )}
                <button
                  className="button primary full-width"
                  disabled={disabled || status.mode === "FAILURE" || !healthy}
                  onClick={() =>
                    void action("Return to automatic", `${path}/commands`, {
                      command: "RETURN_TO_AUTOMATIC",
                    })
                  }
                >
                  Return to automatic
                </button>
              </section>
              <section className="card">
                <SectionHeading
                  eyebrow="TRANSITION STATUS"
                  title="Controller confirmation"
                >
                  <span className="count-badge">{pending.length}</span>
                </SectionHeading>
                <div className="transition-status">
                  <span>{title(status.stage)}</span>
                  <strong>{seconds === null ? "—" : `${seconds}s`}</strong>
                </div>
                <p className="subtle">
                  {pending.length
                    ? "Awaiting physical responses. Missing acknowledgements cause a safe stop."
                    : status.stage === "FAILURE_STOP"
                      ? "Traffic remains stopped until explicit recovery."
                      : "No controller commands awaiting confirmation."}
                </p>
                {pending.map((command) => (
                  <div className="command-row" key={command.command_id}>
                    <Lamp signal={command.requested_state} small />
                    <span>
                      {title(command.direction)}
                      <small>{command.command_id.slice(0, 8)}</small>
                    </span>
                    <strong>{command.requested_state}</strong>
                    <button
                      className="button small secondary"
                      disabled={
                        disabled || status.controller_status !== "ONLINE"
                      }
                      onClick={() =>
                        void action(
                          "Controller acknowledgement",
                          "/controller-events",
                          {
                            command_id: command.command_id,
                            junction_id: junctionId,
                            status: "ACK",
                            actual_state: command.requested_state,
                            timestamp: new Date().toISOString(),
                          },
                        )
                      }
                    >
                      ACK
                    </button>
                    <button
                      className="button small danger-outline"
                      disabled={disabled}
                      onClick={() =>
                        void action(
                          "Controller failure report",
                          "/controller-events",
                          {
                            command_id: command.command_id,
                            junction_id: junctionId,
                            status: "NACK",
                            actual_state: "UNKNOWN",
                            timestamp: new Date().toISOString(),
                          },
                        )
                      }
                    >
                      NACK
                    </button>
                  </div>
                ))}
                {!!pending.length && (
                  <button
                    className="button secondary full-width"
                    disabled={disabled || status.controller_status !== "ONLINE"}
                    onClick={() => void acknowledgeAll()}
                  >
                    Acknowledge entire batch
                  </button>
                )}
                {status.stage === "FAILURE_STOP" && (
                  <button
                    className="button primary full-width"
                    disabled={disabled || !healthy}
                    onClick={() =>
                      void action("Recovery request", `${path}/commands`, {
                        command: "RECOVER",
                      })
                    }
                  >
                    Start safe recovery
                  </button>
                )}
              </section>
            </div>
          </div>
          <div className="bottom-grid">
            <TrafficSimulation
              status={status}
              action={action}
              busy={disabled}
            />
            <section className="card activity-card">
              <SectionHeading eyebrow="AUDIT HISTORY" title="Recent activity">
                <span className="subtle">Latest 30 events</span>
              </SectionHeading>
              <div className="activity-list">
                {!polling.data?.history.length && (
                  <div className="empty compact">No activity yet.</div>
                )}
                {polling.data?.history.map((item) => (
                  <div className="activity-item" key={item.id}>
                    <span
                      className={`activity-dot ${/FAILURE|TIMEOUT|REJECTED|MISMATCH/.test(item.event_type) ? "dot-warning" : ""}`}
                    />
                    <div>
                      <strong>{title(item.event_type)}</strong>
                      <p>
                        {Object.entries(item.details || {})
                          .slice(0, 3)
                          .map(
                            ([key, value]) =>
                              `${title(key)}: ${typeof value === "object" ? JSON.stringify(value) : String(value)}`,
                          )
                          .join(" · ") || "Junction state recorded"}
                      </p>
                    </div>
                    <time>{clock(item.timestamp)}</time>
                  </div>
                ))}
              </div>
            </section>
          </div>
          <section className="card device-card">
            <SectionHeading
              eyebrow="DEVICE HEALTH & FAILURE SIMULATION"
              title="Junction devices"
            >
              <span className="subtle">
                ONLINE reports do not confirm lamps
              </span>
            </SectionHeading>
            <div className="device-list">
              {status.devices.map((device) => (
                <div key={`${device.device_type}-${device.direction}`}>
                  <span>
                    {device.direction
                      ? `${title(device.direction)} ${title(device.device_type)}`
                      : "Junction controller"}
                  </span>
                  <Badge value={device.status} />
                </div>
              ))}
            </div>
            <form
              className="device-form"
              onSubmit={(event) => {
                event.preventDefault();
                void action("Device status report", "/device-events", {
                  event_id: crypto.randomUUID(),
                  junction_id: junctionId,
                  device_type: deviceType,
                  direction:
                    deviceType === "SIGNAL_CONTROLLER"
                      ? undefined
                      : deviceDirection,
                  status: deviceStatus,
                  timestamp: new Date().toISOString(),
                });
              }}
            >
              <label>
                Device
                <select
                  value={deviceType}
                  onChange={(e) => setDeviceType(e.target.value)}
                >
                  <option value="SIGNAL_CONTROLLER">Junction controller</option>
                  <option value="SENSOR">Vehicle sensor</option>
                  <option value="SIGNAL">Traffic signal</option>
                </select>
              </label>
              <label>
                Direction
                <select
                  value={deviceDirection}
                  onChange={(e) =>
                    setDeviceDirection(e.target.value as Direction)
                  }
                  disabled={deviceType === "SIGNAL_CONTROLLER"}
                >
                  {directions.map((d) => (
                    <option key={d}>{d}</option>
                  ))}
                </select>
              </label>
              <label>
                Status
                <select
                  value={deviceStatus}
                  onChange={(e) => setDeviceStatus(e.target.value)}
                >
                  {["OFFLINE", "ONLINE", "DEGRADED", "WARNING", "UNKNOWN"].map(
                    (value) => (
                      <option key={value}>{value}</option>
                    ),
                  )}
                </select>
              </label>
              <button className="button secondary" disabled={disabled}>
                Report device status
              </button>
            </form>
          </section>
        </>
      )}
    </>
  );
}
