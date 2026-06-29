# Workyard Multi-Service Fixture

This fixture is a small monorepo for exercising Workyard against a realistic remote development shape:

- `apps/customer-ui`: Next.js app that renders order data from the API.
- `apps/operator-ui`: Next.js app that renders API status and event activity.
- `services/api`: Bun + ElysiaJS API that calls the Go analytics service.
- `services/events`: Bun + ElysiaJS event feed.
- `services/analytics`: Go HTTP service compiled on the worker for Linux arm64.
- `packages/shared`: shared TypeScript types and fixture data.

Install dependencies locally so editor tooling and TypeScript checks work:

```sh
cd fixtures/multi-service
bun install
bun run check
```

To exercise mirror as the remote development workflow, register two mirror records that point at separate remote destinations on the same worker:

```sh
workyard mirror setup --local fixtures/multi-service --worker jack@jack-r5-16gb --remote-path /home/jack/.workyard/runs/mirrors/multi-service-a --name multi-service-a --yes
workyard mirror setup --local fixtures/multi-service --worker jack@jack-r5-16gb --remote-path /home/jack/.workyard/runs/mirrors/multi-service-b --name multi-service-b --yes
```

Then start each mirrored stack by ID:

```sh
workyard mirror list
workyard mirror services up <mirror-a-id> --timeout 180s
workyard mirror services up <mirror-b-id> --timeout 180s
```

The fixture's build step runs `scripts/build-analytics-remote.sh`, which builds the Go analytics binary on Linux arm64 and avoids syncing generated binaries from the local machine. `node_modules`, `.next`, and `services/analytics/bin` may appear in the remote mirror after setup/build/start; those are worker-local artifacts protected by mirror excludes. The services read peer URLs from Workyard runtime variables such as `WORKYARD_SERVICE_API_URL` and `WORKYARD_SERVICE_ANALYTICS_URL`, so both mirrored stacks can coexist even when their configured default ports collide.

Useful checks while iterating:

```sh
workyard mirror services status <mirror-id>
workyard mirror services logs <mirror-id> api --tail 200
workyard mirror services restart <mirror-id> events
workyard mirror shell <mirror-id> --auto --tmux
workyard mirror services cleanup <mirror-id>
```
