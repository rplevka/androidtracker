#!/usr/bin/env python3
"""Generate the isometric login wallpaper used by the admin frontend."""

from pathlib import Path
import random

random.seed(20261119)
WIDTH, HEIGHT = 1600, 1000
OUT = Path(__file__).resolve().parent.parent / "web" / "login-wallpaper.svg"

PALETTES = [
    ("#2d4b63", "#223b50", "#1a2d3f", "#79c7d8"),
    ("#3b536b", "#2c4055", "#213244", "#e8b86a"),
    ("#345b5b", "#284848", "#1d3738", "#7ee0b0"),
    ("#5a4b63", "#463a4f", "#332b3b", "#f08d7a"),
    ("#4c566b", "#3a4356", "#2b3243", "#9db7e8"),
]


def poly(points, fill, opacity=1.0, extra=""):
    pts = " ".join(f"{x:.1f},{y:.1f}" for x, y in points)
    return f'<polygon points="{pts}" fill="{fill}" opacity="{opacity}" {extra}/>'


def building(cx, cy, half, height, palette, windows=True):
    top, right, left, accent = palette
    shadow = f'<ellipse cx="{cx}" cy="{cy + half * 0.15}" rx="{half * 1.2}" ry="{half * 0.55}" fill="#020812" opacity=".38"/>'
    top_points = [
        (cx, cy - height - half),
        (cx + half, cy - height),
        (cx, cy - height + half),
        (cx - half, cy - height),
    ]
    right_points = [
        (cx, cy - height + half),
        (cx + half, cy - height),
        (cx + half, cy),
        (cx, cy + half),
    ]
    left_points = [
        (cx - half, cy - height),
        (cx, cy - height + half),
        (cx, cy + half),
        (cx - half, cy),
    ]
    parts = [shadow, poly(right_points, right), poly(left_points, left), poly(top_points, top)]
    if height > 70:
        parts.append(
            poly(
                [
                    (cx - half * 0.72, cy - height * 0.84),
                    (cx - half * 0.18, cy - height * 0.62),
                    (cx - half * 0.18, cy - height * 0.55),
                    (cx - half * 0.72, cy - height * 0.77),
                ],
                accent,
                0.7,
            )
        )
    if windows:
        for index in range(2 if height < 110 else 3):
            y = cy - height * (0.22 + index * 0.2)
            parts.append(
                poly(
                    [
                        (cx + half * 0.2, y - half * 0.05),
                        (cx + half * 0.62, y - half * 0.26),
                        (cx + half * 0.62, y - half * 0.19),
                        (cx + half * 0.2, y + half * 0.02),
                    ],
                    accent,
                    0.55,
                )
            )
    return "".join(parts)


def tree(cx, cy, scale=1.0):
    parts = [f'<ellipse cx="{cx}" cy="{cy + 8 * scale}" rx="{20 * scale}" ry="{9 * scale}" fill="#020812" opacity=".35"/>']
    parts.append(f'<rect x="{cx - 2.5 * scale}" y="{cy - 24 * scale}" width="{5 * scale}" height="{26 * scale}" rx="2" fill="#5b4632"/>')
    parts.append(poly([(cx, cy - 58 * scale), (cx + 22 * scale, cy - 24 * scale), (cx - 22 * scale, cy - 24 * scale)], "#2c7d6b"))
    parts.append(poly([(cx, cy - 43 * scale), (cx + 18 * scale, cy - 12 * scale), (cx - 18 * scale, cy - 12 * scale)], "#3c9c7f"))
    parts.append(poly([(cx, cy - 58 * scale), (cx + 10 * scale, cy - 36 * scale), (cx - 10 * scale, cy - 36 * scale)], "#57b48e", 0.85))
    return "".join(parts)


def car(cx, cy, color):
    return (
        f'<g transform="translate({cx} {cy})">'
        f'<ellipse cx="0" cy="6" rx="19" ry="8" fill="#020812" opacity=".35"/>'
        f'<polygon points="0,-8 16,0 0,8 -16,0" fill="{color}"/>'
        f'<polygon points="0,-3 7,0 0,3 -7,0" fill="#dff7ff" opacity=".75"/>'
        f"</g>"
    )


buildings = [
    (1220, 470, 52, 150, 0),
    (1360, 540, 40, 100, 1),
    (1120, 610, 36, 82, 2),
    (1460, 650, 46, 126, 3),
    (1280, 730, 34, 64, 4),
    (1040, 760, 44, 104, 1),
    (910, 690, 30, 54, 2),
    (790, 820, 48, 118, 0),
    (650, 860, 34, 70, 3),
    (470, 760, 42, 92, 1),
    (300, 700, 34, 62, 2),
    (150, 820, 44, 108, 4),
    (1110, 280, 38, 82, 2),
    (1260, 220, 30, 54, 1),
    (1430, 300, 44, 112, 0),
    (1470, 430, 28, 48, 2),
    (720, 340, 30, 56, 3),
    (530, 430, 26, 42, 4),
]
trees = [
    (1010, 560, 0.8), (1090, 690, 0.7), (1170, 780, 0.9), (1380, 780, 0.8),
    (1510, 520, 0.7), (980, 310, 0.6), (1330, 340, 0.7), (1550, 720, 0.9),
    (850, 900, 0.8), (560, 820, 0.7), (390, 840, 0.9), (230, 730, 0.6),
    (690, 560, 0.6), (610, 980, 0.8), (1190, 180, 0.6),
]
cars = [(600, 520, "#e7b34f"), (960, 520, "#7ed4ef"), (1230, 600, "#f08d7a")]

building_parts = [building(cx, cy, half, height, PALETTES[palette]) for cx, cy, half, height, palette in buildings]
tree_parts = [tree(cx, cy, scale) for cx, cy, scale in trees]
scene = []
for item in sorted(zip(buildings, building_parts), key=lambda pair: pair[0][1]):
    scene.append(item[1])
for item in sorted(zip(trees, tree_parts), key=lambda pair: pair[0][1]):
    scene.append(item[1])

svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {WIDTH} {HEIGHT}" width="{WIDTH}" height="{HEIGHT}" role="img" aria-label="Isometric city map with roads, buildings and a glowing GPS breadcrumb trail">
  <defs>
    <linearGradient id="sky" x1="0" y1="0" x2="0.8" y2="1">
      <stop offset="0" stop-color="#071323"/>
      <stop offset="0.52" stop-color="#0b2035"/>
      <stop offset="1" stop-color="#10293a"/>
    </linearGradient>
    <radialGradient id="glow" cx="0.72" cy="0.38" r="0.7">
      <stop offset="0" stop-color="#2aa9a0" stop-opacity=".25"/>
      <stop offset="0.55" stop-color="#17627a" stop-opacity=".08"/>
      <stop offset="1" stop-color="#071323" stop-opacity="0"/>
    </radialGradient>
    <linearGradient id="river" x1="0" y1="0" x2="1" y2="0">
      <stop offset="0" stop-color="#0d3b55"/>
      <stop offset="0.5" stop-color="#125c77"/>
      <stop offset="1" stop-color="#0d3b55"/>
    </linearGradient>
    <pattern id="isogrid" width="64" height="32" patternUnits="userSpaceOnUse">
      <path d="M0 16 L32 0 L64 16 L32 32 Z" fill="none" stroke="#1b3a51" stroke-width="1" opacity=".55"/>
    </pattern>
    <filter id="pathGlow" x="-30%" y="-30%" width="160%" height="160%">
      <feGaussianBlur stdDeviation="8" result="blur"/>
      <feMerge><feMergeNode in="blur"/><feMergeNode in="SourceGraphic"/></feMerge>
    </filter>
    <filter id="softGlow" x="-60%" y="-60%" width="220%" height="220%">
      <feGaussianBlur stdDeviation="5" result="blur"/>
      <feMerge><feMergeNode in="blur"/><feMergeNode in="SourceGraphic"/></feMerge>
    </filter>
    <linearGradient id="vignette" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#020814" stop-opacity=".15"/>
      <stop offset=".7" stop-color="#020814" stop-opacity=".2"/>
      <stop offset="1" stop-color="#020814" stop-opacity=".72"/>
    </linearGradient>
  </defs>
  <rect width="{WIDTH}" height="{HEIGHT}" fill="url(#sky)"/>
  <rect width="{WIDTH}" height="{HEIGHT}" fill="url(#glow)"/>
  <g opacity=".55">
    <path d="M -120 820 C 260 640 500 690 760 780 C 1030 875 1260 860 1720 610" fill="none" stroke="url(#river)" stroke-width="92" stroke-linecap="round"/>
    <path d="M -120 805 C 260 625 500 675 760 765 C 1030 860 1260 845 1720 595" fill="none" stroke="#4a9cb5" stroke-width="2" opacity=".35"/>
    <path d="M -120 840 C 260 660 500 710 760 800 C 1030 895 1260 880 1720 630" fill="none" stroke="#04131f" stroke-width="3" opacity=".45"/>
  </g>
  <polygon points="-120,430 800,900 1720,430 800,-40" fill="url(#isogrid)" opacity=".7"/>
  <g stroke-linecap="round" stroke-linejoin="round">
    <path d="M -180 720 L 720 270 L 1620 720" fill="none" stroke="#06111e" stroke-width="86" opacity=".6"/>
    <path d="M -180 720 L 720 270 L 1620 720" fill="none" stroke="#233c54" stroke-width="70"/>
    <path d="M -180 720 L 720 270 L 1620 720" fill="none" stroke="#365a74" stroke-width="4" opacity=".5"/>
    <path d="M 260 1030 L 880 720 L 1500 1030" fill="none" stroke="#06111e" stroke-width="76" opacity=".55"/>
    <path d="M 260 1030 L 880 720 L 1500 1030" fill="none" stroke="#1e354b" stroke-width="60"/>
    <path d="M 1040 -80 L 780 50 L 1040 180 L 1300 50 Z" fill="none" stroke="#06111e" stroke-width="70" opacity=".55"/>
    <path d="M 1040 -80 L 780 50 L 1040 180 L 1300 50 Z" fill="none" stroke="#1e354b" stroke-width="56"/>
    <path d="M -180 720 L 720 270 L 1620 720" fill="none" stroke="#8fc4d8" stroke-width="3" stroke-dasharray="18 26" opacity=".5"/>
    <path d="M 260 1030 L 880 720 L 1500 1030" fill="none" stroke="#8fc4d8" stroke-width="3" stroke-dasharray="18 26" opacity=".45"/>
  </g>
  <g opacity=".9">
    <path d="M 470 880 L 560 835 L 650 880 L 560 925 Z" fill="#18324a" stroke="#2d526c" stroke-width="2"/>
    <path d="M 470 880 L 560 925 L 650 880" fill="none" stroke="#3b6c85" stroke-width="3"/>
    <path d="M 480 882 L 560 922" stroke="#4a91a8" stroke-width="2" opacity=".6"/>
  </g>
  {''.join(scene)}
  {''.join(car(*item) for item in cars)}
  <g filter="url(#pathGlow)" fill="none" stroke-linecap="round" stroke-linejoin="round">
    <path d="M 150 760 L 330 670 L 500 590 L 690 500 L 860 545 L 1030 625 L 1195 555 L 1360 470 L 1510 520"
          stroke="#1a6f72" stroke-width="18" opacity=".5"/>
    <path d="M 150 760 L 330 670 L 500 590 L 690 500 L 860 545 L 1030 625 L 1195 555 L 1360 470 L 1510 520"
          stroke="#56e2c0" stroke-width="5" stroke-dasharray="2 18"/>
    <circle cx="150" cy="760" r="9" fill="#0b2431" stroke="#56e2c0" stroke-width="4"/>
    <circle cx="330" cy="670" r="5" fill="#56e2c0"/>
    <circle cx="500" cy="590" r="5" fill="#56e2c0"/>
    <circle cx="690" cy="500" r="5" fill="#56e2c0"/>
    <circle cx="860" cy="545" r="5" fill="#56e2c0"/>
    <circle cx="1030" cy="625" r="5" fill="#56e2c0"/>
    <circle cx="1195" cy="555" r="5" fill="#56e2c0"/>
    <circle cx="1360" cy="470" r="5" fill="#56e2c0"/>
  </g>
  <g filter="url(#softGlow)">
    <circle cx="1510" cy="520" r="26" fill="#56e2c0" opacity=".12"/>
    <circle cx="1510" cy="520" r="15" fill="#56e2c0" opacity=".25"/>
    <circle cx="1510" cy="520" r="8" fill="#d8fff2" stroke="#56e2c0" stroke-width="3"/>
    <circle cx="1510" cy="520" r="48" fill="none" stroke="#56e2c0" stroke-width="2" opacity=".35"/>
  </g>
  <rect width="{WIDTH}" height="{HEIGHT}" fill="url(#vignette)"/>
</svg>
'''

OUT.write_text(svg)
print(f"wrote {OUT} ({len(svg)} bytes)")
