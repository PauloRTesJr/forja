"""Another computer trades a short code, once, for this engine's token."""

import pytest
from fastapi.testclient import TestClient

from forja_engine.api.app import EngineConfig, create_app


@pytest.fixture
def engine(tmp_path):
    app = create_app(EngineConfig(store_root=tmp_path / "store", token="secret", local_hosts=("testclient",)))
    with TestClient(app) as c:
        yield c


def set_code(c, code="482913"):
    resp = c.post("/v1/pairing", json={"code": code}, headers={"Authorization": "Bearer secret"})
    assert resp.status_code == 204


def test_a_code_trades_for_the_token_once(engine):
    assert engine.post("/v1/pair", json={"code": "482913"}).status_code == 410
    set_code(engine)
    resp = engine.post("/v1/pair", json={"code": "482913"})
    assert resp.status_code == 200 and resp.json()["token"] == "secret"
    assert engine.post("/v1/pair", json={"code": "482913"}).status_code == 410


def test_guessing_ends_the_code(engine):
    set_code(engine)
    for _ in range(5):
        assert engine.post("/v1/pair", json={"code": "000000"}).status_code == 403
    assert engine.post("/v1/pair", json={"code": "482913"}).status_code == 429
    assert engine.post("/v1/pair", json={"code": "482913"}).status_code == 410


def test_only_this_computer_sets_a_code(tmp_path):
    app = create_app(EngineConfig(store_root=tmp_path / "store", token="secret"))
    with TestClient(app) as c:
        resp = c.post("/v1/pairing", json={"code": "482913"}, headers={"Authorization": "Bearer secret"})
        assert resp.status_code == 403
        assert c.post("/v1/pairing", json={"code": "482913"}).status_code == 401
