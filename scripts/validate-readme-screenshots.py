#!/usr/bin/env python3
"""Validate README screenshot embedding.

Checks that:
1. every approved screenshot file under docs/screenshots/ is embedded in
   the root README as a Markdown image,
2. every image reference in the README resolves to an existing file,
3. the screenshot manifest (SCREENSHOT_INDEX.md) lists every file,
4. duplicate references are reported (allowed only when intentional and
   annotated, so duplicates are surfaced for review).

Exit code is non-zero when validation fails.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"
SCREENSHOTS = ROOT / "docs" / "screenshots"
MANIFEST = SCREENSHOTS / "SCREENSHOT_INDEX.md"
IMAGE_EXTENSIONS = {".png", ".jpg", ".jpeg", ".webp", ".gif"}


def approved_screenshots() -> set[str]:
    """Relative paths of all image files under docs/screenshots/."""
    found = set()
    for path in SCREENSHOTS.rglob("*"):
        if path.is_file() and path.suffix.lower() in IMAGE_EXTENSIONS:
            found.add(path.relative_to(ROOT).as_posix())
    return found


def readme_images(readme_text: str) -> list[str]:
    """Image references in the README (Markdown and HTML img tags)."""
    refs = re.findall(r"!\[[^\]]*\]\(([^)\s]+)", readme_text)
    refs += re.findall(r'<img[^>]+src="([^"]+)"', readme_text)
    return refs


def manifest_files() -> set[str]:
    if not MANIFEST.exists():
        return set()
    found = set()
    for match in re.findall(r"docs/screenshots/[^\s|]+", MANIFEST.read_text()):
        candidate = Path(match.strip().strip("`"))
        if candidate.suffix.lower() in IMAGE_EXTENSIONS:
            found.add(candidate.as_posix())
    return found


def main() -> int:
    if not README.exists():
        print("FAIL: README.md not found")
        return 1

    readme_text = README.read_text()
    approved = approved_screenshots()
    referenced = readme_images(readme_text)
    referenced_local = {r for r in referenced if r.startswith("docs/screenshots/")}
    referenced_local_all = [r for r in referenced if r.startswith("docs/screenshots/")]

    failures: list[str] = []

    # 1. Every approved screenshot is embedded.
    missing = sorted(approved - set(referenced_local))
    for ref in missing:
        failures.append(f"unreferenced screenshot: {ref}")

    # 2. Every README image reference exists.
    for ref in referenced:
        if ref.startswith("http"):
            continue
        if not (ROOT / ref).exists():
            failures.append(f"broken image path in README: {ref}")

    # 3. Duplicates surfaced for review.
    seen: set[str] = set()
    dup: set[str] = set()
    for ref in referenced_local_all:
        if ref in seen:
            dup.add(ref)
        seen.add(ref)
    duplicates = sorted(dup)
    for ref in duplicates:
        failures.append(f"duplicate reference in README: {ref}")

    # 4. Manifest consistency.
    manifest = manifest_files()
    if not manifest:
        failures.append("SCREENSHOT_INDEX.md missing or lists no screenshots")
    else:
        for ref in sorted(approved - manifest):
            failures.append(f"screenshot missing from manifest: {ref}")
        for ref in sorted(manifest - approved):
            failures.append(f"manifest lists non-existent file: {ref}")

    print(f"Total approved screenshots: {len(approved)}")
    print(f"Screenshots embedded in README: {len(set(referenced_local))}")
    print(f"Unreferenced screenshots: {len(missing)}")
    print(f"Broken image paths: {sum(1 for f in failures if f.startswith('broken'))}")
    print(f"Manifest mismatches: {len(approved.symmetric_difference(manifest))}")
    print(f"Duplicate references: {len(duplicates)}")

    if failures:
        print("\nFAILURES:")
        for f in failures:
            print(f"  - {f}")
        return 1
    print("\nAll screenshot validations passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
