# Overlay Fonts

This folder holds the TTF font files that the renderer hands to FFmpeg's
`drawtext` filter when burning per-clip text overlays into the video. The
filenames are referenced explicitly from `pipeline/renderer.go ->
resolveFontFile()` — if a file is missing the renderer transparently falls
back to FFmpeg's built-in font and the overlay still renders, just in the
default face.

## Why these aren't checked into git

All fonts here are freely redistributable under the SIL Open Font License
(or Apache 2.0 for Roboto), but they are still ~200 KB each and rarely
change. Keep them out of git and download them once per environment with
the script below.

## Expected files

| Family           | Files                                                                                |
| ---------------- | ------------------------------------------------------------------------------------ |
| Inter            | `Inter-Regular.ttf`, `Inter-Bold.ttf`, `Inter-Italic.ttf`, `Inter-BoldItalic.ttf`     |
| Roboto           | `Roboto-Regular.ttf`, `Roboto-Bold.ttf`, `Roboto-Italic.ttf`, `Roboto-BoldItalic.ttf` |
| Montserrat       | `Montserrat-Regular.ttf`, `Montserrat-Bold.ttf`                                      |
| Playfair Display | `PlayfairDisplay-Regular.ttf`, `PlayfairDisplay-Bold.ttf`                            |
| Bebas Neue       | `BebasNeue-Regular.ttf`                                                              |

`Inter-Regular.ttf` is the global fallback — make sure at least this one
exists. Everything else degrades gracefully.

## Quick install (PowerShell)

From the repo root:

```powershell
.\assets\fonts\download.ps1
```

The script pulls static-weight TTFs from the fontsource CDN
(`https://cdn.jsdelivr.net/fontsource/fonts/...`). We use fontsource rather
than the official google/fonts GitHub mirror because the latter now ships
only variable fonts at the top level, which FFmpeg's `drawtext` can't
select weights from cleanly.

## Licensing

- Inter — SIL Open Font License 1.1
- Roboto — Apache License 2.0
- Montserrat — SIL Open Font License 1.1
- Playfair Display — SIL Open Font License 1.1
- Bebas Neue — SIL Open Font License 1.1

These licenses allow redistribution with the rendered video; no attribution
inside the video itself is required, but keep the license texts available
if you redistribute the font files themselves.
