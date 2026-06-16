package imageutil

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"

	"github.com/disintegration/imaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeImage builds a solid-color image of the given size (compresses small).
func makeImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := imaging.New(w, h, color.NRGBA{R: 100, G: 150, B: 200, A: 255})
	var buf bytes.Buffer
	require.NoError(t, imaging.Encode(&buf, img, imaging.PNG))
	return buf.Bytes()
}

// makeNoisy builds a random-noise JPEG of the given size (does NOT compress well
// → useful for byte-budget tests).
func makeNoisy(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.Intn(256))
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}))
	return buf.Bytes()
}

func dims(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	return cfg.Width, cfg.Height
}

func TestNormalize_FastPath_Untouched(t *testing.T) {
	data := makeImage(t, 800, 600)
	res, err := Normalize(data, Options{MaxWidth: 2000, MaxHeight: 2000, MaxBytes: 4 << 20})
	require.NoError(t, err)
	assert.False(t, res.Resized, "in-limit image must pass through untouched")
	assert.Equal(t, data, res.Data, "data must be unchanged on fast path")
	assert.Equal(t, 800, res.Width)
	assert.Equal(t, 600, res.Height)
}

func TestNormalize_OversizedWidth_BannerImage(t *testing.T) {
	// The real failing case: 10982x1285 banner exceeds the 8000px provider cap.
	data := makeImage(t, 10982, 1285)
	res, err := Normalize(data, Options{MaxWidth: 2000, MaxHeight: 2000, MaxBytes: 4 << 20})
	require.NoError(t, err)
	assert.True(t, res.Resized)
	assert.LessOrEqual(t, res.Width, 2000, "width capped at 2000")
	assert.LessOrEqual(t, res.Height, 2000)
	// Aspect ratio preserved: 10982/1285 ≈ 8.55 → 2000/233 ≈ 8.58 (rounding).
	gotRatio := float64(res.Width) / float64(res.Height)
	assert.InDelta(t, 10982.0/1285.0, gotRatio, 0.1, "aspect ratio preserved")
	w, h := dims(t, res.Data)
	assert.Equal(t, res.Width, w)
	assert.Equal(t, res.Height, h)
}

func TestNormalize_TotalPixels(t *testing.T) {
	// 8000x8000 = 64MP > 36MP cap → sqrt(36/64)=0.75 → ~6000x6000=36MP.
	data := makeImage(t, 8000, 8000)
	res, err := Normalize(data, Options{MaxBytes: 8 << 20, MaxTotalPixels: 36_000_000})
	require.NoError(t, err)
	assert.True(t, res.Resized)
	assert.LessOrEqual(t, int64(res.Width)*int64(res.Height), int64(36_000_000), "total pixels under cap")
}

func TestNormalize_AspectRatio(t *testing.T) {
	// 20000x100 = ratio 200 > 150 cap.
	data := makeImage(t, 20000, 100)
	res, err := Normalize(data, Options{MaxBytes: 8 << 20, MaxTotalPixels: 36_000_000, MaxAspectRatio: 150})
	require.NoError(t, err)
	assert.True(t, res.Resized)
	// Extra 0.8 shrink applied for over-ratio; just assert it changed + still decodable.
	w, h := dims(t, res.Data)
	assert.Positive(t, w)
	assert.Positive(t, h)
}

func TestNormalize_ByteBudget_Converges(t *testing.T) {
	// Noisy 3000x3000 JPEG is large; cap dims at 2000 + 300KB byte budget.
	data := makeNoisy(t, 3000, 3000)
	const maxBytes = 300 * 1024
	res, err := Normalize(data, Options{MaxWidth: 2000, MaxHeight: 2000, MaxBytes: maxBytes})
	require.NoError(t, err)
	assert.True(t, res.Resized)
	assert.LessOrEqual(t, len(res.Data), maxBytes, "output within byte budget")
	assert.Equal(t, "image/jpeg", res.MediaType)
}

func TestNormalize_ImpossibleByteBudget(t *testing.T) {
	data := makeNoisy(t, 1000, 1000)
	_, err := Normalize(data, Options{MaxBytes: 10}) // 10 bytes is impossible
	assert.ErrorIs(t, err, ErrImageTooLarge)
}

func TestNormalize_CorruptedAndEmpty(t *testing.T) {
	_, err := Normalize([]byte("not an image"), Options{MaxWidth: 2000})
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrImageTooLarge, "decode failure is a distinct error")

	_, err = Normalize(nil, Options{})
	assert.Error(t, err)
}

func TestNormalize_NoLimits_FastPath(t *testing.T) {
	data := makeImage(t, 5000, 5000)
	res, err := Normalize(data, Options{}) // all zero = no limits
	require.NoError(t, err)
	assert.False(t, res.Resized)
	assert.Equal(t, data, res.Data)
}

func TestTargetDimensions_ClampMin1(t *testing.T) {
	// Extreme: tiny max on a wide image → width clamps to >=1, never 0.
	tw, th := targetDimensions(10000, 1, Options{MaxWidth: 2})
	assert.GreaterOrEqual(t, tw, 1)
	assert.GreaterOrEqual(t, th, 1)
}
