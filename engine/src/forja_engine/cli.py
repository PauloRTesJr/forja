from __future__ import annotations

import argparse
import ipaddress
import logging
import os
import sys
from pathlib import Path

import uvicorn

from forja_engine.api.app import EngineConfig, create_app
from forja_engine.vram import FitMode


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(prog="forja-engine")
    sub = parser.add_subparsers(dest="cmd", required=True)
    serve = sub.add_parser("serve", help="run the engine API")
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=0, help="0 picks a free port and prints it")
    serve.add_argument("--store", type=Path, required=True, help="artifact store root (shared with the core)")
    serve.add_argument("--manifests", type=Path, action="append", default=[], help="extra manifest folders")
    serve.add_argument("--fit-mode", choices=[m.value for m in FitMode], default=FitMode.FIT_FIRST.value)
    serve.add_argument("--vram-reserve-gb", type=float, default=1.0)
    serve.add_argument("--idle-unload-minutes", type=float, default=15)
    serve.add_argument("--tls-cert", type=Path)
    serve.add_argument("--tls-key", type=Path)
    serve.add_argument("--log-level", default="info")
    args = parser.parse_args(argv)

    token = os.environ.get("FORJA_ENGINE_TOKEN") or None
    loopback = _is_loopback(args.host)
    if not loopback and not (token and args.tls_cert and args.tls_key):
        sys.exit("binding beyond loopback requires FORJA_ENGINE_TOKEN, --tls-cert and --tls-key")

    logging.basicConfig(level=args.log_level.upper(), format="%(asctime)s %(levelname)s %(name)s %(message)s")
    cfg = EngineConfig(
        store_root=args.store,
        token=token,
        fit_mode=FitMode(args.fit_mode),
        vram_reserve_bytes=int(args.vram_reserve_gb * (1 << 30)),
        idle_unload_seconds=args.idle_unload_minutes * 60,
        manifest_dirs=args.manifests,
    )
    config = uvicorn.Config(
        create_app(cfg),
        host=args.host,
        port=args.port,
        log_level=args.log_level,
        ssl_certfile=args.tls_cert,
        ssl_keyfile=args.tls_key,
    )
    server = uvicorn.Server(config)
    _announce_port(server)
    server.run()


def _is_loopback(host: str) -> bool:
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _announce_port(server: uvicorn.Server) -> None:
    """Print the bound port on one line, so the core can start the engine on port 0 and read it."""
    original = server.startup

    async def startup(sockets: list | None = None) -> None:  # type: ignore[type-arg]
        await original(sockets)
        for s in server.servers:
            for sock in s.sockets:
                print(f"FORJA_ENGINE_LISTENING {sock.getsockname()[1]}", flush=True)
                return

    server.startup = startup  # type: ignore[method-assign]


if __name__ == "__main__":
    main()
