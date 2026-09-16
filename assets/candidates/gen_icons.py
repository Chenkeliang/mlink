#!/usr/bin/env python3
"""Generate refined pixel-style MLink README icon candidates (SVG)."""
import os

OUT = os.path.dirname(os.path.abspath(__file__))


def group(cells, color, block):
    return f'<g fill="{color}">' + "".join(
        f'<rect x="{c*block}" y="{r*block}" width="{block}" height="{block}"/>'
        for (r, c) in sorted(cells)
    ) + "</g>"


def dilate(filled):
    out = set()
    for (r, c) in filled:
        for (dr, dc) in ((1, 0), (-1, 0), (0, 1), (0, -1)):
            n = (r + dr, c + dc)
            if n not in filled:
                out.add(n)
    return out


def octagon_bg(n, cut, color, block):
    cells = set()
    for r in range(n):
        lo = cut - r if r < cut else (cut - (n - 1 - r) if r >= n - cut else 0)
        lo = max(lo, 0)
        for c in range(lo, n - lo):
            cells.add((r, c))
    return group(cells, color, block)


def write_svg(name, w, h, body, attrs=""):
    svg = (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" '
        f'viewBox="0 0 {w} {h}" shape-rendering="crispEdges" {attrs}>\n{body}\n</svg>\n'
    )
    with open(os.path.join(OUT, name), "w") as f:
        f.write(svg)
    print("wrote", name)


# ============ shared: isometric memory cube ============
def cube_parts(cx=16, r0=3, w=8):
    """True isometric cube: hexagon outline (T, L, L', B', R', R).
    Top face = diamond; side faces have vertical outer edges and stepped
    bottom edges meeting at the bottom vertex."""
    top, left, right = set(), set(), set()
    rw = r0 + w        # widest diamond row
    rb = r0 + 2 * w    # diamond bottom vertex B / side-face lower corners
    rbp = rb + w       # cube bottom vertex B'
    for i in range(2 * w + 1):
        r = r0 + i
        hw = i if i <= w else 2 * w - i
        for c in range(cx - hw, cx + hw + 1):
            top.add((r, c))
    for r in range(rw, rbp + 1):
        if r <= rb:
            for c in range(cx - w, cx - w + (r - rw) + 1):
                left.add((r, c))
            for c in range(cx + w - (r - rw), cx + w + 1):
                right.add((r, c))
        else:
            for c in range(cx - w + (r - rb), cx + 1):
                left.add((r, c))
            for c in range(cx, cx + w - (r - rb) + 1):
                right.add((r, c))
    return top, left, right


def cube_group(block):
    top, left, right = cube_parts()
    filled = top | left | right
    outline = dilate(filled)
    parts = [group(outline, "#04141D", block)]
    parts.append(group(left, "#2FA8D8", block))
    parts.append(group(right, "#1B6E9E", block))
    parts.append(group(top, "#7DF0FF", block))
    # bottom-edge shading on side faces
    parts.append(group({(r, c) for (r, c) in left if (r + 1, c) not in filled}, "#248BB4", block))
    parts.append(group({(r, c) for (r, c) in right if (r + 1, c) not in filled}, "#155980", block))
    # top-face lower edge shadow where top meets sides
    parts.append(group({(r, c) for (r, c) in top if (r + 1, c) in left or (r + 1, c) in right},
                       "#3FC3EA", block))
    # amber core window on left face
    core = {(21, 11), (21, 12), (22, 11), (22, 12)}
    parts.append(group(core, "#FFC53D", block))
    parts.append(group({(21, 11)}, "#FFE29A", block))
    # sparkle on top face
    parts.append(group({(4, 16), (5, 15)}, "#DFFBFF", block))
    return "".join(parts)


def orbit_group(block):
    """Three agent nodes with dotted trails converging on the cube."""
    parts = []
    nodes = [
        ((3, 3), "#FFC53D", "#7A5A17", [(5, 5), (7, 7), (9, 9)]),
        ((3, 26), "#4DE3FF", "#1B6E7E", [(5, 24), (7, 22), (9, 20)]),
        ((25, 26), "#FF6EC7", "#8F3A6E", [(23, 24), (21, 22)]),
    ]
    for ((r, c), col, dim, trail) in nodes:
        sq = {(r + dr, c + dc) for dr in (0, 1) for dc in (0, 1)}
        parts.append(group(dilate(sq), "#04141D", block))
        parts.append(group(sq, col, block))
        parts.append(group({(r, c)}, "#FFFFFF", block))
        parts.append(group(set(trail), dim, block))
    return "".join(parts)


STARS = [(2, 21), (6, 29), (11, 2), (21, 3), (27, 9), (29, 18), (25, 30), (2, 13), (14, 30)]

# ============ A. memory cube mark (32x32, block 8 -> 256) ============
parts_a = [octagon_bg(32, 3, "#0A101C", 8)]
parts_a.append(group(set(STARS), "#16233A", 8))
parts_a.append(cube_group(8))
parts_a.append(orbit_group(8))
write_svg("mlink-mark-a-cube.svg", 256, 256, "\n".join(parts_a),
          'role="img" aria-label="MLink memory cube mark"')

# ============ B. interlocked chain links mark (32x32, block 8) ============
def ring(r0, c0, outer=14, hole=8):
    o = {(r, c) for r in range(r0, r0 + outer) for c in range(c0, c0 + outer)}
    # chamfer outer corners (2-cell step) for a rounded link silhouette
    for (dr, dc) in ((0, 0), (0, 1), (1, 0),
                     (0, outer - 1), (0, outer - 2), (1, outer - 1),
                     (outer - 1, 0), (outer - 2, 0), (outer - 1, 1),
                     (outer - 1, outer - 1), (outer - 2, outer - 1), (outer - 1, outer - 2)):
        o.discard((r0 + dr, c0 + dc))
    h_off = (outer - hole) // 2
    h = {(r, c) for r in range(r0 + h_off, r0 + h_off + hole)
         for c in range(c0 + h_off, c0 + h_off + hole)}
    # chamfer inner corners (1 cell) so the hole echoes the outer shape
    for (dr, dc) in ((0, 0), (0, hole - 1), (hole - 1, 0), (hole - 1, hole - 1)):
        h.discard((r0 + h_off + dr, c0 + h_off + dc))
    return o - h, o


ring_a, outer_a = ring(6, 4)    # amber
ring_b, outer_b = ring(12, 14)  # cyan


def shade(cells, r0, c0, size, base, light, dark):
    out = {}
    for (r, c) in cells:
        if r == r0 or c == c0:
            out[(r, c)] = light
        elif r == r0 + size - 1 or c == c0 + size - 1:
            out[(r, c)] = dark
        else:
            out[(r, c)] = base
    return out


def group_map(mapping, block):
    by_color = {}
    for cell, col in mapping.items():
        by_color.setdefault(col, set()).add(cell)
    return "".join(group(cells, col, block) for col, cells in by_color.items())


parts_b = [octagon_bg(32, 3, "#0A0F1A", 8)]
parts_b.append(group(set(STARS), "#152134", 8))
# A under
parts_b.append(group(dilate(ring_a), "#2E1703", 8))
parts_b.append(group_map(shade(ring_a, 6, 4, 14, "#F59E0B", "#FFD166", "#B45309"), 8))
# B over
parts_b.append(group(dilate(ring_b), "#06222E", 8))
parts_b.append(group_map(shade(ring_b, 12, 14, 14, "#22B8E8", "#7DF0FF", "#0E7490"), 8))
# interlock: A's bottom-right corner passes over B
patch = {(r, c) for (r, c) in ring_a if r >= 17 and c >= 14}
parts_b.append(group_map(shade(patch, 6, 4, 14, "#F59E0B", "#FFD166", "#B45309"), 8))
write_svg("mlink-mark-b-links.svg", 256, 256, "\n".join(parts_b),
          'role="img" aria-label="MLink interlocked links mark"')

# ============ C. hero banner (1000x280): cube + refined wordmark ============
FONT = {
    "M": ["#...#", "##.##", "#.#.#", "#.#.#", "#...#", "#...#", "#...#"],
    "L": ["#....", "#....", "#....", "#....", "#....", "#....", "#####"],
    "I": [".###.", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."],
    "N": ["#...#", "##..#", "##..#", "#.#.#", "#..##", "#..##", "#...#"],
    "K": ["#...#", "#..#.", "#.#..", "##...", "#.#..", "#..#.", "#...#"],
}


def wordmark(word, ox, oy, block, colors_map, shadow=None):
    parts = []
    if shadow:
        x = ox + shadow[0]
        for ch in word:
            for r, row in enumerate(FONT[ch]):
                for c, cell in enumerate(row):
                    if cell == "#":
                        parts.append(f'<rect x="{x + c*block}" y="{oy + shadow[1] + r*block}" '
                                     f'width="{block}" height="{block}" fill="{shadow[2]}"/>')
            x += 7 * block
    x = ox
    for ch in word:
        for r, row in enumerate(FONT[ch]):
            for c, cell in enumerate(row):
                if cell == "#":
                    parts.append(f'<rect x="{x + c*block}" y="{oy + r*block}" '
                                 f'width="{block}" height="{block}" fill="{colors_map[ch]}"/>')
        x += 7 * block
    return "".join(parts)


parts_c = ['<rect width="1000" height="280" fill="#0A101C"/>']
# faint memory-plane dot grid on the right
dots = []
for y in range(48, 250, 40):
    for x in range(660, 990, 40):
        dots.append(f'<rect x="{x}" y="{y}" width="4" height="4"/>')
parts_c.append(f'<g fill="#152238">{"".join(dots)}</g>')
# edge bars
parts_c.append('<g fill="#22324A"><rect x="0" y="0" width="1000" height="4"/>'
               '<rect x="0" y="276" width="1000" height="4"/></g>')
# cube mark (block 5)
parts_c.append(f'<g transform="translate(60 60)">{cube_group(5)}{orbit_group(5)}</g>')
# wordmark with drop shadow
colors_c = {"M": "#F3F7FC", "L": "#F3F7FC", "I": "#FFC53D", "N": "#F3F7FC", "K": "#F3F7FC"}
parts_c.append(wordmark("MLINK", 220, 62, 12, colors_c, shadow=(5, 5, "#16233A")))
# amber square bullet + tagline
parts_c.append('<g fill="#FFC53D"><rect x="220" y="196" width="12" height="12"/></g>')
parts_c.append('<text x="244" y="208" fill="#FFC53D" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" '
               'font-size="17" letter-spacing="2">ONE MEMORY PLANE · YOUR MODELS STAY YOURS</text>')
# cyan data dashes under wordmark
dashes = "".join(f'<rect x="{x}" y="170" width="26" height="6"/>' for x in range(220, 580, 44))
parts_c.append(f'<g fill="#2FA8D8">{dashes}</g>')
# sparkles
parts_c.append('<g fill="#7DF0FF"><rect x="640" y="40" width="10" height="10"/>'
               '<rect x="616" y="60" width="6" height="6"/><rect x="160" y="36" width="8" height="8"/></g>')
write_svg("mlink-logo-c-hero.svg", 1000, 280, "\n".join(parts_c),
          'role="img" aria-label="MLink hero logo"')
