"""
pptx-author helpers package.

Exposes the public API surface used by main.py and the examples:
  - DeckBuilder         slide lifecycle manager
  - BrandConfig         brand identity dataclass
  - merge_brand_config  deep-merge user config onto DEFAULT_BRAND_CONFIG
  - apply_brand_to_slide / apply_brand_to_deck  brand application
  - render_chart_to_image / insert_chart_image  matplotlib chart helpers
  - layout renderer functions (one per SLIDE_LAYOUTS entry)
"""

from .brand import (
    BrandConfig,
    DEFAULT_BRAND_CONFIG,
    merge_brand_config,
    apply_brand_to_slide,
    apply_brand_to_deck,
    apply_brand_colors_to_template,
)
from .chart import render_chart_to_image, insert_chart_image
from .deck import DeckBuilder
from .layout_renderers import (
    render_cover,
    render_section,
    render_title_body,
    render_title_bullets,
    render_title_table,
    render_title_chart,
    render_title_image,
    render_two_column,
    render_end,
)

__all__ = [
    "BrandConfig",
    "DEFAULT_BRAND_CONFIG",
    "merge_brand_config",
    "apply_brand_to_slide",
    "apply_brand_to_deck",
    "apply_brand_colors_to_template",
    "render_chart_to_image",
    "insert_chart_image",
    "DeckBuilder",
    "render_cover",
    "render_section",
    "render_title_body",
    "render_title_bullets",
    "render_title_table",
    "render_title_chart",
    "render_title_image",
    "render_two_column",
    "render_end",
]
