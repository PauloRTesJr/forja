# Sharing a GPU over the network

A computer with a good GPU can run models for other computers on the same network: a laptop or a Mac sends its jobs' model stages to the desktop, and keeps everything else, its library included. The two pair once with a six-digit code.

## On the computer with the GPU

**In the app:** Settings, turn on **Share this computer's GPU**. It shows the address to use and a pairing code.

**From a terminal:**

```sh
forja share on      # serve this computer's engine to the network, over TLS
forja share code    # a one-use pairing code, valid for ten minutes
forja share         # whether it shares, and on which address
forja share off
```

`forja share code` needs a running core (the app, or `forja daemon`), because the code lives in its engine. The engine listens on port 7861 (`engine.share_port`); the first time, your firewall may ask whether to allow it on private networks.

## On the other computer

**In the app:** Settings, **Add another computer**. Type the address the GPU computer shows (`192.0.2.10`, or a name such as `studio.local`), the pairing code, and a name if you like.

**From a terminal:**

```sh
forja providers add 192.0.2.10 --code 482913 --label "Studio PC"
forja providers ls          # providers, their health and keys
forja models ls --all       # what each one can run
```

Forja does not search the network: type the address the sharing computer shows.

## What pairing does

- The sharing computer serves its engine over TLS with a certificate it signs itself.
- The joining computer trades the code for a long-lived access token, and pins the certificate's SHA-256, so it never talks to an impostor and never needs a certificate authority.
- A code works once, for ten minutes, and allows five attempts.
- The token is kept in the OS keychain only. No API returns it; the app shows only the store it lives in and its last four characters. `config.toml` keeps the address and the certificate fingerprint.

`forja providers token <provider>` replaces a token (read from stdin) once the new one works; `forja providers rm <provider>` forgets a computer, its token and the stages routed to it. An engine you started by hand (`forja engine serve`) joins with its token instead of a code: **Access token** in the app, `--token-stdin` in the CLI.

## Which computer a stage runs on

A newly added computer goes after this one in `routing.priority`. With the default policy, **This computer first** (`prefer-local`), a stage runs here when this computer has a ready model for it, and on the next provider in that order otherwise. **Never leave this network** (`offline-only`) uses only this computer and your own engines, never a hosted service. Install models on the other computer from here with `forja models install <id> --provider <provider>`.

The core moves only what is missing: it asks the other engine whether it already holds an input by its content hash, uploads only when it does not, and downloads the outputs into this computer's store.

The other policies, **Cheapest** and **Fastest**, fallback when a computer drops out, and cost estimates are in [routing](routing.md). A GPU you rent can run stages too ([cloud](cloud.md)).
