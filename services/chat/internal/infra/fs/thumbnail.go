package fs

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"teamie/services/chat/internal/domain"
)

const thumbnailDecodeDeadline = 2 * time.Second

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr *ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

// GenerateThumbnail decodes supported raster formats (PNG, JPEG, GIF, WebP)
// within strict dimension, pixel-area, byte, and 2-second time budgets,
// normalizes JPEG EXIF orientation, downscales to <= 640px longest side without
// upscaling, and re-encodes a clean metadata-free thumbnail <= 256 KiB.
// Returns nil on unsupported formats, oversize inputs, or decode/encode failure
// so the caller still accepts the upload as an ordinary file attachment.
func GenerateThumbnail(ctx context.Context, mediaType string, content []byte) *domain.AttachmentThumbnail {
	if len(content) == 0 || len(content) > domain.AttachmentBytes {
		return nil
	}
	baseType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		baseType = mediaType
	}
	switch baseType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil
	}

	decodeCtx, cancel := context.WithTimeout(ctx, thumbnailDecodeDeadline)
	defer cancel()

	cfg, err := decodeConfig(decodeCtx, baseType, content)
	if err != nil {
		return nil
	}
	rawW, rawH := cfg.Width, cfg.Height
	if rawW < 1 || rawH < 1 || rawW > domain.MaxImageDimension || rawH > domain.MaxImageDimension {
		return nil
	}
	if int64(rawW)*int64(rawH) > domain.MaxImageArea {
		return nil
	}

	orientation := 1
	if baseType == "image/jpeg" {
		orientation = parseJPEGExifOrientation(content)
	}
	displayW, displayH := rawW, rawH
	if orientation >= 5 && orientation <= 8 {
		displayW, displayH = rawH, rawW
	}

	src, err := decodeFirstFrame(decodeCtx, baseType, content)
	if err != nil || decodeCtx.Err() != nil {
		return nil
	}
	bounds := src.Bounds()
	if bounds.Dx() != rawW || bounds.Dy() != rawH {
		return nil
	}

	thumbRawW, thumbRawH := fitDimension(rawW, rawH, domain.MaxThumbnailDimension)
	var scaled *image.NRGBA
	if thumbRawW == rawW && thumbRawH == rawH {
		scaled = image.NewNRGBA(image.Rect(0, 0, rawW, rawH))
		draw.Draw(scaled, scaled.Bounds(), src, bounds.Min, draw.Src)
	} else {
		scaled = image.NewNRGBA(image.Rect(0, 0, thumbRawW, thumbRawH))
		draw.BiLinear.Scale(scaled, scaled.Bounds(), src, bounds, draw.Src, nil)
	}
	if decodeCtx.Err() != nil {
		return nil
	}

	oriented := applyExifOrientation(scaled, orientation)
	outType, outBytes, ok := encodeCleanThumbnail(decodeCtx, oriented)
	if !ok {
		return nil
	}
	return &domain.AttachmentThumbnail{
		Width:     displayW,
		Height:    displayH,
		MediaType: outType,
		Content:   outBytes,
	}
}

func decodeConfig(ctx context.Context, baseType string, content []byte) (image.Config, error) {
	r := &ctxReader{ctx: ctx, r: bytes.NewReader(content)}
	switch baseType {
	case "image/png":
		return png.DecodeConfig(r)
	case "image/jpeg":
		return jpeg.DecodeConfig(r)
	case "image/gif":
		return gif.DecodeConfig(r)
	case "image/webp":
		return webp.DecodeConfig(r)
	default:
		return image.Config{}, image.ErrFormat
	}
}

func decodeFirstFrame(ctx context.Context, baseType string, content []byte) (image.Image, error) {
	r := &ctxReader{ctx: ctx, r: bytes.NewReader(content)}
	switch baseType {
	case "image/png":
		return png.Decode(r)
	case "image/jpeg":
		return jpeg.Decode(r)
	case "image/gif":
		return gif.Decode(r)
	case "image/webp":
		return webp.Decode(r)
	default:
		return nil, image.ErrFormat
	}
}

func fitDimension(w, h, maxSide int) (int, int) {
	if w <= maxSide && h <= maxSide {
		return w, h
	}
	if w >= h {
		nh := (h*maxSide + w/2) / w
		if nh < 1 {
			nh = 1
		}
		return maxSide, nh
	}
	nw := (w*maxSide + h/2) / h
	if nw < 1 {
		nw = 1
	}
	return nw, maxSide
}

func applyExifOrientation(src *image.NRGBA, orientation int) *image.NRGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	switch orientation {
	case 2:
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetNRGBA(w-1-x, y, src.NRGBAAt(x, y))
			}
		}
		return dst
	case 3:
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetNRGBA(w-1-x, h-1-y, src.NRGBAAt(x, y))
			}
		}
		return dst
	case 4:
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetNRGBA(x, h-1-y, src.NRGBAAt(x, y))
			}
		}
		return dst
	case 5:
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetNRGBA(y, x, src.NRGBAAt(x, y))
			}
		}
		return dst
	case 6:
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetNRGBA(h-1-y, x, src.NRGBAAt(x, y))
			}
		}
		return dst
	case 7:
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetNRGBA(h-1-y, w-1-x, src.NRGBAAt(x, y))
			}
		}
		return dst
	case 8:
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetNRGBA(y, w-1-x, src.NRGBAAt(x, y))
			}
		}
		return dst
	default:
		return src
	}
}

func hasNonOpaqueAlpha(img *image.NRGBA) bool {
	pix := img.Pix
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0xff {
			return true
		}
	}
	return false
}

func encodeCleanThumbnail(ctx context.Context, img *image.NRGBA) (string, []byte, bool) {
	var buf bytes.Buffer
	if hasNonOpaqueAlpha(img) {
		for _, level := range []png.CompressionLevel{png.DefaultCompression, png.BestCompression} {
			if ctx.Err() != nil {
				return "", nil, false
			}
			buf.Reset()
			enc := png.Encoder{CompressionLevel: level}
			if err := enc.Encode(&buf, img); err != nil {
				return "", nil, false
			}
			if buf.Len() > 0 && buf.Len() <= domain.MaxThumbnailBytes {
				out := make([]byte, buf.Len())
				copy(out, buf.Bytes())
				return "image/png", out, true
			}
		}
		return "", nil, false
	}

	for _, quality := range []int{82, 65, 45} {
		if ctx.Err() != nil {
			return "", nil, false
		}
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return "", nil, false
		}
		if buf.Len() > 0 && buf.Len() <= domain.MaxThumbnailBytes {
			out := make([]byte, buf.Len())
			copy(out, buf.Bytes())
			return "image/jpeg", out, true
		}
	}
	return "", nil, false
}

func parseJPEGExifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xff {
			return 1
		}
		for pos < len(data) && data[pos] == 0xff {
			pos++
		}
		if pos >= len(data) {
			return 1
		}
		marker := data[pos]
		pos++
		if marker == 0xd9 || marker == 0xda {
			return 1
		}
		if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if pos+2 > len(data) {
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		if segLen < 2 || pos+segLen > len(data) {
			return 1
		}
		payload := data[pos+2 : pos+segLen]
		pos += segLen
		if marker == 0xe1 && len(payload) >= 14 && string(payload[:6]) == "Exif\x00\x00" {
			if o := parseTIFFOrientation(payload[6:]); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

func parseTIFFOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(tiff[2:4]) != 0x002a {
		return 1
	}
	ifdOffset := int(bo.Uint32(tiff[4:8]))
	if ifdOffset < 8 || ifdOffset+2 > len(tiff) {
		return 1
	}
	count := int(bo.Uint16(tiff[ifdOffset : ifdOffset+2]))
	entries := tiff[ifdOffset+2:]
	for i := 0; i < count && (i+1)*12 <= len(entries); i++ {
		entry := entries[i*12 : (i+1)*12]
		tag := bo.Uint16(entry[0:2])
		fieldType := bo.Uint16(entry[2:4])
		numValues := bo.Uint32(entry[4:8])
		if tag == 0x0112 && fieldType == 3 && numValues == 1 {
			val := int(bo.Uint16(entry[8:10]))
			if val >= 1 && val <= 8 {
				return val
			}
			return 1
		}
	}
	return 1
}
