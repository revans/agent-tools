package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

const (
	maxFileBytes  = 100 << 20 // sanity cap on the input file
	maxPixels     = 100_000_000
	startQuality  = 92
	qualityFloor  = 60
	qualityStep   = 5
	shrinkFactor  = 0.9
	aspectEpsilon = 1e-9
)

// Info describes an image file.
type Info struct {
	Format      string `json:"format"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Bytes       int64  `json:"bytes"`
	HasAlpha    bool   `json:"has_alpha"`
	Animated    bool   `json:"animated"`
	Orientation int    `json:"orientation,omitempty"`
}

// loaded is a file read into memory with its Info.
type loaded struct {
	Info
	data []byte
}

// displayDims are the dimensions after the EXIF orientation is applied.
func (i Info) displayDims() (int, int) {
	if i.Orientation >= 5 {
		return i.Height, i.Width
	}
	return i.Width, i.Height
}

func isAnimatedWebP(b []byte) bool {
	// RIFF....WEBPVP8X, flags byte at offset 20, animation bit 0x02.
	return len(b) >= 30 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" &&
		string(b[12:16]) == "VP8X" && b[20]&0x02 != 0
}

// load reads and inspects a file. Dimensions are checked with DecodeConfig
// before any pixel is decoded.
func load(path string) (*loaded, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not an image", path)
	}
	if fi.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d", path, fi.Size(), maxFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxFileBytes)
	}
	l := &loaded{data: data}
	l.Bytes = int64(len(data))
	if isAnimatedWebP(data) {
		l.Format, l.Animated = "webp", true
		// VP8X canvas size: 24-bit little-endian, stored minus one.
		l.Width = int(binary.LittleEndian.Uint32(append(append([]byte{}, data[24:27]...), 0))) + 1
		l.Height = int(binary.LittleEndian.Uint32(append(append([]byte{}, data[27:30]...), 0))) + 1
		l.HasAlpha = data[20]&0x10 != 0
		return l, checkPixels(path, l.Width, l.Height)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s is not a decodable image (%v)", path, err)
	}
	if err := checkPixels(path, cfg.Width, cfg.Height); err != nil {
		return nil, err
	}
	l.Format, l.Width, l.Height = format, cfg.Width, cfg.Height
	switch format {
	case "jpeg":
		l.Orientation = jpegOrientation(data)
	case "gif":
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%s is not a decodable image (%v)", path, err)
		}
		l.Animated = len(g.Image) > 1
		if g.Config.ColorModel != nil {
			if p, ok := g.Config.ColorModel.(color.Palette); ok {
				for _, c := range p {
					if _, _, _, a := c.RGBA(); a < 0xffff {
						l.HasAlpha = true
					}
				}
			}
		}
	case "png", "webp":
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%s is not a decodable image (%v)", path, err)
		}
		l.HasAlpha = hasAlpha(img)
	}
	return l, nil
}

func checkPixels(path string, w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%s has no pixels", path)
	}
	if int64(w)*int64(h) > maxPixels {
		return fmt.Errorf("%s is %dx%d, over the %d pixel limit", path, w, h, maxPixels)
	}
	return nil
}

func hasAlpha(img image.Image) bool {
	switch m := img.(type) {
	case *image.Gray, *image.Gray16, *image.YCbCr, *image.CMYK:
		return false
	case *image.NRGBA:
		for i := 3; i < len(m.Pix); i += 4 {
			if m.Pix[i] != 0xff {
				return true
			}
		}
		return false
	case *image.RGBA:
		for i := 3; i < len(m.Pix); i += 4 {
			if m.Pix[i] != 0xff {
				return true
			}
		}
		return false
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return true
			}
		}
	}
	return false
}

// checkInfo lists the ways an image breaks a profile. Empty means it fits.
func checkInfo(p *Profile, in Info) []string {
	problems := []string{}
	ok := false
	for _, f := range p.Formats {
		if f == in.Format {
			ok = true
		}
	}
	if !ok {
		problems = append(problems, fmt.Sprintf("format %s is not accepted (accepted: %s)", in.Format, strings.Join(p.Formats, ", ")))
	}
	if p.MaxBytes > 0 && in.Bytes > p.MaxBytes {
		problems = append(problems, fmt.Sprintf("file is %d bytes; the limit is %d", in.Bytes, p.MaxBytes))
	}
	if in.Orientation > 1 {
		problems = append(problems, fmt.Sprintf("EXIF orientation %d is set; viewers disagree about applying it, so it should be baked into the pixels", in.Orientation))
	}
	dw, dh := in.displayDims()
	problems = append(problems, dimProblems(p, dw, dh)...)
	return problems
}

func dimProblems(p *Profile, w, h int) []string {
	var pr []string
	if p.MinWidth > 0 && w < p.MinWidth {
		pr = append(pr, fmt.Sprintf("width %d is below the minimum %d", w, p.MinWidth))
	}
	if p.MaxWidth > 0 && w > p.MaxWidth {
		pr = append(pr, fmt.Sprintf("width %d is above the maximum %d", w, p.MaxWidth))
	}
	if p.MinHeight > 0 && h < p.MinHeight {
		pr = append(pr, fmt.Sprintf("height %d is below the minimum %d", h, p.MinHeight))
	}
	if p.MaxHeight > 0 && h > p.MaxHeight {
		pr = append(pr, fmt.Sprintf("height %d is above the maximum %d", h, p.MaxHeight))
	}
	if h > 0 {
		r := float64(w) / float64(h)
		if p.MinAspect > 0 && r < p.MinAspect-aspectEpsilon {
			pr = append(pr, fmt.Sprintf("aspect ratio %.3f is below the minimum %.3f", r, p.MinAspect))
		}
		if p.MaxAspect > 0 && r > p.MaxAspect+aspectEpsilon {
			pr = append(pr, fmt.Sprintf("aspect ratio %.3f is above the maximum %.3f", r, p.MaxAspect))
		}
	}
	return pr
}

// ---- fit ----

type fitOptions struct {
	Out        string
	Mode       string // pad or crop
	Background color.NRGBA
	BGHex      string
	Force      bool
}

// FitResult is what `media fit` prints.
type FitResult struct {
	Out     string   `json:"out"`
	Format  string   `json:"format"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	Bytes   int64    `json:"bytes"`
	Changes []string `json:"changes"`
	Note    string   `json:"note,omitempty"`
}

// encodeHook lets tests corrupt the encoded bytes to exercise the final check.
var encodeHook func([]byte) []byte

func parseBackground(s string) (color.NRGBA, error) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, fmt.Errorf("background %q must look like #RRGGBB", s)
	}
	var r, g, b uint8
	if _, err := fmt.Sscanf(s[1:], "%02x%02x%02x", &r, &g, &b); err != nil {
		return color.NRGBA{}, fmt.Errorf("background %q must look like #RRGGBB", s)
	}
	return color.NRGBA{R: r, G: g, B: b, A: 255}, nil
}

var extFor = map[string]string{"jpeg": "jpg", "png": "png", "gif": "gif", "webp": "webp"}
var encodable = map[string]bool{"jpeg": true, "png": true}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// chooseFormat keeps the input's format when the profile allows it and we can
// write it; otherwise the first writable format the profile lists.
func chooseFormat(p *Profile, in string, rewrite bool) (string, error) {
	if has(p.Formats, in) && (!rewrite || encodable[in]) {
		return in, nil
	}
	for _, f := range p.Formats {
		if encodable[f] {
			return f, nil
		}
	}
	return "", fmt.Errorf("profile %q accepts only %s, and this tool can write only jpeg and png", p.Name, strings.Join(p.Formats, ", "))
}

func fitFile(p *Profile, in string, opt fitOptions) (*FitResult, error) {
	l, err := load(in)
	if err != nil {
		return nil, err
	}
	mode := opt.Mode
	if mode == "" {
		mode = "pad"
	}
	out := opt.Out
	userOut := opt.Out != ""

	problems := checkInfo(p, l.Info)
	if len(problems) == 0 {
		if !userOut {
			return &FitResult{Out: in, Format: l.Format, Width: l.Width, Height: l.Height, Bytes: l.Bytes,
				Changes: []string{}, Note: "already satisfies the profile; nothing written"}, nil
		}
		if err := prepareOut(in, out, opt.Force); err != nil {
			return nil, err
		}
		res, err := writeOut(p, out, l.data, opt.Force)
		if err != nil {
			return nil, err
		}
		res.Changes = []string{}
		res.Note = "already satisfies the profile; copied unchanged"
		return res, nil
	}
	if l.Animated {
		return nil, fmt.Errorf("%s is an animated %s and this tool cannot resize or convert animations; it breaks the %q profile: %s",
			in, strings.ToUpper(l.Format), p.Name, strings.Join(problems, "; "))
	}

	img, _, err := image.Decode(bytes.NewReader(l.data))
	if err != nil {
		return nil, fmt.Errorf("%s is not a decodable image (%v)", in, err)
	}
	var changes []string
	work := toNRGBA(img)
	if l.Orientation > 1 {
		work = orient(work, l.Orientation)
		changes = append(changes, fmt.Sprintf("applied EXIF orientation %d (%s)", l.Orientation, orientationName[l.Orientation]))
	}

	rewrite := true
	outFmt, err := chooseFormat(p, l.Format, rewrite)
	if err != nil {
		return nil, err
	}
	if outFmt != l.Format {
		changes = append(changes, fmt.Sprintf("converted %s to %s", strings.ToUpper(l.Format), strings.ToUpper(outFmt)))
	}
	if outFmt == "jpeg" {
		if l.HasAlpha {
			changes = append(changes, "flattened transparency onto "+opt.BGHex)
		}
		work = flatten(work, opt.Background)
	}

	// Aspect.
	w, h := work.Bounds().Dx(), work.Bounds().Dy()
	if nw, nh, kind := aspectTarget(p, w, h, mode); kind != "" {
		if mode == "crop" {
			work = crop(work, nw, nh)
			changes = append(changes, fmt.Sprintf("cropped %dx%d to %dx%d to reach the %s aspect ratio %.3f", w, h, nw, nh, kind, aspectOf(p, kind)))
		} else {
			work = pad(work, nw, nh, opt.Background)
			changes = append(changes, fmt.Sprintf("padded %dx%d to %dx%d to reach the %s aspect ratio %.3f", w, h, nw, nh, kind, aspectOf(p, kind)))
		}
	}

	// Scale to the dimension limits.
	w, h = work.Bounds().Dx(), work.Bounds().Dy()
	nw, nh, err := scaleTarget(p, w, h)
	if err != nil {
		return nil, err
	}
	if nw != w || nh != h {
		work = scale(work, nw, nh)
		changes = append(changes, fmt.Sprintf("scaled %dx%d to %dx%d", w, h, nw, nh))
	}

	data, outFmt, changes, err := encodeUnderLimit(p, work, outFmt, opt, changes)
	if err != nil {
		return nil, err
	}
	if encodeHook != nil {
		data = encodeHook(data)
	}
	if !userOut {
		out = defaultOut(in, p.Name, outFmt)
	}
	if err := prepareOut(in, out, opt.Force); err != nil {
		return nil, err
	}
	res, err := writeOut(p, out, data, opt.Force)
	if err != nil {
		return nil, err
	}
	res.Changes = changes
	return res, nil
}

func defaultOut(in, profile, format string) string {
	base := filepath.Base(in)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(filepath.Dir(in), stem+"."+profile+"."+extFor[format])
}

var orientationName = map[int]string{
	2: "mirrored", 3: "rotated 180", 4: "flipped vertically", 5: "transposed",
	6: "rotated 90 clockwise", 7: "transverse", 8: "rotated 90 counterclockwise",
}

func aspectOf(p *Profile, kind string) float64 {
	if kind == "minimum" {
		return p.MinAspect
	}
	return p.MaxAspect
}

// aspectTarget returns the new canvas size when the aspect ratio is outside the
// profile's range, changing it only as far as the nearest allowed ratio. kind is
// "" when nothing needs to change.
func aspectTarget(p *Profile, w, h int, mode string) (nw, nh int, kind string) {
	r := float64(w) / float64(h)
	switch {
	case p.MinAspect > 0 && r < p.MinAspect-aspectEpsilon: // too tall
		kind = "minimum"
		if mode == "crop" {
			return w, maxInt(1, int(math.Floor(float64(w)/p.MinAspect+aspectEpsilon))), kind
		}
		return int(math.Ceil(float64(h)*p.MinAspect - aspectEpsilon)), h, kind
	case p.MaxAspect > 0 && r > p.MaxAspect+aspectEpsilon: // too wide
		kind = "maximum"
		if mode == "crop" {
			return maxInt(1, int(math.Floor(float64(h)*p.MaxAspect+aspectEpsilon))), h, kind
		}
		return w, int(math.Ceil(float64(w)/p.MaxAspect - aspectEpsilon)), kind
	}
	return w, h, ""
}

// scaleTarget decides the final size: down to meet a maximum, up only to meet a
// minimum, never otherwise.
func scaleTarget(p *Profile, w, h int) (int, int, error) {
	s := 1.0
	if p.MaxWidth > 0 && w > p.MaxWidth {
		s = math.Min(s, float64(p.MaxWidth)/float64(w))
	}
	if p.MaxHeight > 0 && h > p.MaxHeight {
		s = math.Min(s, float64(p.MaxHeight)/float64(h))
	}
	if s == 1.0 {
		if p.MinWidth > 0 && w < p.MinWidth {
			s = math.Max(s, float64(p.MinWidth)/float64(w))
		}
		if p.MinHeight > 0 && h < p.MinHeight {
			s = math.Max(s, float64(p.MinHeight)/float64(h))
		}
	}
	if s == 1.0 {
		return w, h, nil
	}
	nw, nh := scaleDims(w, h, s)
	nw, nh = fixAspect(p, nw, nh)
	if pr := dimProblems(p, nw, nh); len(pr) > 0 {
		return 0, 0, fmt.Errorf("cannot reach a size inside the %q limits from %dx%d: %s", p.Name, w, h, strings.Join(pr, "; "))
	}
	return nw, nh, nil
}

func scaleDims(w, h int, s float64) (int, int) {
	return maxInt(1, int(math.Round(float64(w)*s))), maxInt(1, int(math.Round(float64(h)*s)))
}

// fixAspect nudges the height by a pixel or so when rounding pushed the ratio
// just outside the profile's range.
func fixAspect(p *Profile, w, h int) (int, int) {
	r := float64(w) / float64(h)
	if p.MinAspect > 0 && r < p.MinAspect-aspectEpsilon {
		h = maxInt(1, int(math.Floor(float64(w)/p.MinAspect+aspectEpsilon)))
	} else if p.MaxAspect > 0 && r > p.MaxAspect+aspectEpsilon {
		h = int(math.Ceil(float64(w)/p.MaxAspect - aspectEpsilon))
	}
	return w, h
}

// encodeUnderLimit encodes the image so it fits the byte limit, stepping the
// JPEG quality down and then shrinking by 10% at a time.
func encodeUnderLimit(p *Profile, base *image.NRGBA, format string, opt fitOptions, changes []string) ([]byte, string, []string, error) {
	target := p.TargetBytes
	if target <= 0 {
		target = p.MaxBytes
	}
	bw, bh := base.Bounds().Dx(), base.Bounds().Dy()
	cur := base
	factor := 1.0
	for {
		cw, ch := cur.Bounds().Dx(), cur.Bounds().Dy()
		if format == "png" {
			data, err := encodePNG(cur)
			if err != nil {
				return nil, "", nil, err
			}
			if target <= 0 || int64(len(data)) <= target {
				return data, "png", changes, nil
			}
			if has(p.Formats, "jpeg") {
				changes = append(changes, fmt.Sprintf("PNG was %d bytes, over the %d limit; converted PNG to JPEG", len(data), target))
				format = "jpeg"
				cur = flatten(cur, opt.Background)
				base = flatten(base, opt.Background)
				continue
			}
		} else {
			q := startQuality
			for {
				data, err := encodeJPEG(cur, q)
				if err != nil {
					return nil, "", nil, err
				}
				if target <= 0 || int64(len(data)) <= target {
					if q < startQuality {
						changes = append(changes, fmt.Sprintf("recompressed at quality %d", q))
					} else {
						changes = append(changes, fmt.Sprintf("encoded JPEG at quality %d", q))
					}
					return data, "jpeg", changes, nil
				}
				if q <= qualityFloor {
					break
				}
				q = maxInt(q-qualityStep, qualityFloor)
			}
		}
		// Still too big: shrink 10% from the base image.
		factor *= shrinkFactor
		nw, nh := scaleDims(bw, bh, factor)
		nw, nh = fixAspect(p, nw, nh)
		if nw < 16 || nh < 16 || (p.MinWidth > 0 && nw < p.MinWidth) || (p.MinHeight > 0 && nh < p.MinHeight) {
			return nil, "", nil, fmt.Errorf("cannot get under %d bytes at quality %d without going below the profile's minimum size (%dx%d); the image is %dx%d now",
				target, qualityFloor, p.MinWidth, p.MinHeight, cw, ch)
		}
		cur = scale(base, nw, nh)
		changes = append(changes, fmt.Sprintf("scaled %dx%d to %dx%d to get under %d bytes", cw, ch, nw, nh, target))
	}
}

func encodeJPEG(img image.Image, q int) ([]byte, error) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ---- pixels ----

func toNRGBA(img image.Image) *image.NRGBA {
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

func flatten(img *image.NRGBA, bg color.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Over)
	return dst
}

func pad(img *image.NRGBA, w, h int, bg color.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	x, y := (w-img.Bounds().Dx())/2, (h-img.Bounds().Dy())/2
	draw.Draw(dst, image.Rect(x, y, x+img.Bounds().Dx(), y+img.Bounds().Dy()), img, img.Bounds().Min, draw.Src)
	return dst
}

func crop(img *image.NRGBA, w, h int) *image.NRGBA {
	x, y := (img.Bounds().Dx()-w)/2, (img.Bounds().Dy()-h)/2
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), img, image.Pt(x, y), draw.Src)
	return dst
}

func scale(img *image.NRGBA, w, h int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	return dst
}

// orient returns the image as it should be displayed for an EXIF orientation 1-8.
func orient(src *image.NRGBA, o int) *image.NRGBA {
	W, H := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := W, H
	if o >= 5 {
		dw, dh = H, W
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = W-1-x, y
			case 3:
				sx, sy = W-1-x, H-1-y
			case 4:
				sx, sy = x, H-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, H-1-x
			case 7:
				sx, sy = W-1-y, H-1-x
			case 8:
				sx, sy = W-1-y, x
			default:
				sx, sy = x, y
			}
			dst.SetNRGBA(x, y, src.NRGBAAt(sx, sy))
		}
	}
	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---- output ----

// prepareOut checks the destination before any heavy work.
func prepareOut(in, out string, force bool) error {
	if filepath.Clean(in) == filepath.Clean(out) {
		return fmt.Errorf("refusing to write over the input %s", in)
	}
	if ii, err := os.Stat(in); err == nil {
		if oi, err := os.Stat(out); err == nil && os.SameFile(ii, oi) {
			return fmt.Errorf("refusing to write over the input %s", in)
		}
	}
	dir := filepath.Dir(out)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("the output directory %s does not exist; create it first", dir)
	}
	if _, err := os.Lstat(out); err == nil && !force {
		return fmt.Errorf("%s already exists; use --force to overwrite it", out)
	}
	return nil
}

// writeOut writes data next to the destination, re-checks it against the
// profile, and only then moves it into place. A bad result is deleted.
func writeOut(p *Profile, out string, data []byte, force bool) (*FitResult, error) {
	tmp, err := os.CreateTemp(filepath.Dir(out), ".media-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	chk, err := load(tmpName)
	if err != nil {
		return nil, fmt.Errorf("the output failed its final check and was deleted: %v", err)
	}
	if pr := checkInfo(p, chk.Info); len(pr) > 0 {
		return nil, fmt.Errorf("the output failed its final check against %q and was deleted: %s", p.Name, strings.Join(pr, "; "))
	}
	if force {
		if err := os.Rename(tmpName, out); err != nil {
			return nil, err
		}
	} else {
		if err := os.Link(tmpName, out); err != nil {
			if os.IsExist(err) {
				return nil, fmt.Errorf("%s already exists; use --force to overwrite it", out)
			}
			f, err2 := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err2 != nil {
				if os.IsExist(err2) {
					return nil, fmt.Errorf("%s already exists; use --force to overwrite it", out)
				}
				return nil, err2
			}
			_, werr := f.Write(data)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				os.Remove(out)
				return nil, werr
			}
		}
	}
	return &FitResult{Out: out, Format: chk.Format, Width: chk.Width, Height: chk.Height, Bytes: chk.Bytes}, nil
}
