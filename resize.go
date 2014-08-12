/*
Copyright (c) 2012, Jan Schlicht <jan.schlicht@gmail.com>

Permission to use, copy, modify, and/or distribute this software for any purpose
with or without fee is hereby granted, provided that the above copyright notice
and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH
REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY AND
FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT,
INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS
OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER
TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF
THIS SOFTWARE.
*/

// Package resize implements various image resizing methods.
//
// The package works with the Image interface described in the image package.
// Various interpolation methods are provided and multiple processors may be
// utilized in the computations.
//
// Example:
//     imgResized := resize.Resize(1000, 0, imgOld, resize.MitchellNetravali)
package resize

import (
	"fmt"
	"image"
	"runtime"
)

// An InterpolationFunction provides the parameters that describe an
// interpolation kernel. It returns the number of samples to take
// and the kernel function to use for sampling.
type InterpolationFunction int

// InterpolationFunction constants
const (
	// Nearest-neighbor interpolation
	NearestNeighbor InterpolationFunction = iota
	// Bilinear interpolation
	Bilinear
	// Bicubic interpolation (with cubic hermite spline)
	Bicubic
	// Mitchell-Netravali interpolation
	MitchellNetravali
	// Lanczos interpolation (a=2)
	Lanczos2
	// Lanczos interpolation (a=3)
	Lanczos3
)

// kernal, returns an InterpolationFunctions taps and kernel.
func (i InterpolationFunction) kernel() (int, func(float64) float64) {
	switch i {
	case Bilinear:
		return 2, linear
	case Bicubic:
		return 4, cubic
	case MitchellNetravali:
		return 4, mitchellnetravali
	case Lanczos2:
		return 4, lanczos2
	case Lanczos3:
		return 6, lanczos3
	default:
		// Default to NearestNeighbor.
		return 2, nearest
	}
}

func offsetError(offset, pix int) error {
	return fmt.Errorf("invalid bounds: offset %d exceeds pix length %d", offset, pix)
}

// values <1 will sharpen the image
var blur = 1.0

// Resize scales an image to new width and height using the interpolation function interp.
// A new image with the given dimensions will be returned.
// If one of the parameters width or height is set to 0, its size will be calculated so that
// the aspect ratio is that of the originating image.
// The resizing algorithm uses channels for parallel computation.
// If an error is encountered Resize returns a nil image.
func Resize(width, height uint, img image.Image, interp InterpolationFunction) image.Image {
	res, err := ResizeSafe(width, height, img, interp)
	if err != nil {
		return nil
	}
	return res
}

// ResizeSafe scales an image using Resize, but will return an error, if any.
func ResizeSafe(width, height uint, img image.Image, interp InterpolationFunction) (image.Image, error) {
	scaleX, scaleY := calcFactors(width, height, float64(img.Bounds().Dx()), float64(img.Bounds().Dy()))
	if width == 0 {
		width = uint(0.7 + float64(img.Bounds().Dx())/scaleX)
	}
	if height == 0 {
		height = uint(0.7 + float64(img.Bounds().Dy())/scaleY)
	}
	if interp == NearestNeighbor {
		return resizeNearest(width, height, scaleX, scaleY, img, interp)
	}
	return resize(width, height, scaleX, scaleY, img, interp)
}

func resize(width, height uint, scaleX, scaleY float64, img image.Image, interp InterpolationFunction) (image.Image, error) {
	taps, kernel := interp.kernel()
	cpus := runtime.NumCPU()
	done := make(chan error, cpus)

	// Generic access to image.Image is slow in tight loops.
	// The optimal access has to be determined from the concrete image type.
	switch input := img.(type) {
	case *image.RGBA:
		// 8-bit precision
		temp := image.NewRGBA(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeights8(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.RGBA)
			go func() {
				done <- resizeRGBA(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeights8(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.RGBA)
			go func() {
				done <- resizeRGBA(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	case *image.YCbCr:
		// 8-bit precision
		// accessing the YCbCr arrays in a tight loop is slow.
		// converting the image to ycc increases performance by 2x.
		temp := newYCC(image.Rect(0, 0, input.Bounds().Dy(), int(width)), input.SubsampleRatio)
		result := newYCC(image.Rect(0, 0, int(width), int(height)), input.SubsampleRatio)

		coeffs, offset, filterLength := createWeights8(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX, kernel)
		in := imageYCbCrToYCC(input)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*ycc)
			go func() {
				done <- resizeYCbCr(in, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		coeffs, offset, filterLength = createWeights8(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*ycc)
			go func() {
				done <- resizeYCbCr(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result.YCbCr(), nil

	case *image.RGBA64:
		// 16-bit precision
		temp := image.NewRGBA64(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewRGBA64(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeights16(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.RGBA64)
			go func() {
				done <- resizeRGBA64(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeights16(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.RGBA64)
			go func() {
				done <- resizeGeneric(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	case *image.Gray:
		// 8-bit precision
		temp := image.NewGray(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewGray(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeights8(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.Gray)
			go func() {
				done <- resizeGray(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeights8(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.Gray)
			go func() {
				done <- resizeGray(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	case *image.Gray16:
		// 16-bit precision
		temp := image.NewGray16(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewGray16(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeights16(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.Gray16)
			go func() {
				done <- resizeGray16(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeights16(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.Gray16)
			go func() {
				done <- resizeGray16(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	default:
		// 16-bit precision
		temp := image.NewRGBA64(image.Rect(0, 0, img.Bounds().Dy(), int(width)))
		result := image.NewRGBA64(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeights16(temp.Bounds().Dy(), img.Bounds().Min.X, taps, blur, scaleX, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.RGBA64)
			go func() {
				done <- resizeGeneric(img, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeights16(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY, kernel)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.RGBA64)
			go func() {
				done <- resizeRGBA64(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	}
}

func resizeNearest(width, height uint, scaleX, scaleY float64, img image.Image, interp InterpolationFunction) (image.Image, error) {
	taps, _ := interp.kernel()
	cpus := runtime.NumCPU()
	done := make(chan error, cpus)

	switch input := img.(type) {
	case *image.RGBA:
		// 8-bit precision
		temp := image.NewRGBA(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeightsNearest(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.RGBA)
			go func() {
				done <- nearestRGBA(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeightsNearest(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.RGBA)
			go func() {
				done <- nearestRGBA(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	case *image.YCbCr:
		// 8-bit precision
		// accessing the YCbCr arrays in a tight loop is slow.
		// converting the image to ycc increases performance by 2x.
		temp := newYCC(image.Rect(0, 0, input.Bounds().Dy(), int(width)), input.SubsampleRatio)
		result := newYCC(image.Rect(0, 0, int(width), int(height)), input.SubsampleRatio)

		coeffs, offset, filterLength := createWeightsNearest(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX)
		in := imageYCbCrToYCC(input)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*ycc)
			go func() {
				done <- nearestYCbCr(in, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		coeffs, offset, filterLength = createWeightsNearest(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*ycc)
			go func() {
				done <- nearestYCbCr(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result.YCbCr(), nil

	case *image.RGBA64:
		// 16-bit precision
		temp := image.NewRGBA64(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewRGBA64(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeightsNearest(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.RGBA64)
			go func() {
				done <- nearestRGBA64(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeightsNearest(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.RGBA64)
			go func() {
				done <- nearestGeneric(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	case *image.Gray:
		// 8-bit precision
		temp := image.NewGray(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewGray(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeightsNearest(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.Gray)
			go func() {
				done <- nearestGray(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeightsNearest(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.Gray)
			go func() {
				done <- nearestGray(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	case *image.Gray16:
		// 16-bit precision
		temp := image.NewGray16(image.Rect(0, 0, input.Bounds().Dy(), int(width)))
		result := image.NewGray16(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeightsNearest(temp.Bounds().Dy(), input.Bounds().Min.X, taps, blur, scaleX)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.Gray16)
			go func() {
				done <- nearestGray16(input, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeightsNearest(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.Gray16)
			go func() {
				done <- nearestGray16(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil

	default:
		// 16-bit precision
		temp := image.NewRGBA64(image.Rect(0, 0, img.Bounds().Dy(), int(width)))
		result := image.NewRGBA64(image.Rect(0, 0, int(width), int(height)))

		// horizontal filter, results in transposed temporary image
		coeffs, offset, filterLength := createWeightsNearest(temp.Bounds().Dy(), img.Bounds().Min.X, taps, blur, scaleX)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(temp, i, cpus).(*image.RGBA64)
			go func() {
				done <- nearestGeneric(img, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}

		// horizontal filter on transposed image, result is not transposed
		coeffs, offset, filterLength = createWeightsNearest(result.Bounds().Dy(), temp.Bounds().Min.X, taps, blur, scaleY)
		for i := 0; i < cpus; i++ {
			slice := makeSlice(result, i, cpus).(*image.RGBA64)
			go func() {
				done <- nearestRGBA64(temp, slice, coeffs, offset, filterLength)
			}()
		}
		for i := 0; i < cpus; i++ {
			if err := <-done; err != nil {
				return nil, err
			}
		}
		return result, nil
	}

}

// Calculates scaling factors using old and new image dimensions.
func calcFactors(width, height uint, oldWidth, oldHeight float64) (scaleX, scaleY float64) {
	if width == 0 {
		if height == 0 {
			scaleX = 1.0
			scaleY = 1.0
		} else {
			scaleY = oldHeight / float64(height)
			scaleX = scaleY
		}
	} else {
		scaleX = oldWidth / float64(width)
		if height == 0 {
			scaleY = scaleX
		} else {
			scaleY = oldHeight / float64(height)
		}
	}
	return
}

type imageWithSubImage interface {
	image.Image
	SubImage(image.Rectangle) image.Image
}

func makeSlice(img imageWithSubImage, i, n int) image.Image {
	return img.SubImage(image.Rect(img.Bounds().Min.X, img.Bounds().Min.Y+i*img.Bounds().Dy()/n, img.Bounds().Max.X, img.Bounds().Min.Y+(i+1)*img.Bounds().Dy()/n))
}
