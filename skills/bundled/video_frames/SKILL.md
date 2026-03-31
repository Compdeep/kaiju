---
name: video_frames
description: "Extract frames, clips, or thumbnails from video files using ffmpeg. Use when the user asks to capture frames, create thumbnails, split video into images, or extract short clips."
metadata:
  requires:
    bins: ["ffmpeg"]
---

## When to Use

Use when the user asks to:
- Extract frames from a video at specific timestamps
- Create a thumbnail grid or contact sheet
- Extract a short clip from a longer video
- Convert video frames to images for analysis

## Planning Guidance

### Extract a single frame

1. `bash` — `ffmpeg -ss 00:01:30 -i input.mp4 -frames:v 1 -q:v 2 frame.jpg`

### Extract frames at interval

1. `bash` — `ffmpeg -i input.mp4 -vf "fps=1" frames/frame_%04d.jpg` (1 frame per second)

### Extract multiple specific timestamps

Plan in parallel:

1. `bash` — `ffmpeg -ss 00:00:10 -i input.mp4 -frames:v 1 frame_10s.jpg`
2. `bash` — `ffmpeg -ss 00:01:00 -i input.mp4 -frames:v 1 frame_60s.jpg` (parallel)
3. `bash` — `ffmpeg -ss 00:05:00 -i input.mp4 -frames:v 1 frame_300s.jpg` (parallel)

### Create a thumbnail contact sheet

1. `bash` — `ffmpeg -i input.mp4 -vf "select='not(mod(n,300))',scale=320:180,tile=4x4" -frames:v 1 contact_sheet.jpg`

### Extract a clip

1. `bash` — `ffmpeg -ss 00:01:00 -i input.mp4 -t 30 -c copy clip.mp4` (30 second clip starting at 1:00)

### Get video info first

1. `bash` — `ffprobe -v quiet -print_format json -show_format -show_streams input.mp4`
2. Based on duration/resolution, plan frame extraction (depends on step 0)

### Display extracted frames

After extraction, frames can be pushed to the composable panel:

1. `bash` — extract frames (as above)
2. `panel_push` — push the image or contact sheet to the preview panel (depends on step 0)

### What NOT to do

- Don't extract every frame from a long video — use fps filter or interval
- Don't plan sequential extractions for independent timestamps — parallelize them
- Don't forget to create output directories before extracting (`mkdir -p frames/`)
