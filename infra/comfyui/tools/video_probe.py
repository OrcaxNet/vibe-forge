#!/usr/bin/env python3
"""Probe a generated MP4 and optionally normalize 1280x704/121f to 720p/120f."""

from __future__ import annotations

import argparse
import hashlib
import json
from fractions import Fraction
from pathlib import Path
from typing import Any

import av
import numpy as np


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while chunk := handle.read(8 * 1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def probe(path: Path) -> dict[str, Any]:
    with av.open(str(path), mode="r") as container:
        if not container.streams.video:
            raise ValueError(f"{path} contains no video stream")
        stream = container.streams.video[0]
        decoded_frames = sum(1 for _ in container.decode(stream))
        rate = stream.average_rate or stream.base_rate or Fraction(0, 1)
        fps = float(rate)
        duration_seconds = decoded_frames / fps if fps else None
        return {
            "path": str(path),
            "size_bytes": path.stat().st_size,
            "sha256": sha256_file(path),
            "codec": stream.codec_context.name,
            "width": stream.codec_context.width,
            "height": stream.codec_context.height,
            "pixel_format": stream.codec_context.pix_fmt,
            "fps": fps,
            "frames": decoded_frames,
            "duration_seconds": duration_seconds,
        }


def normalize(source: Path, destination: Path) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists():
        raise FileExistsError(
            f"refusing to overwrite existing delivery file: {destination}"
        )

    with av.open(str(source), mode="r") as source_container:
        if not source_container.streams.video:
            raise ValueError(f"{source} contains no video stream")
        source_stream = source_container.streams.video[0]
        if (
            source_stream.codec_context.width != 1280
            or source_stream.codec_context.height != 704
        ):
            raise ValueError("normalization expects a 1280x704 Wan-native source")

        with av.open(str(destination), mode="w") as output_container:
            output_container.metadata["source_sha256"] = sha256_file(source)
            output_stream = output_container.add_stream("libx264", rate=Fraction(24, 1))
            output_stream.width = 1280
            output_stream.height = 720
            output_stream.pix_fmt = "yuv420p"
            output_stream.options = {"crf": "18", "preset": "medium"}

            frame_count = 0
            for source_frame in source_container.decode(source_stream):
                if frame_count >= 120:
                    break
                source_rgb = source_frame.to_ndarray(format="rgb24")
                if source_rgb.shape != (704, 1280, 3):
                    raise ValueError(f"unexpected frame shape: {source_rgb.shape}")
                padded = np.zeros((720, 1280, 3), dtype=np.uint8)
                padded[8:712, :, :] = source_rgb
                output_frame = av.VideoFrame.from_ndarray(padded, format="rgb24")
                output_frame.pts = frame_count
                output_frame.time_base = Fraction(1, 24)
                for packet in output_stream.encode(output_frame):
                    output_container.mux(packet)
                frame_count += 1
            for packet in output_stream.encode():
                output_container.mux(packet)

    if frame_count != 120:
        destination.unlink(missing_ok=True)
        raise ValueError(
            f"normalization requires at least 120 source frames, got {frame_count}"
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("--normalize", type=Path, dest="destination")
    args = parser.parse_args()

    result: dict[str, Any] = {"source": probe(args.source)}
    if args.destination:
        normalize(args.source, args.destination)
        result["delivery"] = probe(args.destination)
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
