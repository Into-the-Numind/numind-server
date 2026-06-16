// Package imageutil is the shared image-normalization service: it downscales and
// recompresses images so they fit the size/dimension limits enforced by the
// various AI providers (claude/dmxapi hard-reject >8000px per side; Doubao caps
// total pixels at 36MP and aspect ratio at 150:1; Anthropic caps base64 at 5MB).
//
// All image-handling callers (chatbot/agent upload, salesrag, VLM describe)
// should route through Normalize instead of hand-rolling resize loops. This
// consolidates four duplicated salesrag blocks and fixes a quality bug in them
// (they re-resized an already-resized image, compounding Lanczos artifacts).
//
// Design follows Claude Code's imageResizer.ts: fast-path untouched if already
// within limits; otherwise resize from the ORIGINAL decoded image (never from a
// prior resize) to fit the dimension caps, then iteratively drop JPEG quality
// (and shrink further if needed) until the byte budget is met.
package imageutil

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"math"
	"net/http"

	"github.com/disintegration/imaging"
)

// ErrImageTooLarge is returned when an image cannot be normalized within the
// requested limits even after maximal downscaling + compression. Callers should
// surface a user-friendly message (e.g. "图片过大，请换一张更小的图片").
var ErrImageTooLarge = errors.New("imageutil: image too large to normalize within limits")

// Options describes the target limits. A zero value for any field means "no
// limit for that dimension".
type Options struct {
	MaxWidth       int     // max width in px (0 = unlimited)
	MaxHeight      int     // max height in px (0 = unlimited)
	MaxBytes       int     // max output byte size (0 = unlimited)
	MaxTotalPixels int64   // max width*height (0 = unlimited)
	MaxAspectRatio float64 // max long/short side ratio (0 = unlimited)
}

// Result is the normalized image.
type Result struct {
	Data      []byte
	MediaType string // sniffed via http.DetectContentType ("image/jpeg" after recompress)
	Width     int
	Height    int
	Resized   bool // false when the original passed the fast path untouched
}

// jpegQualitySteps is the descending JPEG quality ladder tried during the
// byte-budget compression loop (mirrors Claude Code / salesrag).
var jpegQualitySteps = []int{85, 70, 55, 40, 25, 20}

// Normalize decodes data, and if it violates any configured limit, downscales
// (maintaining aspect ratio) and/or recompresses it to fit. Images already
// within all limits are returned unchanged (Resized=false).
func Normalize(data []byte, opt Options) (Result, error) {
	if len(data) == 0 {
		return Result{}, fmt.Errorf("imageutil.Normalize: empty input")
	}

	img, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("imageutil.Normalize: decode: %w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// Fast path: already within every configured limit.
	if withinLimits(w, h, len(data), opt) {
		return Result{
			Data:      data,
			MediaType: sniff(data),
			Width:     w,
			Height:    h,
			Resized:   false,
		}, nil
	}

	// Compute the target dimensions that satisfy the dimension/pixel/ratio caps.
	tw, th := targetDimensions(w, h, opt)

	// Resize ALWAYS from the original img (never from a prior resize) + iterate
	// JPEG quality down until the byte budget is met. If quality alone is not
	// enough, shrink the target further and retry.
	scale := 1.0
	for attempt := 0; attempt < 8; attempt++ {
		curW := clampMin1(int(float64(tw) * scale))
		curH := clampMin1(int(float64(th) * scale))

		resized := imaging.Resize(img, curW, curH, imaging.Lanczos)
		rb := resized.Bounds()
		outW, outH := rb.Dx(), rb.Dy()

		for _, q := range jpegQualitySteps {
			var buf bytes.Buffer
			if encErr := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: q}); encErr != nil {
				return Result{}, fmt.Errorf("imageutil.Normalize: encode: %w", encErr)
			}
			out := buf.Bytes()
			if opt.MaxBytes <= 0 || len(out) <= opt.MaxBytes {
				return Result{
					Data:      out,
					MediaType: "image/jpeg",
					Width:     outW,
					Height:    outH,
					Resized:   true,
				}, nil
			}
		}

		// Quality ladder exhausted at this size and still over budget → shrink 25%.
		if curW <= 1 && curH <= 1 {
			break
		}
		scale *= 0.75
	}

	return Result{}, ErrImageTooLarge
}

// withinLimits reports whether (w,h,bytes) already satisfy every configured cap.
func withinLimits(w, h, byteLen int, opt Options) bool {
	if opt.MaxWidth > 0 && w > opt.MaxWidth {
		return false
	}
	if opt.MaxHeight > 0 && h > opt.MaxHeight {
		return false
	}
	if opt.MaxBytes > 0 && byteLen > opt.MaxBytes {
		return false
	}
	if opt.MaxTotalPixels > 0 && int64(w)*int64(h) > opt.MaxTotalPixels {
		return false
	}
	if opt.MaxAspectRatio > 0 && aspectRatio(w, h) > opt.MaxAspectRatio {
		return false
	}
	return true
}

// targetDimensions computes the largest (w,h) preserving aspect ratio that
// satisfies the per-side, total-pixel and aspect-ratio caps. Byte budget is
// handled separately by the quality/scale loop.
func targetDimensions(w, h int, opt Options) (int, int) {
	tw, th := float64(w), float64(h)

	// Per-side caps.
	if opt.MaxWidth > 0 && tw > float64(opt.MaxWidth) {
		th = th * float64(opt.MaxWidth) / tw
		tw = float64(opt.MaxWidth)
	}
	if opt.MaxHeight > 0 && th > float64(opt.MaxHeight) {
		tw = tw * float64(opt.MaxHeight) / th
		th = float64(opt.MaxHeight)
	}

	// Total-pixel cap: scale both sides by sqrt(maxPixels/current).
	if opt.MaxTotalPixels > 0 {
		cur := tw * th
		if cur > float64(opt.MaxTotalPixels) {
			s := math.Sqrt(float64(opt.MaxTotalPixels) / cur)
			tw *= s
			th *= s
		}
	}

	// Aspect-ratio cap: a too-extreme ratio (e.g. WeChat long screenshots) gets an
	// extra modest shrink. NOTE: this is BEST-EFFORT pixel reduction, not a ratio
	// fix — scaling both sides preserves the ratio. Genuinely capping the ratio
	// would require cropping (losing content, bad for OCR), which we intentionally
	// don't do; the shrink just lowers total pixels so the image is less likely to
	// trip a provider's combined size+ratio guard. Mirrors the original salesrag
	// behaviour. Only the salesrag/Doubao path sets MaxAspectRatio (the chatbot
	// upload path leaves it 0).
	if opt.MaxAspectRatio > 0 && aspectRatio(int(tw), int(th)) > opt.MaxAspectRatio {
		tw *= 0.8
		th *= 0.8
	}

	return clampMin1(int(tw)), clampMin1(int(th))
}

func aspectRatio(w, h int) float64 {
	if w == 0 || h == 0 {
		return 0
	}
	if w >= h {
		return float64(w) / float64(h)
	}
	return float64(h) / float64(w)
}

// clampMin1 guards against int truncation to 0 (a 0 dimension would wedge the
// resize loop / be rejected by encoders).
func clampMin1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

// sniff returns the media type of raw image bytes (imaging.Decode does not
// expose the source format, so we use http.DetectContentType on the header).
func sniff(data []byte) string {
	n := len(data)
	if n > 512 {
		n = 512
	}
	mt := http.DetectContentType(data[:n])
	if mt == "application/octet-stream" {
		return "image/jpeg" // best-effort default for decodable-but-unsniffed images
	}
	return mt
}
