## What and why

<!-- What this changes, and the problem it solves. Link the issue if there is one. -->

## How I tested it

<!-- make check, plus what you ran by hand: in the app, with the CLI, on which OS and GPU. -->

## Checklist

- [ ] The title is a Conventional Commit (`feat(engine): ...`, `fix(desktop): ...`).
- [ ] `make check` passes.
- [ ] A feature change reaches both the app and the `forja` CLI through one core endpoint.
- [ ] Docs in `docs/` say what changed for users; scope, defaults, presets or licensing changes add a row to `docs/decisions.md`.
- [ ] An adapter whose output changed bumps its `version` (and the manifest's `adapter_version`); a `/v1` change bumps the API version in both the engine and the core.
- [ ] I read `git diff --staged`: no keys, tokens, passwords, private hostnames or IPs, or paths from my machine.
- [ ] Every commit is signed off (`git commit -s`), certifying the [Developer Certificate of Origin](https://developercertificate.org/).
