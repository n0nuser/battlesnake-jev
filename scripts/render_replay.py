#!/usr/bin/env python3
"""Render a recorded Battlesnake game as video frames.

Frames come from the rules CLI's -o output; the decision annotations come from
our own server's debug log, joined on game id and turn.
"""
import json, re, sys, os
from PIL import Image, ImageDraw, ImageFont

BG      = (15, 16, 22)
PANEL   = (23, 25, 34)
GRID    = (38, 41, 54)
TEXT    = (232, 234, 242)
DIM     = (138, 143, 161)
ACCENT  = (167, 139, 250)   # the model decided
CODE    = (94, 234, 212)    # the code decided

FONT_DIR = "/usr/share/fonts"
def font(name, size):
    for root, _, files in os.walk(FONT_DIR):
        for f in files:
            if f == name:
                return ImageFont.truetype(os.path.join(root, f), size)
    return ImageFont.load_default()

F_H1   = font("DejaVuSans-Bold.ttf", 34)
F_H2   = font("DejaVuSans-Bold.ttf", 22)
F_BODY = font("DejaVuSans.ttf", 19)
F_SM   = font("DejaVuSans.ttf", 16)
F_MONO = font("DejaVuSansMono-Bold.ttf", 21)
F_MONOS= font("DejaVuSansMono.ttf", 16)

def hexrgb(h, fallback=(120,120,120)):
    h = (h or "").lstrip("#")
    if len(h) != 6:
        return fallback
    try:
        return tuple(int(h[i:i+2], 16) for i in (0, 2, 4))
    except ValueError:
        return fallback

def load_frames(path):
    frames = []
    for line in open(path):
        d = json.loads(line)
        if "board" in d:
            frames.append(d)
    return frames

def load_decisions(path, game_id):
    """turn -> dict of the server's own decision fields for that turn."""
    out = {}
    pat = re.compile(r'msg=move game=(\S+) turn=(\d+) move=(\S+) reason=(\S+) mode=(\S+) '
                     r'health=(\d+) length=(\d+) tokens=(\d+) room=(\S+) confinement=(\S+) '
                     r'space=(\S+) tailsafe=(\S+)')
    for line in open(path):
        m = pat.search(line)
        if not m or m.group(1) != game_id:
            continue
        out[int(m.group(2))] = dict(
            move=m.group(3), reason=m.group(4), mode=m.group(5),
            health=int(m.group(6)), length=int(m.group(7)), tokens=int(m.group(8)),
            space=m.group(11), tailsafe=m.group(12) == "true")
    return out

def ascii_board(board, me_id):
    """Exactly what internal/strategy/render.go sends to the model."""
    W, H = board["width"], board["height"]
    g = [["."] * W for _ in range(H)]
    for f in board["food"]:
        g[f["y"]][f["x"]] = "o"
    for h in board.get("hazards") or []:
        g[h["y"]][h["x"]] = "x"
    for s in board["snakes"]:
        body, head = ("#", "H") if s["id"] == me_id else ("+", "E")
        for c in s["body"]:
            if 0 <= c["y"] < H and 0 <= c["x"] < W:
                g[c["y"]][c["x"]] = body
        g[s["head"]["y"]][s["head"]["x"]] = head
    return "\n".join("".join(g[y]) for y in range(H - 1, -1, -1))

def draw_board(d, box, board, colors, cell_gap=3):
    x0, y0, size = box
    W, H = board["width"], board["height"]
    cell = size // W
    d.rounded_rectangle([x0 - 10, y0 - 10, x0 + cell * W + 10, y0 + cell * H + 10], 12, fill=PANEL)
    for gy in range(H):
        for gx in range(W):
            px, py = x0 + gx * cell, y0 + (H - 1 - gy) * cell
            d.rounded_rectangle([px + 1, py + 1, px + cell - 2, py + cell - 2], 4, fill=GRID)
    for f in board["food"]:
        px, py = x0 + f["x"] * cell, y0 + (H - 1 - f["y"]) * cell
        r = cell // 2 - 5
        cx, cy = px + cell // 2, py + cell // 2
        d.ellipse([cx - r, cy - r, cx + r, cy + r], fill=(248, 113, 113))
    for s in board["snakes"]:
        col = colors.get(s["id"], (150, 150, 150))
        for i, c in enumerate(s["body"]):
            px, py = x0 + c["x"] * cell, y0 + (H - 1 - c["y"]) * cell
            pad = cell_gap if i else 1
            shade = col if i else tuple(int(v + (255 - v) * 0.42) for v in col)
            d.rounded_rectangle([px + pad, py + pad, px + cell - pad - 1, py + cell - pad - 1],
                                6 if i else 8, fill=shade)
        hx, hy = s["head"]["x"], s["head"]["y"]
        px, py = x0 + hx * cell, y0 + (H - 1 - hy) * cell
        d.rounded_rectangle([px + 1, py + 1, px + cell - 2, py + cell - 2], 8,
                            outline=(255, 255, 255), width=2)

def snake_rows(d, x, y, board, colors, me_id):
    d.text((x, y), "ON THE BOARD", font=F_SM, fill=DIM)
    y += 26
    for s in sorted(board["snakes"], key=lambda s: -len(s["body"])):
        col = colors.get(s["id"], (150, 150, 150))
        d.rounded_rectangle([x, y + 4, x + 14, y + 18], 4, fill=col)
        name = s["name"] + ("  ← the model" if s["id"] == me_id else "")
        d.text((x + 24, y), name, font=F_BODY, fill=TEXT)
        d.text((x + 24, y + 24), f"length {len(s['body'])}   health {s['health']}",
               font=F_SM, fill=DIM)
        y += 54
    return y

def render_hero(frames, decisions, me_id, colors, outdir, title, subtitle):
    """Board plus a live readout of who decided each move and why."""
    os.makedirs(outdir, exist_ok=True)
    W, H = 1280, 720
    for i, fr in enumerate(frames):
        img = Image.new("RGB", (W, H), BG)
        d = ImageDraw.Draw(img)
        d.text((44, 34), title, font=F_H1, fill=TEXT)
        d.text((44, 76), subtitle, font=F_SM, fill=DIM)

        draw_board(d, (44, 130, 540), fr["board"], colors)

        px = 650
        d.text((px, 130), f"TURN {fr['turn']}", font=F_H2, fill=TEXT)

        dec = decisions.get(fr["turn"])
        by_model = bool(dec and dec["reason"].startswith("jev") and dec["reason"] != "jev-failed")
        col = ACCENT if by_model else CODE
        label = "THE MODEL DECIDED" if by_model else "THE CODE DECIDED"
        if dec is None:
            label, col = "—", DIM

        d.rounded_rectangle([px, 176, px + 546, 330], 12, fill=PANEL)
        d.rounded_rectangle([px, 176, px + 6, 330], 3, fill=col)
        d.text((px + 24, 196), label, font=F_SM, fill=col)
        if dec:
            d.text((px + 24, 220), dec["move"].upper(), font=F_H1, fill=TEXT)
            reason_text = {
                "jev": "close call, asked the model",
                "jev-avoid": "model struck a move off as a trap",
                "jev-alarm": "model warned the space was closing",
                "jev-failed": "model missed the deadline, fell back",
                "interchangeable": "options identical, no call needed",
                "clear-winner": "deterministic scores were decisive",
                "only-move": "the only safe move",
                "trapped": "nothing safe, least bad",
                "no-budget": "not enough time to ask",
                "cold-pool": "connection cold, skipped the call",
                "random": "coin flip (control arm)",
            }.get(dec["reason"], dec["reason"])
            d.text((px + 24, 262), reason_text, font=F_BODY, fill=DIM)
            d.text((px + 24, 292), f"space {dec['space']}    "
                                   f"tail {'reachable' if dec['tailsafe'] else 'CUT OFF'}    "
                                   f"{dec['tokens'] or '0'} tokens",
                   font=F_SM, fill=DIM)

        snake_rows(d, px, 362, fr["board"], colors, me_id)
        d.text((px, 660), "tactics: deterministic Go   ·   judgment calls: TypeSafe Jev",
               font=F_SM, fill=DIM)
        names = [s["name"] for s in fr["board"]["snakes"]]
        if len(names) == 1 and names[0] == "jev":
            d.rounded_rectangle([px, 560, px + 546, 630], 12, fill=(45, 30, 80))
            d.text((px + 24, 578), "LAST SNAKE STANDING", font=F_H2, fill=ACCENT)
            d.text((px + 24, 604), f"survived {fr['turn']} turns against three bots",
                   font=F_SM, fill=DIM)
        img.save(f"{outdir}/f{i:05d}.png")
    # hold the final frame so the ending reads
    last = Image.open(f"{outdir}/f{len(frames)-1:05d}.png")
    for k in range(18):
        last.save(f"{outdir}/f{len(frames)+k:05d}.png")
    return len(frames) + 18

def render_split(frames, decisions, me_id, colors, outdir, caption):
    """The real board beside the exact text the model is sent."""
    os.makedirs(outdir, exist_ok=True)
    W, H = 1280, 720
    for i, fr in enumerate(frames):
        img = Image.new("RGB", (W, H), BG)
        d = ImageDraw.Draw(img)
        d.text((44, 30), "What the model actually sees", font=F_H1, fill=TEXT)
        d.text((44, 72), caption, font=F_SM, fill=DIM)

        draw_board(d, (44, 124, 480), fr["board"], colors)
        d.text((44, 640), "the game", font=F_SM, fill=DIM)

        px = 620
        d.rounded_rectangle([px, 114, px + 616, 622], 12, fill=PANEL)
        d.text((px + 24, 132), "state sent to the model", font=F_SM, fill=ACCENT)
        # Each glyph takes the colour of the thing it stands for, so the text
        # panel and the board read as the same picture.
        glyph_col = {"H": (221, 214, 254), "#": ACCENT, "E": (252, 165, 165),
                     "+": (148, 163, 184), "o": (248, 113, 113),
                     "x": (250, 204, 21), ".": (84, 90, 112)}
        art = ascii_board(fr["board"], me_id)
        y = 168
        for row in art.split("\n"):
            for j, ch in enumerate(row):
                d.text((px + 24 + j * 30, y), ch, font=F_MONO, fill=glyph_col.get(ch, TEXT))
            y += 30
        me = next((s for s in fr["board"]["snakes"] if s["id"] == me_id), None)
        if me:
            d.text((px + 24, y + 12),
                   f'turn {fr["turn"]}  health {me["health"]}  length {len(me["body"])}',
                   font=F_MONOS, fill=DIM)
        d.text((px + 24, y + 38), "H my head   # my body   E rival head",
               font=F_MONOS, fill=DIM)
        d.text((px + 24, y + 60), "+ rival body   o food   . empty",
               font=F_MONOS, fill=DIM)
        d.text((px, 648), "~40 tokens per turn", font=F_SM, fill=DIM)
        img.save(f"{outdir}/f{i:05d}.png")
    return len(frames)

if __name__ == "__main__":
    rec, log, outdir, mode = sys.argv[1:5]
    frames = load_frames(rec)
    gid = frames[0]["game"]["id"]
    me = next(s for s in frames[0]["board"]["snakes"] if s["name"] == "jev")
    me_id = me["id"]
    # Colours are assigned here for legibility rather than taken from each
    # snake's own customisation: the randomised hues can land close together,
    # and the one thing a viewer must never lose track of is which snake is the
    # model's.
    palette = [(45, 212, 191), (251, 191, 36), (244, 114, 182), (148, 163, 184)]
    colors, k = {}, 0
    for sn in frames[0]["board"]["snakes"]:
        if sn["id"] == me_id:
            colors[sn["id"]] = (167, 139, 250)
        else:
            colors[sn["id"]] = palette[k % len(palette)]
            k += 1
    decisions = load_decisions(log, gid)
    print(f"game {gid[:8]}  frames={len(frames)}  decisions={len(decisions)}")
    if mode == "hero":
        n = render_hero(frames, decisions, me_id, colors, outdir,
                        sys.argv[5], sys.argv[6])
    else:
        n = render_split(frames, decisions, me_id, colors, outdir, sys.argv[5])
    print(f"wrote {n} frames to {outdir}")
