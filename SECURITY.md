# Security

CMesh v0.1.0 Linux RC1 is intended for private invited Linux clusters. It is
not ready for untrusted public compute networks or public worker marketplaces.

The supported deployment model is a private cluster where the operator controls
the manager and invites known worker owners. Public worker markets, arbitrary
code execution, payments, reputation, and fraud resistance require additional
security design.

## Supported Versions

| Version | Supported | Notes |
| --- | --- | --- |
| `v0.1.0-linux-rc.1` | Yes | Linux manager/worker RC for private invited clusters |
| older alpha builds | No | Use the latest Linux RC package and signed artifacts |

Windows, macOS, GPU execution, public untrusted worker networks, and arbitrary
model slicing are not part of the current security support boundary.

## Initial Security Boundaries

- Workers should only execute supported CMesh job types.
- Resource limits must be explicit and visible.
- Cluster joins should require invite tokens.
- Operator APIs must require `Authorization: Bearer $CMESH_OPERATOR_TOKEN`.
- Runtime and model artifacts must be checksum-verified.
- Public managers should follow `docs/LINUX_SECURITY_HARDENING.md`.
- Sensitive model inputs should not be sent to untrusted workers.
- Future releases should add transport encryption, signed workloads, sandboxing, and stronger identity.

## Reporting Issues

Please report suspected vulnerabilities privately through GitHub Security
Advisories for the repository. Do not open a public issue for vulnerabilities
until maintainers have confirmed disclosure timing.

Include:

- affected CMesh version and platform;
- manager/worker topology;
- whether the manager was publicly exposed;
- relevant logs with tokens redacted;
- reproduction steps;
- impact assessment.

## Token Handling

- Never publish `CMESH_JOIN_TOKEN`, `CMESH_OPERATOR_TOKEN`, worker node tokens,
  or signing private keys.
- Rotate join and operator tokens after demos, screenshots, shell-history leaks,
  or accidental logs.
- Do not send sensitive prompts or model data through workers you do not trust.
