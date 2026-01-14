package imageproc

import (
	"bytes"
	"errors"
	"image"

	"github.com/disintegration/imaging"
)

var (
	ErrImageTooLarge      = errors.New("image exceeds maximum size")
	ErrImageTooManyPixels = errors.New("image exceeds maximum pixel count")
	ErrCropOutOfBounds    = errors.New("crop rectangle exceeds image bounds")
	ErrCropInvalid        = errors.New("crop rectangle is invalid")
)

type Crop struct {
	X      int
	Y      int
	Width  int
	Height int
}

type Limits struct {
	MaxBytes  int64
	MaxPixels int
}

func DecodeImage(data []byte) (image.Image, error) {
	return imaging.Decode(bytes.NewReader(data))
}

func ValidateImage(img image.Image, maxPixels int) error {
	// Guard against images that are valid but too large for memory/time budgets.
	if maxPixels <= 0 {
		return nil
	}
	bounds := img.Bounds()
	pixels := bounds.Dx() * bounds.Dy()
	if pixels > maxPixels {
		return ErrImageTooManyPixels
	}
	return nil
}

func CropImage(img image.Image, crop Crop) (image.Image, error) {
	if crop.Width <= 0 || crop.Height <= 0 || crop.X < 0 || crop.Y < 0 {
		return nil, ErrCropInvalid
	}

	bounds := img.Bounds()
	if crop.X+crop.Width > bounds.Dx() || crop.Y+crop.Height > bounds.Dy() {
		return nil, ErrCropOutOfBounds
	}

	rect := image.Rect(
		bounds.Min.X+crop.X,
		bounds.Min.Y+crop.Y,
		bounds.Min.X+crop.X+crop.Width,
		bounds.Min.Y+crop.Y+crop.Height,
	)

	return imaging.Crop(img, rect), nil
}

func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 90
	}
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG, imaging.JPEGQuality(quality)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
