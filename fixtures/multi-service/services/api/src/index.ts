import { cors } from "@elysiajs/cors";
import { demoOrders, serviceURL, type AnalyticsSummary } from "@workyard-multi/shared";
import { Elysia } from "elysia";

const port = Number(process.env.PORT ?? process.env.WORKYARD_PORT ?? 4100);
const host = process.env.HOST ?? "0.0.0.0";
const analyticsURL = serviceURL("analytics", 4103);
const eventsURL = serviceURL("events", 4101);

async function readJSON<T>(url: string, fallback: T): Promise<T> {
  try {
    const response = await fetch(url);
    if (!response.ok) return fallback;
    return (await response.json()) as T;
  } catch {
    return fallback;
  }
}

async function publishEvent(message: string) {
  try {
    await fetch(`${eventsURL}/events`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ service: "api", message })
    });
  } catch {
    // The API should stay usable if the optional event feed is unavailable.
  }
}

const app = new Elysia()
  .use(cors())
  .get("/health", () => ({
    ok: true,
    service: "api",
    port,
    analyticsURL,
    eventsURL
  }))
  .get("/api/orders", async () => {
    const analytics = await readJSON<AnalyticsSummary>(`${analyticsURL}/summary`, {
      orderCount: demoOrders.length,
      revenue: demoOrders.reduce((sum, order) => sum + order.total, 0),
      averageOrderValue: 0,
      busiestRegion: "unknown"
    });
    await publishEvent(`served ${demoOrders.length} orders`);
    return {
      orders: demoOrders,
      analytics,
      dependencies: {
        analyticsURL,
        eventsURL
      }
    };
  })
  .get("/api/status", async () => {
    const analytics = await readJSON(`${analyticsURL}/health`, { ok: false, service: "analytics" });
    const events = await readJSON(`${eventsURL}/health`, { ok: false, service: "events" });
    return {
      ok: true,
      service: "api",
      dependencies: { analytics, events }
    };
  })
  .listen({ hostname: host, port });

console.log(`api listening on ${app.server?.hostname}:${app.server?.port}`);
