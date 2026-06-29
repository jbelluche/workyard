import { NextResponse } from "next/server";

export function GET() {
  return NextResponse.json({
    ok: true,
    service: "operator-ui",
    apiURL: process.env.WORKYARD_SERVICE_API_URL ?? "http://127.0.0.1:4100",
    eventsURL: process.env.WORKYARD_SERVICE_EVENTS_URL ?? "http://127.0.0.1:4101"
  });
}
