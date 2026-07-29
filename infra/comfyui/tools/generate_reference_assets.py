#!/usr/bin/env python3
"""Generate deterministic, third-party-free PPM reference fixtures."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
from pathlib import Path
from typing import Any

WIDTH = 1280
HEIGHT = 704


def parse_color(value: str) -> tuple[int, int, int]:
    if len(value) != 7 or not value.startswith("#"):
        raise ValueError(f"invalid color: {value!r}")
    return tuple(int(value[index : index + 2], 16) for index in (1, 3, 5))


class Canvas:
    def __init__(
        self, width: int, height: int, background: tuple[int, int, int]
    ) -> None:
        self.width = width
        self.height = height
        self.pixels = bytearray(background * (width * height))

    def rectangle(
        self,
        x0: int,
        y0: int,
        x1: int,
        y1: int,
        color: tuple[int, int, int],
    ) -> None:
        x0, x1 = sorted((max(0, x0), min(self.width, x1)))
        y0, y1 = sorted((max(0, y0), min(self.height, y1)))
        row = bytes(color) * max(0, x1 - x0)
        for y in range(y0, y1):
            start = (y * self.width + x0) * 3
            self.pixels[start : start + len(row)] = row

    def circle(
        self, cx: int, cy: int, radius: int, color: tuple[int, int, int]
    ) -> None:
        radius_squared = radius * radius
        for y in range(max(0, cy - radius), min(self.height, cy + radius + 1)):
            dy_squared = (y - cy) * (y - cy)
            half_width = int(math.sqrt(max(0, radius_squared - dy_squared)))
            self.rectangle(cx - half_width, y, cx + half_width + 1, y + 1, color)

    def line(
        self,
        x0: int,
        y0: int,
        x1: int,
        y1: int,
        width: int,
        color: tuple[int, int, int],
    ) -> None:
        steps = max(abs(x1 - x0), abs(y1 - y0), 1)
        for step in range(steps + 1):
            ratio = step / steps
            x = round(x0 + (x1 - x0) * ratio)
            y = round(y0 + (y1 - y0) * ratio)
            self.circle(x, y, width, color)

    def write_ppm(self, path: Path) -> None:
        header = f"P6\n{self.width} {self.height}\n255\n".encode("ascii")
        with path.open("wb") as handle:
            handle.write(header)
            handle.write(self.pixels)


def _lighten(color: tuple[int, int, int], amount: int) -> tuple[int, int, int]:
    return tuple(min(255, channel + amount) for channel in color)


def _darken(color: tuple[int, int, int], amount: int) -> tuple[int, int, int]:
    return tuple(max(0, channel - amount) for channel in color)


def draw_figure(
    canvas: Canvas,
    x: int,
    ground_y: int,
    scale: float,
    body: tuple[int, int, int],
    skin: tuple[int, int, int],
) -> None:
    head_radius = round(45 * scale)
    torso_width = round(105 * scale)
    torso_height = round(165 * scale)
    head_y = ground_y - torso_height - round(110 * scale)
    canvas.circle(x, head_y, head_radius, skin)
    canvas.rectangle(
        x - torso_width // 2,
        head_y + head_radius,
        x + torso_width // 2,
        head_y + head_radius + torso_height,
        body,
    )
    shoulder_y = head_y + head_radius + round(35 * scale)
    hip_y = head_y + head_radius + torso_height
    canvas.line(
        x - torso_width // 2, shoulder_y, x - round(95 * scale), hip_y - 20, 10, body
    )
    canvas.line(
        x + torso_width // 2, shoulder_y, x + round(95 * scale), hip_y - 20, 10, body
    )
    canvas.line(
        x - round(25 * scale),
        hip_y,
        x - round(45 * scale),
        ground_y,
        13,
        _darken(body, 18),
    )
    canvas.line(
        x + round(25 * scale),
        hip_y,
        x + round(45 * scale),
        ground_y,
        13,
        _darken(body, 18),
    )
    canvas.rectangle(
        x - torso_width // 2,
        head_y + head_radius + round(45 * scale),
        x + torso_width // 2,
        head_y + head_radius + round(63 * scale),
        _lighten(body, 35),
    )


def draw_single(
    canvas: Canvas, palette: list[tuple[int, int, int]], variant: int
) -> None:
    x = 640 + (variant - 3) * 12
    draw_figure(canvas, x, 610, 1.15, palette[0], palette[1])
    canvas.circle(x + 125, 365, 34, palette[2])
    canvas.line(x + 92, 395, x + 115, 375, 7, palette[2])


def draw_pair_prop(
    canvas: Canvas, palette: list[tuple[int, int, int]], variant: int
) -> None:
    draw_figure(canvas, 430, 610, 0.95, palette[0], (226, 185, 150))
    draw_figure(canvas, 850, 610, 0.88, palette[1], (197, 141, 115))
    prop_x = 640
    prop_y = 445 + (variant % 2) * 15
    canvas.rectangle(prop_x - 65, prop_y - 55, prop_x + 65, prop_y + 55, palette[2])
    canvas.rectangle(
        prop_x - 50, prop_y - 40, prop_x + 50, prop_y + 40, _lighten(palette[2], 30)
    )
    canvas.line(525, 405, prop_x - 60, prop_y, 9, palette[0])
    canvas.line(755, 405, prop_x + 60, prop_y, 9, palette[1])


def draw_motion(
    canvas: Canvas, palette: list[tuple[int, int, int]], variant: int
) -> None:
    horizon = 500
    canvas.rectangle(0, horizon, WIDTH, HEIGHT, _darken(palette[2], 55))
    if variant in (1, 4):
        canvas.circle(470, 535, 80, _darken(palette[0], 20))
        canvas.circle(765, 535, 80, _darken(palette[0], 20))
        canvas.circle(470, 535, 58, palette[2])
        canvas.circle(765, 535, 58, palette[2])
        canvas.line(470, 535, 610, 405, 10, palette[1])
        canvas.line(610, 405, 765, 535, 10, palette[1])
        canvas.line(470, 535, 665, 535, 10, palette[1])
        draw_figure(canvas, 620, 470, 0.62, palette[0], (225, 181, 147))
    elif variant in (2, 5):
        draw_figure(canvas, 520, 610, 0.92, palette[0], (226, 185, 150))
        canvas.line(605, 380, 790, 265, 8, palette[1])
        canvas.rectangle(775, 245, 895, 300, palette[1])
    else:
        canvas.rectangle(410, 410, 875, 575, palette[1])
        canvas.rectangle(505, 360, 760, 430, _lighten(palette[1], 25))
        canvas.circle(505, 575, 45, palette[0])
        canvas.circle(790, 575, 45, palette[0])
        canvas.rectangle(675, 445, 805, 540, palette[2])


def generate_asset(shot: dict[str, Any], output_dir: Path) -> dict[str, Any]:
    palette = [parse_color(value) for value in shot["palette"]]
    background = tuple((channel + 245) // 2 for channel in palette[2])
    canvas = Canvas(WIDTH, HEIGHT, background)
    canvas.rectangle(0, 0, WIDTH, 100, _lighten(background, 8))
    variant = int(shot["id"].rsplit("-", 1)[1])
    if shot["category"] == "single_person":
        draw_single(canvas, palette, variant)
    elif shot["category"] == "two_person_prop":
        draw_pair_prop(canvas, palette, variant)
    elif shot["category"] == "motion_camera":
        draw_motion(canvas, palette, variant)
    else:
        raise ValueError(f"unsupported category: {shot['category']}")

    path = output_dir / shot["reference_image"]
    canvas.write_ppm(path)
    payload = path.read_bytes()
    return {
        "shot_id": shot["id"],
        "filename": path.name,
        "size_bytes": len(payload),
        "sha256": hashlib.sha256(payload).hexdigest(),
        "width": WIDTH,
        "height": HEIGHT,
        "format": "PPM P6",
        "license": "MIT",
    }


def generate_all(manifest_path: Path, output_dir: Path) -> dict[str, Any]:
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    shots = manifest.get("shots")
    if not isinstance(shots, list) or len(shots) != 15:
        raise ValueError("shot manifest must contain exactly 15 shots")
    category_counts: dict[str, int] = {}
    for shot in shots:
        category_counts[shot["category"]] = category_counts.get(shot["category"], 0) + 1
    if set(category_counts.values()) != {5} or len(category_counts) != 3:
        raise ValueError("shot manifest must contain 5 shots in each of 3 categories")

    output_dir.mkdir(parents=True, exist_ok=True)
    assets = [generate_asset(shot, output_dir) for shot in shots]
    generator_sha = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    evidence = {
        "schema_version": 1,
        "generator": str(Path(__file__).name),
        "generator_sha256": generator_sha,
        "manifest": str(manifest_path.name),
        "manifest_sha256": hashlib.sha256(manifest_path.read_bytes()).hexdigest(),
        "assets": assets,
    }
    evidence_path = output_dir / "reference-assets.json"
    evidence_path.write_text(
        json.dumps(evidence, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return evidence


def main() -> int:
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--manifest",
        type=Path,
        default=root / "benchmark" / "shots.json",
    )
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=root / "runtime" / "input",
    )
    args = parser.parse_args()
    evidence = generate_all(args.manifest, args.output_dir)
    print(json.dumps(evidence, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
