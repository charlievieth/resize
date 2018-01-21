#include <stdlib.h>
#include <stdint.h>

typedef struct {
	int64_t x;
	int64_t y;
} point;

typedef struct {
	point min;
	point max;
} rectangle;

typedef struct {
	uint8_t   *pix;
	int64_t   stride;
	rectangle rect;
} image;

static int64_t calculate_xi(const int64_t xi, const int64_t max) {
	if ((uint64_t)xi < (uint64_t)max) {
		return xi * 4;
	}
	if (xi >= max) {
		return max;
	}
	return 0;
}

static uint8_t clamp_int32(int32_t n) {
	if ((uint32_t)n < 256) {
		return (uint8_t)n;
	}
	if (n > 255) {
		return 255;
	}
	return 0;
}

int nearest_rgba(image *in, image *out, int16_t coeffs[], int64_t offset[],
	             int64_t filter_length) {

	const rectangle new_bounds = out->rect;
	const int64_t max_x = in->rect.max.x - in->rect.min.x;

	for (int64_t x = new_bounds.min.x; x < new_bounds.max.x; x++) {
		const uint8_t *row = (const uint8_t *)in->pix + (x * in->stride);
		for (int64_t y = new_bounds.min.y; y < new_bounds.max.y; y++) {
			int32_t r = 0;
			int32_t g = 0;
			int32_t b = 0;
			int32_t a = 0;
			int32_t sum = 0;
			for (int64_t i = 0; i < filter_length; i++) {
				int32_t coeff = (int32_t)coeffs[(y * filter_length) + i];
				if (coeff != 0) {
					int64_t xi = calculate_xi(offset[y] + i, max_x);
					r += coeff * (int32_t)row[xi+0];
					g += coeff * (int32_t)row[xi+1];
					b += coeff * (int32_t)row[xi+2];
					a += coeff * (int32_t)row[xi+3];
					sum += coeff;
				}
			}

			int64_t xo = (y-new_bounds.min.y)*out->stride + (x-new_bounds.min.x)*4;
			out->pix[xo+0] = clamp_int32(r / sum);
			out->pix[xo+1] = clamp_int32(g / sum);
			out->pix[xo+2] = clamp_int32(b / sum);
			out->pix[xo+3] = clamp_int32(a / sum);
		}
	}

	return 1;
}

// int main(int argc, char const *argv[]) {
// 	return 0;
// }
