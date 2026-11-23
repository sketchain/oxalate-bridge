#!/usr/bin/env python3
"""Generates the C₂O₄²⁻ logo assets from IBM Plex Mono (the self-hosted
@fontsource files under web/node_modules):

  web/src/components/logoGlyphs.gen.ts   path data of the in-app mark (SemiBold)
  web/public/favicon.svg                 32-unit favicon (Bold, tighter)
  web/public/favicon-32.png              32 × 32
  web/public/apple-touch-icon.png        180 × 180
  web/public/icons/icon-192.png          PWA icon 192 × 192
  web/public/icons/icon-512.png          PWA icon 512 × 512
  web/public/icons/maskable-512.png      PWA maskable icon (formula inside the 80 % safe zone)
  web/public/icons/badge-96.png          notification badge (white formula, transparent)

The glyphs are outlines, so the mark renders identically everywhere
(favicons cannot load web fonts). Subscripts 2 and 4 and the superscript 2−
are real scaled and shifted glyphs (the 4 and the 2− stacked, as in chemical
notation), not Unicode sub/superscript characters.

Dev-only: needs `pip install fonttools brotli pillow` and `pnpm install` in
web/. The outputs are committed; rerun after changing the layout below.
"""
import pathlib

from fontTools.pens.basePen import BasePen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont
from PIL import Image, ImageChops, ImageDraw

ROOT = pathlib.Path(__file__).resolve().parent.parent
FONTS = next((ROOT / "web/node_modules/.pnpm").glob("@fontsource+ibm-plex-mono@*/node_modules/@fontsource/ibm-plex-mono/files"))
YELLOW, INK = "#F2C200", "#0A0A0A"


def font(weight):
    return TTFont(FONTS / f"ibm-plex-mono-latin-{weight}-normal.woff2")


class Layout:
    """One design: a size × size square with a 2-unit ink border, the formula and a 3-unit bar."""

    def __init__(self, size, weight, em, tracking, sub_scale, bar_gap, bar_h, inset):
        self.size, self.weight, self.em, self.tracking = size, weight, em, tracking
        self.sub_scale, self.bar_gap, self.bar_h, self.inset = sub_scale, bar_gap, bar_h, inset

    def runs(self):
        """(char, scale, dx_em, dy_em) of each glyph: x advances in em of the run's scale."""
        s = self.sub_scale
        sub_dy, sup_dy = 0.30, -0.42  # baseline shifts, in em of the main size
        return [("C", 1, 0), ("2", s, sub_dy), ("O", 1, 0), ("4", s, sub_dy), ("2", s, sup_dy), ("−", s, sup_dy)]

    def glyphs(self):
        f = font(self.weight)
        cmap, gs, upm = f.getBestCmap(), f.getGlyphSet(), f["head"].unitsPerEm
        cap = f["OS/2"].sCapHeight / upm
        scale = self.em / upm
        placed, x = [], 0.0
        stack_x = None
        for i, (ch, s, dy) in enumerate(self.runs()):
            name = cmap[ord(ch)]
            adv = gs[name].width * scale * s
            gx = x
            if i == 4:  # superscript 2 sits above the subscript 4 (stacked)
                gx = stack_x
            if i == 3:
                stack_x = x
            placed.append((name, gx, dy * self.em, scale * s))
            if i in (0, 1, 2):
                x += adv + self.tracking
            elif i == 4:
                x = gx + adv + self.tracking * 0.3
            elif i == 5:
                x = x + adv
        width = x
        # Vertical extent: superscript top .. subscript baseline.
        top = -(0.42 * self.em) - cap * self.em * self.sub_scale
        bottom = 0.30 * self.em
        height = bottom - top + self.bar_gap + self.bar_h
        inner = self.size - 2 * 2  # inside the 2-unit border
        ox = 2 + (inner - width) / 2
        baseline = 2 + (inner - height) / 2 - top
        out = []
        for name, gx, gy, sc in placed:
            out.append((gs[name], ox + gx, baseline + gy, sc))
        bar = (ox, baseline + bottom + self.bar_gap, width, self.bar_h)
        return out, bar

    def path(self):
        glyphs, bar = self.glyphs()
        pen = SVGPathPen(None, ntos=lambda v: f"{v:.2f}".rstrip("0").rstrip("."))
        for g, x, y, sc in glyphs:
            g.draw(TransformPen(pen, (sc, 0, 0, -sc, x, y)))
        return pen.getCommands(), bar

    def svg(self):
        d, (bx, by, bw, bh) = self.path()
        s = self.size
        return (
            f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {s} {s}">'
            f'<rect x="1" y="1" width="{s - 2}" height="{s - 2}" fill="{YELLOW}" stroke="{INK}" stroke-width="2"/>'
            f'<path fill="{INK}" d="{d}"/>'
            f'<rect x="{bx:.2f}" y="{by:.2f}" width="{bw:.2f}" height="{bh}" fill="{INK}"/></svg>\n'
        )


class PolyPen(BasePen):
    """Flattens outlines into polygons (for the PNG raster)."""

    def __init__(self, glyphset, k):
        super().__init__(glyphset)
        self.k, self.polys, self.cur = k, [], []

    def _moveTo(self, p):
        self.cur = [p]

    def _lineTo(self, p):
        self.cur.append(p)

    def _qCurveToOne(self, p1, p2):
        p0 = self.cur[-1]
        for i in range(1, 9):
            t = i / 8
            self.cur.append(((1 - t) ** 2 * p0[0] + 2 * (1 - t) * t * p1[0] + t * t * p2[0], (1 - t) ** 2 * p0[1] + 2 * (1 - t) * t * p1[1] + t * t * p2[1]))

    def _curveToOne(self, p1, p2, p3):
        p0 = self.cur[-1]
        for i in range(1, 9):
            t = i / 8
            u = 1 - t
            self.cur.append(tuple(u**3 * a + 3 * u * u * t * b + 3 * u * t * t * c + t**3 * d for a, b, c, d in zip(p0, p1, p2, p3)))

    def _closePath(self):
        if len(self.cur) > 2:
            self.polys.append([(x * self.k, y * self.k) for x, y in self.cur])
        self.cur = []

    _endPath = _closePath


def formula_mask(layout, W, k, off):
    """1-bit mask of the formula and the bar, the layout box scaled by k and shifted by off."""
    glyphs, (bx, by, bw, bh) = layout.glyphs()
    mask = Image.new("1", (W, W), 0)
    for g, x, y, sc in glyphs:
        pen = PolyPen(None, k)
        g.draw(TransformPen(pen, (sc, 0, 0, -sc, x, y)))
        for poly in pen.polys:  # even-odd: counters (O, 4) cut out
            m = Image.new("1", (W, W), 0)
            ImageDraw.Draw(m).polygon([(px + off, py + off) for px, py in poly], fill=1)
            mask = ImageChops.logical_xor(mask, m)
    ImageDraw.Draw(mask).rectangle([bx * k + off, by * k + off, (bx + bw) * k + off - 1, (by + bh) * k + off - 1], fill=1)
    return mask


def png(layout, px, out):
    ss = 8
    k = px * ss / layout.size
    W = px * ss
    img = Image.new("RGBA", (W, W), INK)
    d = ImageDraw.Draw(img)
    d.rectangle([2 * k, 2 * k, W - 2 * k - 1, W - 2 * k - 1], fill=YELLOW)
    img.paste(INK, mask=formula_mask(layout, W, k, 0))
    img.resize((px, px), Image.LANCZOS).convert("RGB").save(out, optimize=True)


def png_maskable(layout, px, out):
    """Full-bleed yellow with the formula (no border) in the central 80 % safe zone."""
    ss = 8
    W = px * ss
    k = W * 0.72 / layout.size
    img = Image.new("RGBA", (W, W), YELLOW)
    img.paste(INK, mask=formula_mask(layout, W, k, (W - layout.size * k) / 2))
    img.resize((px, px), Image.LANCZOS).convert("RGB").save(out, optimize=True)


def png_badge(layout, px, out):
    """White formula on transparent: Android draws notification badges from the alpha channel."""
    ss = 8
    W = px * ss
    k = W * 1.0 / layout.size
    mask = formula_mask(layout, W, k, (W - layout.size * k) / 2).convert("L")
    img = Image.new("RGBA", (W, W), (255, 255, 255, 0))
    img.putalpha(mask)
    img.resize((px, px), Image.LANCZOS).save(out, optimize=True)


# In-app mark: 36-unit design (2px border and 3px bar at the nominal 36px).
APP = Layout(size=36, weight=600, em=12.2, tracking=-0.15, sub_scale=0.62, bar_gap=2.2, bar_h=3, inset=0)
# Small icons: bolder, larger and tighter for legibility at 32px.
ICON = Layout(size=32, weight=700, em=12.0, tracking=-0.55, sub_scale=0.66, bar_gap=1.6, bar_h=3, inset=0)

if __name__ == "__main__":
    d, (bx, by, bw, bh) = APP.path()
    (ROOT / "web/src/components/logoGlyphs.gen.ts").write_text(
        "// Generated by scripts/gen-logo.py from IBM Plex Mono SemiBold. Do not edit.\n"
        "// The C₂O₄²⁻ formula as outlines in a 36 × 36 box (2-unit border inside).\n"
        f'export const LOGO_GLYPHS = "{d}";\n'
        f"export const LOGO_BAR = {{ x: {bx:.2f}, y: {by:.2f}, width: {bw:.2f}, height: {bh} }};\n"
    )
    (ROOT / "web/public/favicon.svg").write_text(ICON.svg())
    png(ICON, 32, ROOT / "web/public/favicon-32.png")
    png(APP, 180, ROOT / "web/public/apple-touch-icon.png")
    icons = ROOT / "web/public/icons"
    icons.mkdir(exist_ok=True)
    png(APP, 192, icons / "icon-192.png")
    png(APP, 512, icons / "icon-512.png")
    png_maskable(APP, 512, icons / "maskable-512.png")
    png_badge(ICON, 96, icons / "badge-96.png")
    print("logo assets written")
