## Planning Guidance

Probe before editing. The action that is correct depends on the colour mode, the
dpi and whether the file carries a palette, and `probe` is the only thing that
reports them. Reading those facts out of a filename or an earlier answer is a
guess.

Every action but `probe` writes a new file and requires `out`. The input is never
modified, so a bad pass costs nothing and there is no need to copy first.

One file per call, by path. The tool takes one path, not a list and not a
command's output.

For print, do geometry first and colour last: resize or crop, then `profile` to
CMYK. Converting first and resizing afterwards resamples values that were already
mapped to the press.

Check the result by probing the output. `status: ok` says the call ran; the
output's own mode, size and `carried` say whether it did what was wanted.

### What it reports about metadata

Every write returns `carried` and `lost` — which of EXIF, the ICC profile and the
dpi survived into the output. These are read back off the file that was written,
not assumed. A format that cannot hold one of them says so: TIFF drops EXIF,
JPEG holds no alpha or palette, PNG holds no CMYK.

### A palette image

A palette image has no colour to correct — its pixels are indices. Geometry keeps
the palette. A colour edit converts to RGB and the palette is gone, which the
result says plainly; pass `keep_palette` to requantise back to it, which is lossy
in its own way. Decide which before planning the step, because the answer depends
on whether the palette or the colour matters more.
