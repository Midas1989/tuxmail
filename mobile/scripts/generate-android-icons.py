#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# Generates the Android launcher icons, splash icon and store icon from the
# brand mascot (assets/branding/tuxmail-penguin.png). Maintainer tool - the
# generated PNGs are committed. Requires Pillow:  python3 scripts/generate-android-icons.py
#
#  mipmap-*/ic_launcher_foreground.png   adaptive icon foreground (108 dp, penguin in the 66 dp safe zone)
#  mipmap-*/ic_launcher_monochrome.png   Android 13+ themed icon (alpha mask of the dark outline/body)
#  mipmap-*/ic_launcher.png, _round.png  legacy icons (Android 7.x) on the brand background #f3f6fa
#  drawable-*/splash_icon.png            Android 12+ splash screen icon (288 dp canvas, 192 dp visible)
#  fastlane/metadata/android/*/images/icon.png  512 px store icon

from pathlib import Path
from PIL import Image, ImageDraw

HERE = Path(__file__).resolve().parent
MOBILE = HERE.parent
REPO = MOBILE.parent
RES = MOBILE / 'android' / 'app' / 'src' / 'main' / 'res'
SRC = REPO / 'assets' / 'branding' / 'tuxmail-penguin.png'
BG = (0xF3, 0xF6, 0xFA, 255)
DENSITIES = {'mdpi': 1.0, 'hdpi': 1.5, 'xhdpi': 2.0, 'xxhdpi': 3.0, 'xxxhdpi': 4.0}


def fit(img: Image.Image, box: int) -> Image.Image:
    """Scale to fit a box x box square, keeping the aspect ratio."""
    w, h = img.size
    s = box / max(w, h)
    return img.resize((max(1, round(w * s)), max(1, round(h * s))), Image.LANCZOS)


def centered(canvas_px: int, content: Image.Image, bg=(0, 0, 0, 0)) -> Image.Image:
    out = Image.new('RGBA', (canvas_px, canvas_px), bg)
    x = (canvas_px - content.width) // 2
    y = (canvas_px - content.height) // 2
    out.alpha_composite(content, (x, y))
    return out


def monochrome_mask(img: Image.Image) -> Image.Image:
    """White glyph whose alpha keeps the dark parts (body, outline, eyes) and drops the light belly/face."""
    rgba = img.convert('RGBA')
    px = rgba.load()
    out = Image.new('RGBA', rgba.size, (255, 255, 255, 0))
    opx = out.load()
    for y in range(rgba.height):
        for x in range(rgba.width):
            r, g, b, a = px[x, y]
            if a == 0:
                continue
            lum = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255
            # dark -> opaque, light -> transparent, orange feet stay half visible
            keep = 1.0 if lum < 0.35 else 0.0 if lum > 0.62 else (0.62 - lum) / 0.27
            if r > 200 and g > 120 and b < 90:  # orange beak/feet
                keep = 0.85
            opx[x, y] = (255, 255, 255, round(a * keep))
    return out


def rounded(size: int, radius: int, fill) -> Image.Image:
    m = Image.new('RGBA', (size, size), (0, 0, 0, 0))
    ImageDraw.Draw(m).rounded_rectangle((0, 0, size - 1, size - 1), radius=radius, fill=fill)
    return m


def main() -> None:
    penguin = Image.open(SRC).convert('RGBA')
    mono = monochrome_mask(penguin)
    for name, d in DENSITIES.items():
        mip = RES / f'mipmap-{name}'
        mip.mkdir(parents=True, exist_ok=True)
        fg_px = round(108 * d)
        safe = round(62 * d)  # inside the 66 dp safe zone (circle mask)
        centered(fg_px, fit(penguin, safe)).save(mip / 'ic_launcher_foreground.png', optimize=True)
        centered(fg_px, fit(mono, safe)).save(mip / 'ic_launcher_monochrome.png', optimize=True)

        legacy = round(48 * d)
        inner = fit(penguin, round(legacy * 0.78))
        sq = rounded(legacy, round(legacy * 0.2), BG)
        sq.alpha_composite(inner, ((legacy - inner.width) // 2, (legacy - inner.height) // 2))
        sq.save(mip / 'ic_launcher.png', optimize=True)
        circle = Image.new('RGBA', (legacy, legacy), (0, 0, 0, 0))
        ImageDraw.Draw(circle).ellipse((0, 0, legacy - 1, legacy - 1), fill=BG)
        inner_r = fit(penguin, round(legacy * 0.7))
        circle.alpha_composite(inner_r, ((legacy - inner_r.width) // 2, (legacy - inner_r.height) // 2))
        circle.save(mip / 'ic_launcher_round.png', optimize=True)

        dr = RES / f'drawable-{name}'
        dr.mkdir(parents=True, exist_ok=True)
        # Splash: 288 dp canvas, only the inner 192 dp circle is shown -> keep the penguin within ~128 dp.
        centered(round(288 * d), fit(penguin, round(128 * d))).save(dr / 'splash_icon.png', optimize=True)

    store = rounded(512, 0, BG)
    pen = fit(penguin, 420)
    store.alpha_composite(pen, ((512 - pen.width) // 2, (512 - pen.height) // 2))
    for lang in ('en-US', 'de-DE'):
        img_dir = MOBILE / 'fastlane' / 'metadata' / 'android' / lang / 'images'
        img_dir.mkdir(parents=True, exist_ok=True)
        store.convert('RGB').save(img_dir / 'icon.png', optimize=True)


if __name__ == '__main__':
    main()
