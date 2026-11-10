from __future__ import annotations

import argparse
import asyncio
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
    serve.add_argument(
        "--idle-unload-minutes", type=_minutes, default=15, help="0 keeps models loaded until asked"
    )
    serve.add_argument("--tls-cert", type=Path)
    serve.add_argument("--tls-key", type=Path)
    serve.add_argument("--log-level", default="info")
    serve.add_argument("--envs", type=Path, help="family environments (default: <store>/../envs)")
    serve.add_argument("--models", type=Path, help="model weights (default: <store>/../models)")
    serve.add_argument(
        "--share-host", help="also serve other computers on this address over TLS (needs a token)"
    )
    serve.add_argument("--share-port", type=int, default=7861)

    inst = sub.add_parser("install", help="build a model's family environment and fetch its weights")
    inst.add_argument("model", nargs="+")
    inst.add_argument("--store", type=Path, required=True)
    inst.add_argument("--envs", type=Path)
    inst.add_argument("--models", type=Path)
    inst.add_argument("--manifests", type=Path, action="append", default=[])
    inst.add_argument("--lane", choices=["cuda", "rocm", "mps", "cpu"], help="default: detected")
    inst.add_argument("--no-weights", action="store_true", help="build the environment only")

    sub.add_parser("lane", help="print the torch lane detected on this machine")
    args = parser.parse_args(argv)
    if args.cmd == "lane":
        from forja_engine.families import detect_lane

        print(detect_lane())
        return
    if args.cmd == "install":
        install(args)
        return

    token = os.environ.get("FORJA_ENGINE_TOKEN") or None
    loopback = _is_loopback(args.host)
    if not loopback and not (token and args.tls_cert and args.tls_key):
        sys.exit("binding beyond loopback requires FORJA_ENGINE_TOKEN, --tls-cert and --tls-key")
    if args.share_host and not (token and args.tls_cert and args.tls_key):
        sys.exit("sharing requires FORJA_ENGINE_TOKEN, --tls-cert and --tls-key")

    logging.basicConfig(level=args.log_level.upper(), format="%(asctime)s %(levelname)s %(name)s %(message)s")
    cfg = EngineConfig(
        store_root=args.store,
        token=token,
        fit_mode=FitMode(args.fit_mode),
        vram_reserve_bytes=int(args.vram_reserve_gb * (1 << 30)),
        idle_unload_seconds=args.idle_unload_minutes * 60,
        manifest_dirs=args.manifests,
        envs_dir=args.envs,
        models_dir=args.models,
    )
    app = create_app(cfg)
    # When sharing, the core's own listener stays plain loopback HTTP; TLS is for the network.
    tls = {} if args.share_host else {"ssl_certfile": args.tls_cert, "ssl_keyfile": args.tls_key}
    local = uvicorn.Config(app, host=args.host, port=args.port, log_level=args.log_level, **tls)
    server = uvicorn.Server(local)
    _announce_port(server)
    if not args.share_host:
        server.run()
        return
    lan = uvicorn.Server(
        uvicorn.Config(
            app,
            host=args.share_host,
            port=args.share_port,
            log_level=args.log_level,
            ssl_certfile=args.tls_cert,
            ssl_keyfile=args.tls_key,
            lifespan="off",  # the scheduler and idle unloader run once, under the loopback server
        )
    )
    asyncio.run(_serve_shared(server, lan, f"{args.share_host}:{args.share_port}"))


async def _serve_shared(local: uvicorn.Server, lan: uvicorn.Server, where: str) -> None:
    """Serve the core and the network; the core's listener decides how long the engine lives."""

    async def network() -> None:
        try:
            await lan.serve()
        except SystemExit:  # uvicorn exits when it cannot bind; the engine keeps serving the core
            logging.getLogger(__name__).error("could not share on %s; is the port in use?", where)

    shared = asyncio.create_task(network())
    await local.serve()
    lan.should_exit = True
    await shared


def install(args: argparse.Namespace) -> None:
    from forja_engine.install import Installer, InstallError
    from forja_engine.registry import Registry

    data = args.store.parent
    installer = Installer(
        Registry.load(args.manifests),
        envs_dir=args.envs or data / "envs",
        models_dir=args.models or data / "models",
        log=lambda line: print(line, flush=True),
    )
    try:
        for model in args.model:
            installer.install(model, lane=args.lane, weights=not args.no_weights)
    except InstallError as e:
        sys.exit(f"install failed: {e}")


def _minutes(raw: str) -> float:
    value = float(raw)
    if not value >= 0:  # also refuses nan
        raise argparse.ArgumentTypeError("must be 0 (never) or more")
    return value


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
