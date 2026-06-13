# Security

CMesh is not yet ready for untrusted public compute networks.

The first supported deployment model is a private invited cluster where operators know the worker owners. Public worker markets, arbitrary code execution, payments, reputation, and fraud resistance require additional security design.

## Initial Security Boundaries

- Workers should only execute supported CMesh job types.
- Resource limits must be explicit and visible.
- Cluster joins should require invite tokens.
- Sensitive model inputs should not be sent to untrusted workers.
- Future releases should add transport encryption, signed workloads, sandboxing, and stronger identity.

## Reporting Issues

Until a formal security contact is published, please report suspected vulnerabilities privately to the maintainers rather than opening a public issue.

