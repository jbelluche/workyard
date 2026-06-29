export const dynamic = "force-dynamic";

type StatusResponse = {
  ok: boolean;
  service: string;
  dependencies: Record<string, unknown>;
};

type EventsResponse = {
  events: Array<{
    id: string;
    service: string;
    message: string;
    at: string;
  }>;
};

const apiURL = process.env.WORKYARD_SERVICE_API_URL ?? "http://127.0.0.1:4100";
const eventsURL = process.env.WORKYARD_SERVICE_EVENTS_URL ?? "http://127.0.0.1:4101";

async function readJSON<T>(url: string): Promise<T | null> {
  try {
    const response = await fetch(url, { cache: "no-store" });
    if (!response.ok) return null;
    return (await response.json()) as T;
  } catch {
    return null;
  }
}

export default async function Page() {
  const [status, activity] = await Promise.all([
    readJSON<StatusResponse>(`${apiURL}/api/status`),
    readJSON<EventsResponse>(`${eventsURL}/events`)
  ]);

  return (
    <main className="console">
      <header>
        <p>operator workspace</p>
        <h1>Operator Console</h1>
      </header>

      <section className="grid">
        <article className="panel">
          <span className={status?.ok ? "dot ok" : "dot bad"} />
          <h2>API dependency graph</h2>
          <pre>{JSON.stringify(status?.dependencies ?? { api: "offline", apiURL }, null, 2)}</pre>
        </article>

        <article className="panel">
          <span className={activity ? "dot ok" : "dot bad"} />
          <h2>Recent activity</h2>
          <div className="events">
            {(activity?.events ?? []).slice(0, 8).map((event) => (
              <div key={event.id} className="event">
                <strong>{event.service}</strong>
                <span>{event.message}</span>
                <time>{new Date(event.at).toLocaleTimeString()}</time>
              </div>
            ))}
            {!activity && <p className="empty">Event service unavailable at {eventsURL}</p>}
          </div>
        </article>
      </section>
    </main>
  );
}
