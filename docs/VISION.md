# Vision

CMesh exists to make private AI infrastructure easier to build from machines people already have.

The long-term goal is a decentralized AI compute protocol where operators can form trusted or semi-trusted clusters, contribute bounded resources, benchmark real capacity, schedule workloads, and manage AI artifacts without depending on one cloud provider.

## First Product Promise

CMesh turns scattered machines into a visible, benchmarked, controllable AI compute pool.

In the first release, users should be able to:

- invite machines into a private cluster;
- set resource limits per worker;
- see real available CPU, memory, GPU, VRAM, and storage;
- run benchmarks and compare workers;
- submit supported AI jobs;
- route jobs to an eligible worker;
- inspect job status and results through API and dashboard.

## Later Product Promise

CMesh should evolve toward:

- replicated manager state;
- AI artifact distribution and repair;
- richer scheduling policies;
- runtime-specific distributed inference experiments;
- trusted public or semi-public resource networks;
- commercial managed offerings around deployment, observability, governance, and enterprise security.

## Honest Boundaries

CMesh should not claim that arbitrary weak machines automatically become one large GPU. That requires model/runtime-specific distributed inference and strong network assumptions.

The first reliable value is operational: resource pooling, benchmarking, scheduling, visibility, and AI workload routing.

