// Package vector 提供向量的编解码和 cosine 计算。
// BLOB 编码统一 little-endian float32。
package vector

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const bytesPerFloat = 4

func Encode(v []float32) []byte {
	buf := make([]byte, len(v)*bytesPerFloat)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*bytesPerFloat:], math.Float32bits(f))
	}
	return buf
}

func Decode(b []byte) ([]float32, error) {
	if len(b)%bytesPerFloat != 0 {
		return nil, fmt.Errorf("vector: blob length %d not multiple of %d", len(b), bytesPerFloat)
	}
	n := len(b) / bytesPerFloat
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*bytesPerFloat:]))
	}
	return v, nil
}

// ErrDimMismatch: 维度不一致，通常是换了 embedding 模型忘清库。
var ErrDimMismatch = errors.New("vector: dimension mismatch")

// Cosine 返回余弦相似度；任一零向量返回 0（避免 NaN 污染 topK 堆）。
func Cosine(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, ErrDimMismatch
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb))), nil
}
