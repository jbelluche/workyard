# Workyard Multi-Service Fixture

This fixture is a small monorepo for exercising Workyard against a realistic remote development shape:

- `apps/customer-ui`: Next.js app that renders order data from the API.
- `apps/operator-ui`: Next.js app that renders API status and event activity.
- `services/api`: Bun + ElysiaJS API that calls the Go analytics service.
- `services/events`: Bun + ElysiaJS event feed.
- `services/analytics`: Go HTTP service compiled locally for Linux arm64.
- `packages/shared`: shared TypeScript types and fixture data.

Before deploying to a Raspberry Pi worker, build the Go service binary from the repo root:

```sh
cd fixtures/multi-service
bun install
bun run build:analytics
```

Then deploy two copies to the same worker:

```sh
workyard deploy --project fixtures/multi-service --worker jack@jack-r5-16gb --run multi-service-a --install --fresh --timeout 90s
workyard deploy --project fixtures/multi-service --worker jack@jack-r5-16gb --run multi-service-b --fresh --timeout 90s
```

The services read peer URLs from Workyard runtime variables such as `WORKYARD_SERVICE_API_URL` and `WORKYARD_SERVICE_ANALYTICS_URL`, so both deployments can coexist even when their configured default ports collide.
