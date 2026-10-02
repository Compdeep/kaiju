"""illustrator — inspect and correct images, and fix PDF page formatting.

Pillow throughout, never OpenCV, for anything that opens or writes a file.
cv2.imread drops EXIF, drops the ICC profile, and flattens a palette image into a
BGR array; Pillow keeps all three — but only if they are handed back on save,
which is what _save is for and why nothing else in here calls Image.save.

Three tiers, declared in install.md and all installable with pip into this
plugin's venv. No system package, no root:

  base  Pillow, numpy          probe, geometry, colour, ICC conversion
  pdf   pikepdf                page subset / reorder / rotate
  cv    opencv-python-headless  deskew

An absent tier is not an error. Its actions report what to install and the rest
of the plugin stands on its own.

The host calls a sync invoke directly on its event loop, so the work here runs in
a thread and invoke only awaits it. A blocking call would stall every other
plugin on the same host.
"""
from __future__ import annotations

import asyncio
import os
from pathlib import Path

try:
    from PIL import Image, ImageCms, ImageEnhance
except Exception:  # base tier not installed yet
    Image = None

try:
    import pikepdf
except Exception:  # pdf tier absent
    pikepdf = None

_HERE = Path(__file__).resolve().parent
_PROFILES = _HERE / "profiles"

# Installed here, not shipped and not discovered. They are third-party binaries, so
# they are not in the repository; install.md says how to put them here. Not
# discovered from the system either: Linux keeps profiles in /usr/share/color/icc
# and Windows in System32\spool\drivers\color, with different contents on every
# machine, so a conversion that reads whatever is installed is not reproducible.
# Absent, the two actions that need them say which file is missing.
_BUILTIN = {
    "rgb":  _PROFILES / "srgb.icc",
    "cmyk": _PROFILES / "default_cmyk.icc",
    "gray": _PROFILES / "default_gray.icc",
}

_ACTIONS = ["probe", "resize", "crop", "rotate", "colour", "profile", "deskew", "pdf_pages"]

MANIFEST = {
    "name": "illustrator",
    "description": "Inspect and correct images, and fix PDF page formatting. Keeps EXIF, "
                   "the ICC profile and the palette through every edit.",
    # Lower than a web plugin's on purpose: each call holds a decoded image in
    # memory, so this bounds megabytes rather than sockets.
    "max_concurrency": 2,
    "tools": [
        {
            "name": "image_edit",
            "description":
                "One image or PDF per call, by path, writing a new file and never the input. "
                "ALWAYS probe first: the action that is correct depends on the colour mode, "
                "the dpi and whether the file carries a palette, and probe is the only thing "
                "that reports them. Returns the path it wrote in `out`, so a later step reads "
                "${step.<tag>.out}. It never returns pixels. "
                "Actions: probe (read-only: size, mode, dpi, EXIF and ICC presence, palette "
                "size, PDF page count); resize, crop, rotate (geometry); colour (brightness, "
                "contrast, saturation, gamma, grey-world white balance); profile (convert "
                "between RGB, CMYK and grayscale through ICC profiles, for print); deskew "
                "(straighten a scan); pdf_pages (subset, reorder or rotate pages).",
            "params": {
                "type": "object",
                "properties": {
                    "action": {"type": "string", "enum": _ACTIONS,
                               "description": "what to do; probe is read-only"},
                    "path": {"type": "string",
                             "description": "one input file, relative to the workspace. One path, "
                                            "never a list and never a command's output"},
                    "out": {"type": "string",
                            "description": "where to write, relative to the workspace. Required by "
                                           "every action except probe. The input is never modified"},
                    "width": {"type": "integer", "description": "resize: target width in pixels"},
                    "height": {"type": "integer", "description": "resize: target height in pixels"},
                    "scale": {"type": "number", "description": "resize: factor, instead of width/height"},
                    "box": {"type": "array", "items": {"type": "integer"},
                            "description": "crop: [left, top, right, bottom] in pixels"},
                    "degrees": {"type": "number", "description": "rotate: clockwise degrees"},
                    "brightness": {"type": "number", "description": "colour: 1.0 is unchanged"},
                    "contrast": {"type": "number", "description": "colour: 1.0 is unchanged"},
                    "saturation": {"type": "number", "description": "colour: 1.0 is unchanged, 0 is grey"},
                    "gamma": {"type": "number", "description": "colour: 1.0 is unchanged, <1 brightens"},
                    "white_balance": {"type": "boolean", "description": "colour: grey-world correction"},
                    "to": {"type": "string", "enum": ["rgb", "cmyk", "gray"],
                           "description": "profile: the colour space to convert into"},
                    "profile": {"type": "string",
                                "description": "profile: an ICC file to use instead of the shipped one — "
                                               "a print shop's profile, for instance"},
                    "keep_palette": {"type": "boolean",
                                     "description": "a palette image losing its palette to a colour edit is "
                                                    "requantised back to it. Lossy, and off by default"},
                    "pages": {"type": "array", "items": {"type": "integer"},
                              "description": "pdf_pages: 1-based page numbers, in the order wanted"},
                },
                "required": ["action", "path"],
            },
        }
    ],
}


# ── envelope ──────────────────────────────────────────────────────────────────
# "type", not "kind". kind is a legacy name kaiju still parses and new code does
# not write.

def _ok(content: str, data: dict) -> dict:
    return {"type": "file", "status": "ok", "content": content, "data": data}


def _err(detail: str, data: dict | None = None) -> dict:
    return {"type": "file", "status": "error", "detail": detail, "data": data or {}}


# ── the sandbox ───────────────────────────────────────────────────────────────
# The remote protocol carries no workspace, so the plugin enforces its own. The
# host is started with KAIJU_WORKSPACE; without it nothing is writable, which is
# the safe direction.
#
# Resolved and compared as paths, not strings: a prefix test passes "/ws/../etc"
# and treats C:\WS and c:\ws as different places.

def _ws() -> Path | None:
    raw = os.environ.get("KAIJU_WORKSPACE", "").strip()
    if not raw:
        return None
    try:
        return Path(raw).resolve(strict=True)
    except Exception:
        return None


def _resolve(ws: Path, p: str, must_exist: bool) -> Path:
    if not p or not str(p).strip():
        raise ValueError("a path is required")
    if "\n" in str(p):
        raise ValueError("one path per call — this looks like several paths joined by newlines")
    full = (ws / str(p)).resolve()
    if not full.is_relative_to(ws):
        raise ValueError(f"{p} resolves outside the workspace")
    if must_exist and not full.exists():
        raise ValueError(f"{p} does not exist")
    return full


# ── facts ─────────────────────────────────────────────────────────────────────

def _facts(im, path: Path) -> dict:
    pal = im.getpalette()
    return {
        "path": str(path),
        "format": im.format,
        "mode": im.mode,
        "width": im.width,
        "height": im.height,
        "dpi": list(im.info.get("dpi", ())) or None,
        "has_exif": bool(im.info.get("exif")),
        "has_icc": bool(im.info.get("icc_profile")),
        "palette_colours": (len(pal) // 3) if pal else 0,
        "bytes": path.stat().st_size,
    }


# ── the one save path ─────────────────────────────────────────────────────────
# Measured: open → resize → save loses both EXIF and the ICC profile. Passing
# them back restores both. So they are passed back here, once, and nowhere else
# in this plugin calls Image.save.

def _save(img, out: Path, src) -> dict:
    out.parent.mkdir(parents=True, exist_ok=True)
    kw: dict = {}
    if exif := src.info.get("exif"):
        kw["exif"] = exif
    if icc := src.info.get("icc_profile"):
        kw["icc_profile"] = icc
    if dpi := src.info.get("dpi"):
        kw["dpi"] = dpi

    suffix = out.suffix.lower()
    notes: list[str] = []

    # A format that cannot hold the mode it was handed. Converted here with the
    # reason recorded, rather than raising from inside Pillow.
    if suffix in (".jpg", ".jpeg"):
        if img.mode in ("RGBA", "P", "LA"):
            img = img.convert("RGB")
            notes.append("converted to RGB because JPEG holds no alpha or palette")
        kw.setdefault("quality", 95)
        kw.setdefault("subsampling", 0)
    elif suffix == ".png" and img.mode == "CMYK":
        img = img.convert("RGB")
        notes.append("converted to RGB because PNG holds no CMYK")
    elif suffix in (".tif", ".tiff"):
        pass  # TIFF holds CMYK, alpha and a palette

    img.save(out, **kw)
    return {"notes": notes, "offered": sorted(k for k in ("exif", "icc_profile", "dpi") if k in kw)}


def _palette_guard(src, img, keep: bool) -> tuple:
    """A palette image has no colour to correct — the pixels are indices.

    Three outcomes, and the caller says which was taken rather than leaving it to
    be discovered in the output.
    """
    if src.mode != "P":
        return img, None
    if keep:
        return img.convert("RGB").quantize(palette=src), \
            "requantised back to the original palette (lossy)"
    return img.convert("RGB"), \
        "palette dropped: a colour edit needs RGB. Pass keep_palette to requantise"


# ── actions ───────────────────────────────────────────────────────────────────

def _act_resize(src, p):
    w, h, scale = p.get("width"), p.get("height"), p.get("scale")
    if scale:
        w, h = int(src.width * float(scale)), int(src.height * float(scale))
    elif w and not h:
        h = round(src.height * (int(w) / src.width))
    elif h and not w:
        w = round(src.width * (int(h) / src.height))
    if not (w and h):
        raise ValueError("resize needs width, height, or scale")
    # NEAREST on a palette image keeps it in P mode; a smooth filter would have
    # to leave the palette to interpolate.
    f = Image.NEAREST if src.mode == "P" else Image.LANCZOS
    return src.resize((int(w), int(h)), f), f"resized to {int(w)}×{int(h)}"


def _act_crop(src, p):
    box = p.get("box")
    if not (isinstance(box, (list, tuple)) and len(box) == 4):
        raise ValueError("crop needs box as [left, top, right, bottom]")
    return src.crop(tuple(int(v) for v in box)), f"cropped to {box}"


def _act_rotate(src, p):
    deg = float(p.get("degrees", 0))
    if not deg:
        raise ValueError("rotate needs degrees")
    f = Image.NEAREST if src.mode == "P" else Image.BICUBIC
    return src.rotate(-deg, resample=f, expand=True), f"rotated {deg}° clockwise"


def _act_colour(src, p):
    img, note = _palette_guard(src, src, bool(p.get("keep_palette")))
    if img.mode == "P":
        img = img.convert("RGB")
    done: list[str] = []

    if (wb := p.get("white_balance")):
        img = _grey_world(img)
        done.append("grey-world white balance")
    for key, cls, label in (("brightness", ImageEnhance.Brightness, "brightness"),
                            ("contrast", ImageEnhance.Contrast, "contrast"),
                            ("saturation", ImageEnhance.Color, "saturation")):
        if (v := p.get(key)) is not None and float(v) != 1.0:
            img = cls(img).enhance(float(v))
            done.append(f"{label} ×{float(v):g}")
    if (g := p.get("gamma")) is not None and float(g) != 1.0:
        img = _gamma(img, float(g))
        done.append(f"gamma {float(g):g}")

    if not done:
        raise ValueError("colour needs at least one of brightness, contrast, saturation, gamma, white_balance")
    if note:
        done.append(note)
    if bool(p.get("keep_palette")) and src.mode == "P":
        img = img.quantize(palette=src)
    return img, "; ".join(done)


def _grey_world(img):
    """Scale each channel so the image's average is neutral. Cheap, and right for
    a cast from one light source; wrong for a scene that is genuinely one colour."""
    rgb = img.convert("RGB")
    chans = rgb.split()
    means = [max(1e-6, sum(c.histogram()[i] * i for i in range(256)) / max(1, rgb.width * rgb.height))
             for c in chans]
    target = sum(means) / 3
    out = [c.point(lambda v, m=m: min(255, int(v * (target / m)))) for c, m in zip(chans, means)]
    return Image.merge("RGB", out)


def _gamma(img, g: float):
    lut = [min(255, int((i / 255.0) ** (1.0 / g) * 255 + 0.5)) for i in range(256)]
    rgb = img.convert("RGB")
    return Image.merge("RGB", [c.point(lut) for c in rgb.split()])


def _act_profile(src, p, ws: Path):
    to = str(p.get("to", "")).lower()
    if to not in _BUILTIN:
        raise ValueError("profile needs to = rgb, cmyk or gray")
    dst = _BUILTIN[to]
    if given := p.get("profile"):
        dst = _resolve(ws, str(given), must_exist=True)
    # The source profile is the file's own when it has one; sRGB is the only
    # honest assumption when it does not, and it is recorded as an assumption.
    assumed = None
    if icc := src.info.get("icc_profile"):
        import io as _io
        src_prof = ImageCms.ImageCmsProfile(_io.BytesIO(icc))
    else:
        src_prof = ImageCms.getOpenProfile(str(_BUILTIN["rgb"]))
        assumed = "input carried no ICC profile, sRGB assumed"
    mode = {"rgb": "RGB", "cmyk": "CMYK", "gray": "L"}[to]
    base = src.convert("RGB") if src.mode == "P" else src
    img = ImageCms.profileToProfile(base, src_prof, ImageCms.getOpenProfile(str(dst)), outputMode=mode)
    name = ImageCms.getProfileDescription(ImageCms.getOpenProfile(str(dst))).strip()
    note = f"converted to {to.upper()} through {name}"
    if assumed:
        note += f" ({assumed})"
    return img, note


def _act_deskew(src, p):
    try:
        import cv2
        import numpy as np
    except Exception:
        raise ValueError("deskew needs the cv tier: pip install -r requirements-cv.txt")
    # OpenCV measures the angle; Pillow does the rotation, so EXIF, the profile
    # and the palette survive it.
    grey = np.array(src.convert("L"))
    inv = cv2.bitwise_not(grey)
    coords = np.column_stack(np.where(inv > 0))
    if coords.size == 0:
        raise ValueError("nothing to measure an angle from")
    angle = cv2.minAreaRect(coords.astype("float32"))[-1]
    angle = -(90 + angle) if angle < -45 else -angle
    f = Image.NEAREST if src.mode == "P" else Image.BICUBIC
    return src.rotate(angle, resample=f, expand=True), f"deskewed by {angle:.2f}°"


def _act_pdf_pages(path: Path, out: Path, p) -> dict:
    if pikepdf is None:
        return _err("pdf_pages needs the pdf tier: pip install -r requirements-pdf.txt")
    pages = p.get("pages")
    deg = p.get("degrees")
    with pikepdf.open(str(path)) as pdf:
        total = len(pdf.pages)
        if pages:
            want = [int(n) for n in pages]
            bad = [n for n in want if n < 1 or n > total]
            if bad:
                return _err(f"pages {bad} out of range: the file has {total}")
            new = pikepdf.Pdf.new()
            for n in want:
                new.pages.append(pdf.pages[n - 1])
            target = new
        else:
            target = pdf
        if deg:
            for pg in target.pages:
                pg.rotate(int(deg), relative=True)
        out.parent.mkdir(parents=True, exist_ok=True)
        target.save(str(out))
        kept = len(target.pages)
    what = f"wrote {kept} of {total} page(s)"
    if deg:
        what += f", rotated {int(deg)}°"
    return _ok(what, {"out": str(out), "pages": kept, "pages_in": total})


def _probe_pdf(path: Path) -> dict:
    if pikepdf is None:
        return _err("probing a PDF needs the pdf tier: pip install -r requirements-pdf.txt")
    with pikepdf.open(str(path)) as pdf:
        sizes = []
        for pg in pdf.pages:
            mb = pg.get("/MediaBox")
            if mb is not None:
                sizes.append([round(float(v), 1) for v in mb])
        return _ok(f"{len(pdf.pages)} page(s)",
                   {"path": str(path), "pages": len(pdf.pages),
                    "media_boxes": sizes[:8], "bytes": path.stat().st_size})


# ── dispatch ──────────────────────────────────────────────────────────────────

_WRITES = {"resize", "crop", "rotate", "colour", "profile", "deskew", "pdf_pages"}


def _do(tool: str, params: dict) -> dict:
    if tool != "image_edit":
        return _err(f"unknown tool {tool!r}")
    if Image is None:
        return _err("illustrator needs its base tier: pip install -r requirements.txt")

    ws = _ws()
    if ws is None:
        return _err("KAIJU_WORKSPACE is not set, so no path can be checked — start the "
                    "plugin host with it set and nothing outside it is reachable")

    p = params or {}
    action = str(p.get("action", "")).strip()
    if action not in _ACTIONS:
        return _err(f"action must be one of {', '.join(_ACTIONS)}")

    try:
        path = _resolve(ws, p.get("path", ""), must_exist=True)
        out = None
        if action in _WRITES:
            if not p.get("out"):
                return _err(f"{action} writes a new file, so `out` is required — the input is never modified")
            out = _resolve(ws, p["out"], must_exist=False)
            if out == path:
                return _err("`out` is the input: this never writes over the file it read")
    except ValueError as e:
        return _err(str(e))

    is_pdf = path.suffix.lower() == ".pdf"

    try:
        if action == "pdf_pages":
            if not is_pdf:
                return _err("pdf_pages needs a .pdf")
            return _act_pdf_pages(path, out, p)

        if is_pdf:
            if action == "probe":
                return _probe_pdf(path)
            return _err(f"{action} works on images; for a PDF use pdf_pages, or render it first")

        with Image.open(path) as src:
            src.load()
            if action == "probe":
                return _ok(f"{src.width}×{src.height} {src.mode}", _facts(src, path))

            if action == "resize":
                img, what = _act_resize(src, p)
            elif action == "crop":
                img, what = _act_crop(src, p)
            elif action == "rotate":
                img, what = _act_rotate(src, p)
            elif action == "colour":
                img, what = _act_colour(src, p)
            elif action == "profile":
                img, what = _act_profile(src, p, ws)
            elif action == "deskew":
                img, what = _act_deskew(src, p)
            else:
                return _err(f"{action} is not implemented")

            saved = _save(img, out, src)

        with Image.open(out) as check:
            check.load()
            facts = _facts(check, out)

        survived = []
        if facts["has_exif"]:
            survived.append("exif")
        if facts["has_icc"]:
            survived.append("icc_profile")
        if facts["dpi"]:
            survived.append("dpi")
        lost = [k for k in saved["offered"] if k not in survived]

        notes = list(saved["notes"])
        if lost:
            notes.append(f"{out.suffix.lstrip('.') or 'this format'} did not keep {', '.join(lost)}")
        content = what + (". " + "; ".join(notes) if notes else "")
        facts.update({"out": str(out), "carried": survived, "lost": lost, "did": what})
        return _ok(content, facts)

    except ValueError as e:
        return _err(str(e), {"path": str(path)})
    except Exception as e:
        return _err(f"{type(e).__name__}: {e}", {"path": str(path)})


async def invoke(tool: str, params: dict) -> dict:
    # Decoding and resampling are CPU-bound, and the host calls a sync invoke on
    # its own event loop — so this runs in a thread and the host keeps serving.
    return await asyncio.to_thread(_do, tool, params or {})
