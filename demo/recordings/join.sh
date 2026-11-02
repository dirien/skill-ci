#!/usr/bin/env bash
# Join the four step clips into the one demo video of the deck, with a title card before each step.
#   demo/recordings/join.sh        writes slides/public/demo/demo.mp4 and its poster frame demo-poster.jpg
# Needs ffmpeg with drawtext. FONT overrides the title font (default: Liberation Sans Bold).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

clips=demo/recordings/clips
out=slides/public/demo/demo.mp4
font="${FONT:-/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf}"
[[ -f "${font}" ]] || { echo "font not found: ${font} (set FONT)" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg not found" >&2; exit 1; }

# Clip and title, in the order of the deck. The colors are Catppuccin Mocha, the theme of the tapes.
steps=(
  "step1-push|Step 1|Push a new version"
  "step2-rejected|Step 2|The gate says no, before any tokens"
  "step3-regression|Step 3|Better takes stable, worse stays on beta"
  "step4-harness|Step 4|The harness changed"
)

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
: >"${tmp}/list.txt"
for entry in "${steps[@]}"; do
  IFS='|' read -r clip kicker title <<<"${entry}"
  [[ -f "${clips}/${clip}.mp4" ]] || { echo "missing ${clips}/${clip}.mp4: record it with vhs demo/recordings/${clip}.tape" >&2; exit 1; }
  printf '%s' "${kicker}" >"${tmp}/${clip}.kicker"
  printf '%s' "${title}" >"${tmp}/${clip}.title"
  ffmpeg -hide_banner -loglevel error -y -f lavfi -i "color=c=0x1e1e2e:s=1600x900:r=25:d=2" \
    -vf "drawtext=fontfile=${font}:textfile=${tmp}/${clip}.kicker:fontcolor=0xcba6f7:fontsize=44:x=(w-tw)/2:y=h/2-90,drawtext=fontfile=${font}:textfile=${tmp}/${clip}.title:fontcolor=0xcdd6f4:fontsize=64:x=(w-tw)/2:y=h/2-10" \
    -c:v libx264 -profile:v high -pix_fmt yuv420p "${tmp}/${clip}-card.mp4"
  ffmpeg -hide_banner -loglevel error -y -i "${clips}/${clip}.mp4" -an \
    -c:v libx264 -profile:v high -pix_fmt yuv420p -r 25 "${tmp}/${clip}.mp4"
  printf "file '%s'\nfile '%s'\n" "${tmp}/${clip}-card.mp4" "${tmp}/${clip}.mp4" >>"${tmp}/list.txt"
done

mkdir -p "$(dirname "${out}")"
ffmpeg -hide_banner -loglevel error -y -f concat -safe 0 -i "${tmp}/list.txt" -c copy -movflags +faststart "${out}"
ffmpeg -hide_banner -loglevel error -y -ss 0.5 -i "${out}" -frames:v 1 "${out%.mp4}-poster.jpg"
echo "wrote ${out} ($(ffprobe -v error -show_entries format=duration -of csv=p=0 "${out}") s)"
