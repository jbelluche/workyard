export type Order = {
  id: string;
  customer: string;
  region: "west" | "central" | "east";
  total: number;
  status: "queued" | "packing" | "shipped";
};

export type ActivityEvent = {
  id: string;
  service: string;
  message: string;
  at: string;
};

export type AnalyticsSummary = {
  orderCount: number;
  revenue: number;
  averageOrderValue: number;
  busiestRegion: string;
};

export const demoOrders: Order[] = [
  { id: "ord_1001", customer: "Northstar Goods", region: "west", total: 184, status: "packing" },
  { id: "ord_1002", customer: "Copperline Supply", region: "central", total: 96, status: "queued" },
  { id: "ord_1003", customer: "Harbor House", region: "east", total: 242, status: "shipped" },
  { id: "ord_1004", customer: "Trailhead Labs", region: "west", total: 133, status: "packing" }
];

export function serviceURL(name: string, fallbackPort: number): string {
  const key = name.toUpperCase().replace(/[^A-Z0-9]/g, "_");
  return process.env[`WORKYARD_SERVICE_${key}_URL`] ?? `http://127.0.0.1:${fallbackPort}`;
}
