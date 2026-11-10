from forja_engine.store import ArtifactStore


def test_storing_the_stored_object_itself_keeps_it(tmp_path):
    """A stage that hands an input back as its output must not delete that input from the store."""
    store = ArtifactStore(tmp_path)
    src = tmp_path / "input.png"
    src.write_bytes(b"picture")
    ref = store.put_file(src)
    assert store.put_file(store.path(ref)) == ref
    assert store.path(ref).read_bytes() == b"picture"


def test_storing_the_same_bytes_again_removes_the_copy(tmp_path):
    store = ArtifactStore(tmp_path)
    first, second = tmp_path / "a", tmp_path / "b"
    first.write_bytes(b"same")
    second.write_bytes(b"same")
    assert store.put_file(first) == store.put_file(second)
    assert not first.exists() and not second.exists()
