# Seamark logo assets

Mark: two channel marks with the safe passage between them. Ink #14181A, paper #F4F3F0, cardinal amber #E8B23A. Wordmark: JetBrains Mono Bold.

## Which file

| Use | File |
|---|---|
| GitHub org / repo avatar | `seamark-avatar-1024.png` (or `seamark-avatar.svg`) |
| README hero banner | `seamark-banner-2560.png` (renders at 1280x400) |
| Horizontal lockup, light bg | `seamark-lockup-2000.png` |
| Favicon | `seamark-favicon.svg` (channel is a solid bar for 16px) |
| Mark on a light surface | `seamark-mark-on-light.svg` |
| Mark on a dark surface | `seamark-mark-on-dark.svg` |
| Mark, tinted by CSS `color` | `seamark-mark-mono.svg` (inline only) |

SVGs contain no text, so they render identically anywhere. Anything with the
wordmark ships as PNG so the type does not fall back to the viewer's mono font.

## Rules

- Clear space: one cone-width (1/4 of the mark) on all sides.
- Minimum mark size 16px; below the favicon file, do not scale the detailed mark.
- Never rotate, never recolor the paper wedge, never place on a mid-tone —
  the amber wedge needs the ink or paper field to read as a signal.

## README snippet

```markdown
<p align="center">
  <img src="assets/seamark-banner-2560.png" alt="Seamark" width="720">
</p>
```
