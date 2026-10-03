package imagehash

import (
	"image"
	"image/color"
	"math"
	"sync"
	"testing"
)

// patternImage 按归一化坐标生成确定性的平滑图案：同一图案可生成任意尺寸。
func patternImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		v := (float64(y) + 0.5) / float64(h)
		for x := 0; x < w; x++ {
			u := (float64(x) + 0.5) / float64(w)
			f := 0.50*u + 0.20*v + 0.18*math.Sin(4*math.Pi*u) +
				0.25*math.Exp(-((u-0.32)*(u-0.32)+(v-0.32)*(v-0.32))/0.08) -
				0.20*math.Exp(-((u-0.72)*(u-0.72)+(v-0.68)*(v-0.68))/0.03)
			img.SetRGBA(x, y, color.RGBA{clampByte(f), clampByte(f), clampByte(f), 255})
		}
	}
	return img
}

// solidImage 生成单色图，用作"与图案差异大"的对照。
func solidImage(w, h int, v uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func clampByte(f float64) uint8 {
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return uint8(f*255 + 0.5)
}

func Test_F62_SolidVsPatternDistance(t *testing.T) {
	solid := solidImage(128, 128, 128)
	pattern := patternImage(128, 128)

	cases := []struct {
		name string
		fn   func(image.Image) Hash
	}{
		{"aHash", AverageHash},
		{"dHash", DifferenceHash},
		{"pHash", PerceptualHash},
	}
	for _, tc := range cases {
		got := Distance(tc.fn(solid), tc.fn(pattern))
		if got < 16 {
			t.Errorf("%s solid vs pattern distance = %d, want large (>=16)", tc.name, got)
		}
	}
}

func Test_F62_ScaledVariantDistance(t *testing.T) {
	small := patternImage(64, 64)
	large := patternImage(200, 200)

	cases := []struct {
		name string
		fn   func(image.Image) Hash
	}{
		{"aHash", AverageHash},
		{"dHash", DifferenceHash},
		{"pHash", PerceptualHash},
	}
	for _, tc := range cases {
		got := Distance(tc.fn(small), tc.fn(large))
		if got > 8 {
			t.Errorf("%s scaled-variant distance = %d, want small (<=8)", tc.name, got)
		}
	}
}

func Test_F62_DistanceSymmetry(t *testing.T) {
	samples := []Hash{0, 1, 0x00FF00FF00FF00FF, 0xFFFFFFFFFFFFFFFF, 0x123456789ABCDEF0}
	for _, a := range samples {
		if d := Distance(a, a); d != 0 {
			t.Errorf("Distance(%#x, itself) = %d, want 0", uint64(a), d)
		}
		for _, b := range samples {
			if x, y := Distance(a, b), Distance(b, a); x != y {
				t.Errorf("Distance not symmetric: %d vs %d for %#x,%#x", x, y, uint64(a), uint64(b))
			}
		}
	}
	if d := Distance(0, 0xFFFFFFFFFFFFFFFF); d != 64 {
		t.Errorf("Distance(all-0, all-1) = %d, want 64", d)
	}
}

func Test_F62_DeduperExactAndNear(t *testing.T) {
	d := NewDeduper(8, 16)
	base := Hash(0x1234)

	if d.Seen(base) {
		t.Fatal("first sighting must return false")
	}
	if !d.Seen(base) {
		t.Fatal("exact duplicate must return true")
	}
	if got := d.Len(); got != 1 {
		t.Fatalf("Len after duplicate = %d, want 1", got)
	}

	near := Hash(uint64(base) ^ 0b1111) // 汉明距离 4，在阈值内
	if !d.Seen(near) {
		t.Fatal("near duplicate (distance 4) must return true")
	}
	if got := d.Len(); got != 1 {
		t.Fatalf("near duplicate should not add an entry, Len = %d", got)
	}

	unrelated := Hash(0xFFFFFFFFFFFFFFFF) // 与 base 距离 59，远超阈值
	if d.Seen(unrelated) {
		t.Fatal("unrelated hash must return false")
	}
	if got := d.Len(); got != 2 {
		t.Fatalf("Len after unrelated = %d, want 2", got)
	}
}

func Test_F62_DeduperBound(t *testing.T) {
	const maxEntries = 8
	d := NewDeduper(0, maxEntries)
	for i := 0; i < maxEntries*2; i++ {
		if d.Seen(Hash(i)) {
			t.Fatalf("distinct hash %d flagged as duplicate", i)
		}
	}
	if got := d.Len(); got != maxEntries {
		t.Fatalf("Len = %d, want %d (oldest evicted)", got, maxEntries)
	}
	if got := d.Len(); got > maxEntries {
		t.Fatalf("Len = %d exceeds maxEntries %d", got, maxEntries)
	}
	// 只保留了最后 maxEntries 个，最旧的已被淘汰。
	if d.Seen(Hash(0)) {
		t.Fatal("evicted hash 0 must no longer match")
	}
}

func Test_F62_DeduperConcurrentBound(t *testing.T) {
	const maxEntries = 32
	d := NewDeduper(4, maxEntries)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				d.Seen(Hash(uint64(g)*1000 + uint64(i)))
			}
		}(g)
	}
	wg.Wait()

	if got := d.Len(); got > maxEntries {
		t.Fatalf("Len = %d exceeds maxEntries %d", got, maxEntries)
	}
}
