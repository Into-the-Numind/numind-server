from pathlib import Path

from parse_eval.samples.loader import load_synthetic_manifest, SampleSpec


def test_load_manifest_returns_specs(tmp_path):
    manifest = tmp_path / "manifest.yaml"
    manifest.write_text(
        "samples:\n"
        "  - file: a.pdf\n"
        "    description: test\n"
        "    keywords: [x, y]\n"
        "    has_table: true\n"
    )
    specs = load_synthetic_manifest(manifest)
    assert len(specs) == 1
    assert specs[0].file == "a.pdf"
    assert specs[0].keywords == ["x", "y"]
    assert specs[0].has_table is True


def test_golden_path_resolution():
    spec = SampleSpec(file="foo.pdf", description="d", keywords=[], has_table=False)
    assert spec.golden_name() == "foo.golden.txt"
