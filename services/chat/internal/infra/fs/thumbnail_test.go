package fs

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image/color"
	"testing"
)

func TestGenerateThumbnailBoundsDecodedImageMemory(t *testing.T) {
	ctx := context.Background()

	// 1. Estimated decoded bytes accounts for pixel format (8 B/px for RGBA64/NRGBA64).
	if got := estimatedDecodedBytes(color.NRGBA64Model, 6000, 6000); got != 288_000_000 {
		t.Fatalf("expected 288 MB for 6000x6000 NRGBA64, got %d", got)
	}
	if got := estimatedDecodedBytes(color.NRGBAModel, 6000, 6000); got != 144_000_000 {
		t.Fatalf("expected 144 MB for 6000x6000 NRGBA, got %d", got)
	}

	// 2. A valid PNG header declaring 6000x6000 RGBA16 passes dimension/area checks
	// (6000 <= 16384 and 36M <= 40M) but requires 288 MB to decode and must be rejected
	// before full frame allocation.
	var pngBuf bytes.Buffer
	pngBuf.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 6000)
	binary.BigEndian.PutUint32(ihdr[4:8], 6000)
	ihdr[8] = 16 // 16-bit depth -> NRGBA64Model (8 bytes/pixel)
	ihdr[9] = 6  // RGBA
	_ = binary.Write(&pngBuf, binary.BigEndian, uint32(len(ihdr)))
	pngBuf.WriteString("IHDR")
	pngBuf.Write(ihdr)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte("IHDR"))
	_, _ = crc.Write(ihdr)
	_ = binary.Write(&pngBuf, binary.BigEndian, crc.Sum32())

	if thumb := GenerateThumbnail(ctx, "image/png", pngBuf.Bytes()); thumb != nil {
		t.Fatalf("expected over-budget 6000x6000 RGBA16 PNG to skip thumbnail generation, got %+v", thumb)
	}
}
