# ADR-0011: Password Authentication Mode

**Status:** Implemented
**Date:** 2026-05-20

## Overview

Add an alternative gateway authentication mode to the Claw CRD. The default token-based authentication generates a per-instance cryptographic token and, for non-local Control UI sessions, requires device pairing — each browser must complete a one-time approval before interacting with the instance. This provides strong per-device identity but adds friction when multiple users need quick access to the same instance (workshops, shared team environments, demos).

Password mode offers a simpler alternative: users authenticate by entering a shared password in the browser. The gateway receives the password through the `OPENCLAW_GATEWAY_PASSWORD` environment variable sourced from a Kubernetes Secret; `injectAuthMode` writes only `gateway.auth.mode`, and the password is never stored in the ConfigMap.

The `spec.auth` field provides three controls:
- `mode` — selects between `token` (default) and `password` authentication
- `passwordSecretRef` — references the Secret holding the shared password (required for password mode)
- `disableDevicePairing` — controls device identity only for legacy generated config; the 7.2 generator requires device identity for non-local Control UI sessions

## Decisions

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | Where should the password live? | Kubernetes Secret via `passwordSecretRef` | Consistent with every other secret in the operator. Keeps passwords out of the CR spec. Secret watch triggers re-reconcile on rotation. |
| 2 | Should `passwordSecretRef` reuse `SecretRefEntry`? | Yes | Same struct used by credentials, web search, MCP. No new types needed. |
| 3 | How to validate the conditional requirement? | CEL `XValidation` rule on `AuthSpec` | Rejects invalid CRs at admission. Same pattern as `CredentialSpec`, `McpServerSpec`, `WebSearchSpec`. No webhook infrastructure needed. |
| 4 | Should device pairing be automatically disabled? | Legacy only | The legacy generator retains the existing setting; the explicit 7.2 generator stops emitting the retired bypass. |
| 5 | Should `status.url` include the token fragment? | No — omit `#token=` in password mode | The token fragment auto-authenticates the browser, bypassing the password prompt. In password mode the user enters the password in the UI. |
| 6 | Should this be a new CRD field or a config override? | New `spec.auth` field | Auth mode is infrastructure-level. It affects gateway config, status URL format, and device pairing behavior. Too cross-cutting for a config patch. |

## Architecture

### Reconciliation Flow

```
Claw CR spec.auth
       │
       ▼
 resolveAuthPassword()          ◄── validate password Secret exists
       │                             return "" for nil/token mode
       │                             error → Ready=False + ValidationFailed + clear URL
       │
       ▼
 enrichConfigAndNetworkPolicy()
       ├─► injectAuthModeIntoConfigMap()
       │     ├── mode metadata only (mode == password):
       │     │     gateway.auth.mode = "password"
       │     │     (no password value — delivered via env var)
       │     └── device pairing:
       │           legacy config honors disableDevicePairing; 7.2 requires identity for non-local Control UI sessions
       │
 configureDeployments()
       └─► configureClawDeploymentForAuth()
             └── OPENCLAW_GATEWAY_PASSWORD env var from secretKeyRef
                 (password never written to ConfigMap)
       │
       ▼
 updateStatus()
       └── password mode: status.url = routeURL (no #token= fragment)
           token mode:    status.url = routeURL + #token=<gateway-token>
```

### Secret Watch

`clawReferencesSecret` includes `spec.auth.passwordSecretRef` so that password Secret updates trigger re-reconcile. The gateway Deployment supplies the password through `OPENCLAW_GATEWAY_PASSWORD`, sourced from the password Secret via `secretKeyRef`; it is never written to the ConfigMap.

## CRD Schema

### AuthSpec Fields

```yaml
spec:
  auth:
    mode: password              # "token" (default) or "password"
    passwordSecretRef:          # required when mode is "password"
      name: my-password
      key: password
    disableDevicePairing: false # legacy gateway images only; false requires pairing
```

### CEL Validation

On `AuthSpec`:
- `passwordSecretRef` is required when `mode` is `password`

### Reconciler Validation

- Secret referenced by `passwordSecretRef` must exist and contain the specified key with a non-empty value
- Failures set `Ready=False` with reason `ValidationFailed`

## ConfigMap and Deployment Injection

Auth configuration is split between the ConfigMap (non-sensitive metadata) and the Deployment (secret-backed env var):

**ConfigMap** (`injectAuthMode`): writes only `gateway.auth.mode`. No password value is stored in the ConfigMap. This follows the same pattern as the gateway token, which is delivered via `OPENCLAW_GATEWAY_TOKEN` env var rather than the config file.

**Deployment** (`configureClawDeploymentForAuth`): adds `OPENCLAW_GATEWAY_PASSWORD` as an env var sourced from the password Secret via `secretKeyRef`. OpenClaw reads this env var as a fallback when `gateway.auth.password` is not in the config (see `auth-surface-resolution.ts` in upstream OpenClaw).

The operator writes both legacy and 7.2 generated configs. A user-approved `spec.migration.doctorFix` Job migrates the PVC before a 7.2 image starts; then the init container reads the version from the same OpenClaw image that runs the gateway and selects the matching config. Legacy config continues to emit the bypass according to `disableDevicePairing`, while OpenClaw 7.2 and later require device identity for non-local Control UI sessions.

When neither concern applies (auth is nil, mode is token, no explicit override), both functions are no-ops.

## Examples

### Password mode

```yaml
apiVersion: claw.sandbox.redhat.com/v1alpha1
kind: Claw
metadata:
  name: instance
spec:
  auth:
    mode: password
    passwordSecretRef:
      name: my-password
      key: password
  credentials:
    - name: gemini
      type: apiKey
      secretRef:
        - name: gemini-api-key
          key: api-key
      provider: google
```

Password mode retains its existing pairing setting when its gateway image uses the legacy config.

### Password mode with device pairing

```yaml
spec:
  auth:
    mode: password
    passwordSecretRef:
      name: team-password
      key: password
```

Users enter the shared password and also go through device pairing for non-local Control UI sessions when their gateway image uses the 7.2 config. Before that, `disableDevicePairing` retains its existing behavior.

### Token mode (default)

```yaml
spec:
  credentials:
    - name: gemini
      type: apiKey
      secretRef:
        - name: gemini-api-key
          key: api-key
      provider: google
```

No `auth` field is needed. Legacy config disables pairing by default; the 7.2 config generator requires it for non-local Control UI sessions.

## Future Considerations

- Per-user password mode (multiple passwords mapped to user identities)
- OIDC/SSO integration as a third auth mode
- Password complexity validation or minimum length enforcement
