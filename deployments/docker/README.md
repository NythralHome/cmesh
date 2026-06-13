# Docker Deployments

Run a CMesh manager with Docker Compose:

```sh
export CMESH_JOIN_TOKEN="replace-with-generated-token"
docker compose up -d --build
```

Use Postgres-backed state for internet alpha deployments:

```sh
export CMESH_JOIN_TOKEN="replace-with-generated-token"
export DATABASE_URL="postgres://user:password@host:5432/cmesh_alpha?sslmode=require"
docker compose up -d --build
```

Without `DATABASE_URL`, the manager uses in-memory state for local development.
