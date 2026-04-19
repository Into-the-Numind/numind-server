from abc import ABC, abstractmethod
from dataclasses import dataclass
from pathlib import Path


@dataclass
class ParseResult:
    pipeline: str
    sample_path: Path
    output_text: str
    error: str | None = None
    elapsed_seconds: float = 0.0


class Pipeline(ABC):
    name: str

    @abstractmethod
    def parse(self, sample_path: Path) -> ParseResult: ...
