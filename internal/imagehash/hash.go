// Package imagehash 实现感知哈希（perceptual hash）与近似图片去重。
//
// 三种哈希都输出 64 位：aHash（均值）、dHash（差分）、pHash（DCT 低频）。
// 它们只依赖标准库 image，输入图片的解码（png/jpeg）由调用方负责。
package imagehash

import (
	"image"
	"image/color"
	"math"
	"math/bits"
	"sort"
)

// Hash 是 64 位感知哈希。不同算法产生的 Hash 语义不同，不可交叉比较。
type Hash uint64

const (
	// hashBits 是每种哈希的位数。
	hashBits = 64

	// aSize 是 aHash 的灰度网格边长（8x8=64 位）。
	aSize = 8
	// dWidth/dHeight 是 dHash 的网格：9 列 x 8 行，逐行比较相邻列得 8x8 位。
	dWidth  = 9
	dHeight = 8
	// pSize 是 pHash 做 DCT 前的灰度网格边长。
	pSize = 32
	// pLow 是 pHash 保留的低频系数边长（8x8=64 位）。
	pLow = 8
)

// grayGrid 用面积平均法把 img 缩放到 w x h 的灰度网格。
//
// 面积平均对缩放不敏感：同一连续图案的不同分辨率会得到近似相同的网格，
// 这正是"缩放/轻微压缩后仍命中"的基础。上采样时源矩形退化会自动补成 1 像素。
func grayGrid(img image.Image, w, h int) []uint8 {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := make([]uint8, w*h)
	if sw <= 0 || sh <= 0 {
		return out
	}
	for ty := 0; ty < h; ty++ {
		y0 := b.Min.Y + ty*sh/h
		y1 := b.Min.Y + (ty+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for tx := 0; tx < w; tx++ {
			x0 := b.Min.X + tx*sw/w
			x1 := b.Min.X + (tx+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sum, n int
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					g := color.GrayModel.Convert(img.At(x, y)).(color.Gray)
					sum += int(g.Y)
					n++
				}
			}
			avg := (sum + n/2) / n
			if avg < 0 {
				avg = 0
			}
			if avg > 255 {
				avg = 255
			}
			//nolint:gosec // G115：color.Gray 的 Y 已是 0..255，上面又做了钳制。
			out[ty*w+tx] = uint8(avg)
		}
	}
	return out
}

// AverageHash 计算 aHash：把图片缩到 8x8 灰度取均值，像素不低于均值的位记 1。
func AverageHash(img image.Image) Hash {
	g := grayGrid(img, aSize, aSize)
	var sum int
	for _, v := range g {
		sum += int(v)
	}
	mean := float64(sum) / float64(len(g))
	var h Hash
	for i, v := range g {
		if float64(v) >= mean {
			h |= 1 << uint(i)
		}
	}
	return h
}

// DifferenceHash 计算 dHash：缩到 9x8 灰度，逐行比较相邻两列，左亮于右记 1。
func DifferenceHash(img image.Image) Hash {
	g := grayGrid(img, dWidth, dHeight)
	var h Hash
	for y := 0; y < dHeight; y++ {
		row := g[y*dWidth : (y+1)*dWidth]
		for x := 0; x < dWidth-1; x++ {
			if row[x] > row[x+1] {
				h |= 1 << uint(y*(dWidth-1)+x)
			}
		}
	}
	return h
}

// PerceptualHash 计算 pHash：对 32x32 灰度做二维 DCT-II，取左上 8x8 低频系数，
// 以这 64 个系数的中位数为阈值置位。
//
// 取舍：完整 pHash 常用 32x32 DCT 并剔除 DC 后取 63 位；这里为保持 64 位定长，
// 保留 DC（通常置 1，近似一个常量位），其余 63 位仍承载低频结构。实现只计算前
// 8 个频率分量，省去高频；并把相对最大系数小到 1e-9 的浮点噪声置零，避免
// 纯色图退化成由舍入误差决定哈希。
func PerceptualHash(img image.Image) Hash {
	g := grayGrid(img, pSize, pSize)

	// 预计算 cos(pi*(2n+1)*k/(2N))，k=0..pLow-1。
	cos := make([][]float64, pLow)
	for k := 0; k < pLow; k++ {
		cos[k] = make([]float64, pSize)
		for n := 0; n < pSize; n++ {
			cos[k][n] = math.Cos(math.Pi * float64(2*n+1) * float64(k) / float64(2*pSize))
		}
	}

	// 先行后列做可分离变换，只保留前 pLow 个频率分量。
	rows := make([][]float64, pSize)
	for y := 0; y < pSize; y++ {
		rows[y] = make([]float64, pLow)
		for k := 0; k < pLow; k++ {
			var s float64
			for x := 0; x < pSize; x++ {
				s += float64(g[y*pSize+x]) * cos[k][x]
			}
			rows[y][k] = s
		}
	}
	coeff := make([]float64, pLow*pLow)
	var maxAbs float64
	for k := 0; k < pLow; k++ {
		for l := 0; l < pLow; l++ {
			var s float64
			for y := 0; y < pSize; y++ {
				s += rows[y][k] * cos[l][y]
			}
			coeff[k*pLow+l] = s
			if a := math.Abs(s); a > maxAbs {
				maxAbs = a
			}
		}
	}
	noise := maxAbs * 1e-9
	for i, c := range coeff {
		if math.Abs(c) < noise {
			coeff[i] = 0
		}
	}

	sorted := append([]float64(nil), coeff...)
	sort.Float64s(sorted)
	median := (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2

	var h Hash
	for i, c := range coeff {
		if c > median {
			h |= 1 << uint(i)
		}
	}
	return h
}

// Distance 返回两个哈希的汉明距离（不同位数），范围 0..64。
//
// 只有同一算法产生的哈希之间比较才有意义。
func Distance(a, b Hash) int {
	return bits.OnesCount64(uint64(a ^ b))
}
