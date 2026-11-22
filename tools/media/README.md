# media

Prepare an image for a platform, locally. `media` takes any image and writes one
that fits what X, LinkedIn or Instagram accept, so you (or an assistant) can fix
an image before posting it with `x`, `linkedin` or `instagram`.

It has no network access, no accounts and no credentials. Output is JSON; errors
go to stderr; exit 0 is ok, 1 an error (or `check` found problems), 2 a usage
mistake.

```bash
cd tools/media && go build -o media .
```

## Profiles

A profile is data: what a platform accepts. Three ship in `profiles.toml`:

| Profile | Formats | Max size | Dimensions | Source |
| --- | --- | --- | --- | --- |
| `instagram` (a feed image) | JPEG only | 8 MB (8,000,000 bytes; `fit` aims for 7,900,000) | aspect 4:5 (0.8) to 1.91:1; width 320 to 1440 | Meta, "IG User Media" reference, Image Specifications |
| `x` | PNG, JPEG, GIF, WebP | 5 MiB | none enforced | Mimir's `x_post` tool |
| `linkedin` | JPEG, PNG, GIF | 8 MiB | none enforced | Mimir's `linkedin_post` tool |

The first format listed is the preferred output format.

## Commands

```bash
media profiles                                   # list profiles (formats, limits, source, where each came from)
media info photo.jpg                             # format, width, height, bytes, has_alpha, animated, orientation
media check --for instagram photo.png            # {"ok": false, "problems": [...]}; exit 1 if not ok; changes nothing
media fit --for instagram photo.png              # writes photo.instagram.jpg next to it
media fit --for instagram photo.png --out /tmp/ig.jpg --mode crop --background '#000000' --force
```

`fit` prints `{out, format, width, height, bytes, changes}`. `changes` says in
words what was done:

```json
{
  "out": "photo.instagram.jpg", "format": "jpeg", "width": 1440, "height": 1080, "bytes": 412873,
  "changes": [
    "converted PNG to JPEG",
    "flattened transparency onto #FFFFFF",
    "scaled 4000x3000 to 1440x1080",
    "encoded JPEG at quality 92"
  ]
}
```

If the input already satisfies the profile, nothing is written, `changes` is
`[]` and `out` is the input path (with `--out`, the file is copied instead).

## Add a profile

Put a file in `~/.config/agent-tools/media/profiles.d/` (`$XDG_CONFIG_HOME` is
honored), or in any directory listed in `$MEDIA_PROFILES` (`:`-separated):

```toml
[[profile]]
name = "square-thumb"
description = "A square 600 px PNG thumbnail."
formats = ["png"]          # jpeg, png, gif, webp; the first is preferred
max_bytes = 500000         # 0 = no limit; every number below too
# target_bytes = 480000    # optional: aim lower than max_bytes
min_width = 0
max_width = 600
min_height = 0
max_height = 600
min_aspect = 1.0           # width / height
max_aspect = 1.0
source = "my own design doc"
```

A profile with a name that already exists (built in or in another file) is an
error, never a silent override. A malformed file stops the tool with the file
named. Unknown keys are errors, so a typo cannot silently drop a limit.

## What `fit` does

In this order:

1. Applies the JPEG EXIF orientation (1-8), so a phone photo is not rotated or
   mirrored. A file carrying an orientation other than 1 counts as a `check`
   problem, because viewers disagree about applying it; `fit` bakes it in and
   writes a file with no orientation tag.
2. Chooses the format: keeps the input's if the profile allows it, else the
   profile's first format that `media` can write (JPEG or PNG).
3. Flattens transparency onto `--background` (default white) for JPEG output.
4. Fixes the aspect ratio only if it is outside the profile's range, and only as
   far as the nearest allowed ratio. `--mode pad` (default) adds background-colored
   bars and loses nothing. `--mode crop` trims a centered strip.
5. Scales (CatmullRom) down to meet a maximum width or height, or up to meet a
   minimum width or height. It never upscales otherwise.
6. Encodes JPEG at quality 92, stepping down to a floor of 60 until it is under
   the byte limit; if still too big it scales down 10% at a time and repeats. A
   PNG that is too big becomes a JPEG when the profile allows one. If the limits
   cannot all be met (for example a byte limit with a minimum width) it fails and
   says so.
7. Re-checks the written file against the profile; a file that fails is deleted.

It never modifies or overwrites the input, and refuses to overwrite an existing
output unless `--force` is given (the input itself is never a valid output). The
output's directory must already exist; it is not created.

### What it does not do

- **Animated GIFs (and animated WebP) cannot be resized or converted.** One that
  already satisfies the profile is passed through; otherwise `fit` fails with a
  message.
- It cannot write WebP or GIF. A still GIF or WebP that already fits is left alone,
  but one that has to change is written as PNG or JPEG, and `changes` says so.
- No upscaling beyond a profile's minimum width/height; no sharpening, no color
  profile handling (ICC profiles are dropped on re-encode).
- It refuses files over 100 MB or 100 megapixels, checked from the header before
  any pixel is decoded.

## Not verified

The limits come from Meta's documentation (Instagram) and from the Mimir tools
that already publish to X and LinkedIn; they are not measured. The tool has
never been run against a real platform upload. Treat the profiles as the best
current statement of the rules, correct them in a profile file if a platform
disagrees, and expect `x` and `linkedin` to have undiscovered dimension rules.
Instagram's byte target (7,900,000) is a safety margin of our own, not a platform number.

## MCP

The MCP server exposes `media_profiles`, `media_info`, `media_check` (read) and
`media_fit` (write, since it creates a file). Paths are relative to the server's
working directory. See `mcp/manifests/media.toml`.
