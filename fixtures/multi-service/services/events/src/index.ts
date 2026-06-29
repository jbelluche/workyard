import { cors } from "@elysiajs/cors";
import type { ActivityEvent } from "@workyard-multi/shared";
import { Elysia, t } from "elysia";

const port = Number(process.env.PORT ?? process.env.WORKYARD_PORT ?? 4101);
const host = process.env.HOST ?? "0.0.0.0";

const events: ActivityEvent[] = [
  {
    id: "evt_boot",
    service: "events",
    message: "event feed booted",
    at: new Date().toISOString()
  }
];

const app = new Elysia()
  .use(cors())
  .get("/health", () => ({ ok: true, service: "events", port, count: events.length }))
  .get("/events", () => ({ events: events.slice(-20).reverse() }))
  .post(
    "/events",
    ({ body }) => {
      const event: ActivityEvent = {
        id: `evt_${Date.now()}`,
        service: body.service,
        message: body.message,
        at: new Date().toISOString()
      };
      events.push(event);
      return event;
    },
    {
      body: t.Object({
        service: t.String(),
        message: t.String()
      })
    }
  )
  .listen({ hostname: host, port });

console.log(`events listening on ${app.server?.hostname}:${app.server?.port}`);
