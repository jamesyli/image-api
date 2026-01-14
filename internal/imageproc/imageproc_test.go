package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestValidateImageMaxPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	if err := ValidateImage(img, 50); err != ErrImageTooManyPixels {
		t.Fatalf("expected ErrImageTooManyPixels, got %v", err)
	}
}

func TestCropImageOutOfBounds(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	_, err := CropImage(img, Crop{X: 5, Y: 5, Width: 10, Height: 10})
	if err != ErrCropOutOfBounds {
		t.Fatalf("expected ErrCropOutOfBounds, got %v", err)
	}
}

func TestEncodeJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	data, err := EncodeJPEG(img, 80)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("expected non-empty jpeg data")
	}

	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("unexpected bounds: %v", decoded.Bounds())
	}
}
