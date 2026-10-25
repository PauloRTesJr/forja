# Security policy

## Reporting a vulnerability

Report it privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability** ([direct link](https://github.com/PauloRTesJr/forja/security/advisories/new)). Do not open a public issue, pull request or discussion about it.

Include what you found, the version or commit, your OS, the steps to reproduce it, and what an attacker gains. A proof of concept helps; keep it to what shows the problem.

We aim to acknowledge a report within 7 days and to agree on a disclosure date with you once we understand it. We credit reporters in the advisory unless they ask us not to.

## Supported versions

Forja is pre-1.0. Fixes land on `main` and in the next release; older releases are not patched.

## What matters most

- **The core's local API.** It listens on a Unix socket or a named pipe for the current user only. Anything that lets another user or a web page reach it is a vulnerability.
- **Sharing a GPU.** A computer that shares its GPU serves the engine API over TLS with a self-signed certificate, and accepts a six-digit pairing code (ten minutes, one use, five attempts) in exchange for a long-lived token. Ways around the code, the token or the certificate pinning are in scope.
- **Secrets.** API keys and share tokens live only in the OS keychain. No API returns one, and the core's log redacts every value it has read. A key that leaks into a log, a file, the diagnostics report or an API response is in scope.
- **Installs.** Model weights are pinned to a commit and verified by sha256, and so is upstream model code. A way to make Forja install something other than what a manifest pins is in scope.
- **Exports.** Conversions run the user's own Blender in the background with factory settings. A crafted asset or file name that escapes the export folder or runs code there is in scope.

## What is not a vulnerability

- **Adapters run code.** A model's adapter, and the upstream code it fetches, are ordinary programs running as you, in their own process but not in a sandbox. That an adapter you chose to install can do anything you can is by design; installing or running one without your choice is not.
- Problems in a model's own code or weights, which belong with its publisher, unless Forja's handling of them makes them worse.
- The quality or content of generated assets.
