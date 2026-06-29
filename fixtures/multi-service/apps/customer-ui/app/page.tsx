export const dynamic = "force-dynamic";

type Order = {
  id: string;
  customer: string;
  region: string;
  total: number;
  status: string;
};

type OrdersResponse = {
  orders: Order[];
  analytics: {
    orderCount: number;
    revenue: number;
    averageOrderValue: number;
    busiestRegion: string;
  };
  dependencies: Record<string, string>;
};

const apiURL = process.env.WORKYARD_SERVICE_API_URL ?? "http://127.0.0.1:4100";

async function loadOrders(): Promise<OrdersResponse | null> {
  try {
    const response = await fetch(`${apiURL}/api/orders`, { cache: "no-store" });
    if (!response.ok) return null;
    return (await response.json()) as OrdersResponse;
  } catch {
    return null;
  }
}

export default async function Page() {
  const data = await loadOrders();
  return (
    <main className="shell">
      <section className="hero">
        <p className="eyebrow">customer workspace</p>
        <h1>Customer Desk</h1>
        <p className="subtle">Live order data rendered through the API service on this Workyard run.</p>
      </section>

      <section className="metrics" aria-label="summary">
        <div>
          <span>orders</span>
          <strong>{data?.analytics.orderCount ?? "offline"}</strong>
        </div>
        <div>
          <span>revenue</span>
          <strong>{data ? `$${data.analytics.revenue}` : "offline"}</strong>
        </div>
        <div>
          <span>busiest region</span>
          <strong>{data?.analytics.busiestRegion ?? "offline"}</strong>
        </div>
      </section>

      <section className="orders">
        {(data?.orders ?? []).map((order) => (
          <article key={order.id} className="order">
            <div>
              <p>{order.customer}</p>
              <span>{order.id}</span>
            </div>
            <strong>${order.total}</strong>
            <em>{order.status}</em>
          </article>
        ))}
        {!data && <p className="empty">API unavailable at {apiURL}</p>}
      </section>
    </main>
  );
}
