---
name: illustrator
tiers:
  base:
    pip: requirements.txt
    gives: ["probe", "resize", "crop", "rotate", "colour", "profile"]
  pdf:
    pip: requirements-pdf.txt
    gives: ["pdf_pages"]
  cv:
    pip: requirements-cv.txt
    gives: ["deskew"]
requires:
  env: ["KAIJU_WORKSPACE"]
root: false
---

# Installing illustrator

Nothing here needs root and nothing needs a system package. Every tier is a pip
install into the plugin host's venv, and each wheel brings its own native code —
`pikepdf` bundles qpdf, `opencv-python-headless` bundles OpenCV.

Install the base tier and the plugin works. The other two add actions; without
them those actions return a message naming the file to install and the rest of
the plugin is unaffected.

## Tiers

| Tier | Install | Adds |
|---|---|---|
| base | `pip install -r requirements.txt` | probe, resize, crop, rotate, colour, profile |
| pdf | `pip install -r requirements-pdf.txt` | pdf_pages |
| cv | `pip install -r requirements-cv.txt` | deskew |

`cv` is the heaviest and the least needed — OpenCV is used only to measure a skew
angle, and Pillow performs the rotation so that EXIF, the ICC profile and the
palette survive it.

## KAIJU_WORKSPACE

The plugin host must be started with `KAIJU_WORKSPACE` set to the agent's
workspace. The remote plugin protocol does not carry it, so the plugin enforces
its own sandbox: every path is resolved and refused if it lands outside that
directory.

Unset, nothing is readable or writable and every call returns an error saying so.
That is deliberate — a file tool with no sandbox is worse than one that is
switched off.

## Colour profiles — an install step

`colour` and `profile` need three ICC profiles in `profiles/`: `srgb.icc`,
`default_cmyk.icc` and `default_gray.icc`. They are NOT in this repository. They
are third-party binaries — Artifex's, distributed with ghostscript under the
AGPL — and one of them is 187KB, so they are installed rather than committed.
`profiles/` is ignored by git.

Install them from the system set where it has them:

    mkdir -p profiles
    cp /usr/share/color/icc/ghostscript/srgb.icc profiles/
    cp /usr/share/color/icc/ghostscript/default_cmyk.icc profiles/
    cp /usr/share/color/icc/ghostscript/default_gray.icc profiles/

Linux keeps them in `/usr/share/color/icc`, Windows in
`System32\spool\drivers\color`, and a given machine may have none — in which
case take them from a ghostscript release, or from any ICC source you trust, and
name them as above.

Without them, `colour` and `profile` report which file is missing and the other
actions are unaffected. A conversion is reproducible only across machines
carrying the same profiles, which is why they are pinned by name.

`default_cmyk.icc` from ghostscript is **SWOP** — a US web-coated press. It is a
correct conversion, not the right one for a European press, which wants FOGRA.
Pass `profile` with a path to use the one your print shop gives you.

## Windows and Linux

The plugin itself is platform-neutral: paths are resolved with `pathlib`, so
backslashes, drive letters and case-insensitivity are handled, and no action
shells out.

What is not neutral is how the host is launched — `plugins/start.sh` is bash and
expects `venv/bin/`, where Windows has `venv\Scripts\`. That belongs to the host,
not to this plugin.
