"""Generates Odin's logo: the Allfather's winged war helm, one gold eye, the other scarred
(the eye he gave for wisdom), and a braided beard bound with a gold clasp. Symmetric, built
on a 128-unit grid.

    python3 tools/logo/generate.py      # writes docs/assets/{logo,logo-dark,favicon}.svg
"""
import os

W = 128
def m(pts): return [(W - x, y) for x, y in pts]
def poly(pts): return "M" + " L".join(f"{x:g} {y:g}" for x, y in pts) + " Z"
def both(pts): return poly(pts) + " " + poly(list(reversed(m(pts))))
def line(pts): return "M" + " L".join(f"{x:g} {y:g}" for x, y in pts)

feathers = [
    [(41, 27), (31, 17), (17, 8), (4, 2), (9, 12), (21, 24), (35, 34)],
    [(39, 34), (26, 25), (12, 18), (1, 15), (9, 24), (23, 32), (36, 40)],
    [(38, 41), (24, 35), (10, 31), (2, 30), (11, 38), (25, 43), (36, 47)],
    [(38, 47), (26, 45), (14, 44), (8, 46), (17, 50), (28, 52), (37, 53)],
]
helm = "M38 52 L38 42 C38 26 50 16 64 16 C78 16 90 26 90 42 L90 52 Z"
cheek = [(36, 48), (43, 48), (43, 74), (36, 69)]
guard = poly([(40, 52), (88, 52), (88, 70), (84, 76), (64, 80), (44, 76), (40, 70)])
beard_l = [(36, 69), (43, 74), (50, 78), (57, 77), (64, 79), (64, 125), (52, 112), (42, 97), (36, 82)]
cuts = [
    line([(39, 46), (89, 46)]),                         # brow band
    line([(64, 17), (64, 45)]),                         # helm ridge
    line([(44, 85), (54, 81), (64, 86), (74, 81), (84, 85)]),  # moustache
    line([(59, 91), (69, 91)]),                         # mouth
    line([(64, 96), (64, 106)]),                        # beard split
    line([(43, 90), (50, 104)]), line([(51, 92), (56, 104)]),
    line([(85, 90), (78, 104)]), line([(77, 92), (72, 104)]),
    line([(69, 37), (80, 64)]),                         # the lost eye: a scar through band and guard
    line([(41, 63), (46, 67), (58, 67), (61, 77)]),     # eye guard and nasal
    line([(87, 63), (82, 67), (70, 67), (67, 77)]),
]
rivets = [(44, 49), (54, 49), (74, 49), (84, 49)]
eye_hole = poly([(44, 59), (51, 55.6), (58, 59.2), (51, 63)])
gold_eye = poly([(45.8, 59.1), (51, 56.6), (56.4, 59.3), (51, 62)])
rings = [poly([(52.6, 110.4), (64, 116.4), (75.4, 110.4), (77.4, 114.6), (64, 121.4), (50.6, 114.6)])]

def svg(fill="#1c1f26", gold="#d4a53a", size=512, tile=None):
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {W}" width="{size}" height="{size}">
  <title>Odin</title>
  <defs>
    <mask id="odin-cut">
      <rect width="{W}" height="{W}" fill="#fff"/>
      <g fill="none" stroke="#000" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
        {"".join(f'<path d="{c}"/>' for c in cuts)}
      </g>
      <g fill="#000">{"".join(f'<circle cx="{x}" cy="{y}" r="1.25"/>' for x, y in rivets)}<path d="{eye_hole}"/></g>
    </mask>
  </defs>
  {f'<rect width="{W}" height="{W}" rx="24" fill="{tile}"/>' if tile else ''}
  <g fill="{fill}" mask="url(#odin-cut)"{' transform="translate(12 10) scale(0.81)"' if tile else ''}>
    <path d="{" ".join(both(f) for f in feathers)}"/>
    <path d="{helm}"/>
    <path d="{both(cheek)}"/>
    <path d="{guard}"/>
    <path d="{both(beard_l)}"/>
  </g>
  <g fill="{gold}"{' transform="translate(12 10) scale(0.81)"' if tile else ''}><path d="{gold_eye}"/>{"".join(f'<path d="{r}"/>' for r in rings)}</g>
</svg>'''


IRON, BONE, GOLD = "#1c1f26", "#ece8df", "#d9a93f"

if __name__ == "__main__":
    assets = os.path.join(os.path.dirname(__file__), "..", "..", "docs", "assets")
    os.makedirs(assets, exist_ok=True)
    files = {
        "logo.svg": svg(fill=BONE, gold=GOLD),             # on dark backgrounds (site header)
        "logo-dark.svg": svg(fill=IRON, gold=GOLD),        # on light backgrounds
        "favicon.svg": svg(fill=BONE, gold=GOLD, tile=IRON),  # icon tile, readable anywhere
    }
    for name, content in files.items():
        with open(os.path.join(assets, name), "w", encoding="utf-8") as f:
            f.write(content + "\n")
        print("wrote", name)
