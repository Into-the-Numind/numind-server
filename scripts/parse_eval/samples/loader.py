from dataclasses import dataclass, field
from pathlib import Path

import yaml


@dataclass
class SampleSpec:
    file: str
    description: str
    keywords: list[str] = field(default_factory=list)
    has_table: bool = False

    def golden_name(self) -> str:
        stem = Path(self.file).stem
        return f"{stem}.golden.txt"


def load_synthetic_manifest(path: Path) -> list[SampleSpec]:
    with open(path, "r", encoding="utf-8") as f:
        data = yaml.safe_load(f)
    return [SampleSpec(**entry) for entry in data.get("samples", [])]
