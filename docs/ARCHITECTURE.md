# Architecture

CMesh is designed as a decentralized-ready AI compute cluster.

## Node Roles

### Manager

Managers maintain cluster state, expose APIs, and coordinate scheduling. A development cluster may run one manager. A resilient cluster should run three or five managers with a consensus-backed state machine.

Manager responsibilities:

- node membership;
- worker liveness;
- cluster state queries;
- job admission;
- scheduling;
- model/artifact metadata;
- dashboard API;
- future consensus replication.

### Worker

Workers contribute bounded resources and execute assigned work.

Worker responsibilities:

- register with manager quorum;
- send heartbeats;
- report hardware and configured limits;
- run benchmarks;
- maintain local artifact cache;
- execute supported job types;
- stream status, logs, and results.

## Planes

CMesh separates behavior into three planes:

- **Control plane:** membership, state, scheduling, auth, APIs.
- **Compute plane:** worker execution, runtimes, benchmark execution.
- **Storage plane:** local artifact cache now, distributed content-addressed object storage later.

## Consensus Strategy

The codebase should treat cluster state as a replicated state machine from the beginning.

Initial implementation:

```text
SingleNodeConsensus
```

Future implementation:

```text
RaftConsensus
```

Callers should depend on a consensus/store interface rather than direct local database access.

## Scheduling Strategy

The scheduler should place jobs using:

- worker health;
- configured resource limits;
- current utilization;
- benchmark score;
- model/runtime compatibility;
- artifact cache availability;
- network and queue estimates.

V1 scheduling can be simple. The API boundary should leave room for richer placement policies.

## Storage Strategy

CMesh storage is AI-artifact oriented, not a general Ceph replacement.

Initial scope:

- worker disk budget reporting;
- local artifact cache metadata;
- model/file presence tracking.

Later scope:

- content-addressed objects;
- chunk transfer;
- checksums;
- replication factor;
- repair;
- garbage collection;
- external registry integrations.

## Non-Goals For V1

- public untrusted compute marketplace;
- cryptocurrency payments;
- arbitrary remote code execution;
- POSIX distributed filesystem;
- block storage;
- automatic multi-machine execution of one large model.

