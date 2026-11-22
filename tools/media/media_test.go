package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- helpers ----

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MEDIA_PROFILES", "")
	t.Setenv("HOME", t.TempDir())
}

func solid(w, h int, c color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = n.R, n.G, n.B, n.A
	}
	return img
}

func noise(w, h int) *image.NRGBA {
	r := rand.New(rand.NewSource(1))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256)), 255
	}
	return img
}

func jpegBytes(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// withEXIF inserts an APP1 EXIF segment carrying the orientation after SOI.
func withEXIF(jpg []byte, orientation int, bigEndian bool) []byte {
	var bo binary.AppendByteOrder = binary.LittleEndian
	hdr := "II"
	if bigEndian {
		bo, hdr = binary.BigEndian, "MM"
	}
	tiff := []byte(hdr)
	tiff = bo.AppendUint16(tiff, 42)
	tiff = bo.AppendUint32(tiff, 8)
	tiff = bo.AppendUint16(tiff, 1) // one entry
	tiff = bo.AppendUint16(tiff, 0x0112)
	tiff = bo.AppendUint16(tiff, 3)
	tiff = bo.AppendUint32(tiff, 1)
	tiff = bo.AppendUint16(tiff, uint16(orientation))
	tiff = append(tiff, 0, 0)
	tiff = bo.AppendUint32(tiff, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(seg)+2))
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func profileDir(t *testing.T, toml string) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, "p.toml", []byte(toml))
	t.Setenv("MEDIA_PROFILES", d)
	return d
}

func mustProfile(t *testing.T, name string) *Profile {
	t.Helper()
	p, err := findProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func white() color.NRGBA { return color.NRGBA{255, 255, 255, 255} }

func opts() fitOptions {
	return fitOptions{Mode: "pad", Background: white(), BGHex: "#FFFFFF"}
}

func fitOK(t *testing.T, p *Profile, in string, o fitOptions) *FitResult {
	t.Helper()
	r, err := fitFile(p, in, o)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	return r
}

func decodeFile(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func near(a, b color.Color, tol int) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) int {
		v := int(x>>8) - int(y>>8)
		if v < 0 {
			v = -v
		}
		return v
	}
	return d(ar, br) <= tol && d(ag, bg) <= tol && d(ab, bb) <= tol
}

func changesText(r *FitResult) string { return strings.Join(r.Changes, " | ") }

// ---- profiles ----

func TestBuiltinProfiles(t *testing.T) {
	isolate(t)
	ps, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 3 {
		t.Fatalf("got %d profiles", len(ps))
	}
	ig := mustProfile(t, "instagram")
	if len(ig.Formats) != 1 || ig.Formats[0] != "jpeg" || ig.MaxBytes != 8000000 || ig.TargetBytes != 7900000 ||
		ig.MinAspect != 0.8 || ig.MaxAspect != 1.91 || ig.MinWidth != 320 || ig.MaxWidth != 1440 {
		t.Errorf("instagram wrong: %+v", ig)
	}
	if x := mustProfile(t, "x"); x.MaxBytes != 5*1024*1024 || len(x.Formats) != 4 {
		t.Errorf("x wrong: %+v", x)
	}
	if l := mustProfile(t, "linkedin"); l.MaxBytes != 8*1024*1024 || len(l.Formats) != 3 {
		t.Errorf("linkedin wrong: %+v", l)
	}
	for _, p := range ps {
		if p.Source == "" || p.Origin != "builtin" {
			t.Errorf("%s lacks source/origin", p.Name)
		}
	}
}

func TestCustomProfilesFromEnvAndXDG(t *testing.T) {
	isolate(t)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	d := filepath.Join(xdg, "agent-tools", "media", "profiles.d")
	os.MkdirAll(d, 0o755)
	write(t, d, "a.toml", []byte("[[profile]]\nname='tiny'\nformats=['png']\nmax_bytes=1000\nsource='me'\n"))
	extra := t.TempDir()
	write(t, extra, "b.toml", []byte("[[profile]]\nname='wide'\nformats=['jpg']\nmin_aspect=1.5\nsource='me'\n"))
	t.Setenv("MEDIA_PROFILES", extra)
	ps, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 5 {
		t.Fatalf("got %d", len(ps))
	}
	w := mustProfile(t, "wide")
	if w.Formats[0] != "jpeg" || !strings.HasSuffix(w.Origin, "b.toml") {
		t.Errorf("%+v", w)
	}
}

func TestDuplicateProfileIsAnError(t *testing.T) {
	isolate(t)
	profileDir(t, "[[profile]]\nname='x'\nformats=['png']\n")
	_, err := loadProfiles()
	if err == nil || !strings.Contains(err.Error(), "already defined") || !strings.Contains(err.Error(), "p.toml") {
		t.Fatalf("want duplicate error naming the file, got %v", err)
	}
	// duplicate between two user files
	isolate(t)
	d := t.TempDir()
	write(t, d, "1.toml", []byte("[[profile]]\nname='z'\nformats=['png']\n"))
	write(t, d, "2.toml", []byte("[[profile]]\nname='z'\nformats=['png']\n"))
	t.Setenv("MEDIA_PROFILES", d)
	if _, err := loadProfiles(); err == nil || !strings.Contains(err.Error(), "2.toml") {
		t.Fatalf("got %v", err)
	}
}

func TestInvalidProfilesNameTheFile(t *testing.T) {
	bad := map[string]string{
		"no formats":    "[[profile]]\nname='a'\n",
		"bad format":    "[[profile]]\nname='a'\nformats=['bmp']\n",
		"min over max":  "[[profile]]\nname='a'\nformats=['png']\nmin_width=10\nmax_width=5\n",
		"aspect":        "[[profile]]\nname='a'\nformats=['png']\nmin_aspect=2\nmax_aspect=1\n",
		"unknown field": "[[profile]]\nname='a'\nformats=['png']\nmax_byte=5\n",
		"negative":      "[[profile]]\nname='a'\nformats=['png']\nmax_bytes=-1\n",
		"not toml":      "this is [[ not toml",
		"no name":       "[[profile]]\nformats=['png']\n",
	}
	for name, body := range bad {
		isolate(t)
		profileDir(t, body)
		_, err := loadProfiles()
		if err == nil || !strings.Contains(err.Error(), "p.toml") {
			t.Errorf("%s: want an error naming the file, got %v", name, err)
		}
	}
}

// ---- info / check ----

const tinyWebP = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

func TestInfo(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	j := write(t, d, "a.jpg", withEXIF(jpegBytes(t, solid(30, 20, color.NRGBA{9, 9, 9, 255}), 90), 6, true))
	l, err := load(j)
	if err != nil {
		t.Fatal(err)
	}
	if l.Format != "jpeg" || l.Width != 30 || l.Height != 20 || l.Orientation != 6 || l.HasAlpha || l.Animated {
		t.Errorf("%+v", l.Info)
	}
	pg := write(t, d, "a.png", pngBytes(t, solid(5, 5, color.NRGBA{1, 2, 3, 100})))
	if l, _ := load(pg); !l.HasAlpha || l.Format != "png" {
		t.Errorf("%+v", l)
	}
	op := write(t, d, "b.png", pngBytes(t, solid(5, 5, color.NRGBA{1, 2, 3, 255})))
	if l, _ := load(op); l.HasAlpha {
		t.Errorf("opaque png reported alpha")
	}
	wb, _ := base64.StdEncoding.DecodeString(tinyWebP)
	wp := write(t, d, "a.webp", wb)
	if l, err := load(wp); err != nil || l.Format != "webp" || l.Width != 1 {
		t.Errorf("%v %+v", err, l)
	}
	g := write(t, d, "a.gif", gifBytes(t, 2))
	if l, _ := load(g); !l.Animated || l.Format != "gif" {
		t.Errorf("%+v", l)
	}
	g1 := write(t, d, "b.gif", gifBytes(t, 1))
	if l, _ := load(g1); l.Animated {
		t.Errorf("single frame reported animated")
	}
}

func gifBytes(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{}
	pal := color.Palette{color.White, color.Black}
	for i := 0; i < frames; i++ {
		fr := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
		fr.SetColorIndex(i, i, 1)
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, 5)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAnimatedWebPDetected(t *testing.T) {
	b := make([]byte, 40)
	copy(b, "RIFF")
	copy(b[8:], "WEBPVP8X")
	b[20] = 0x02
	b[24], b[27] = 9, 4 // 10 x 5
	d := t.TempDir()
	l, err := load(write(t, d, "a.webp", b))
	if err != nil || !l.Animated || l.Width != 10 || l.Height != 5 {
		t.Fatalf("%v %+v", err, l)
	}
}

func TestCheckEachProfile(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	okJPG := write(t, d, "ok.jpg", jpegBytes(t, solid(1000, 1000, color.NRGBA{10, 20, 30, 255}), 80))
	pngf := write(t, d, "a.png", pngBytes(t, solid(1000, 1000, color.NRGBA{10, 20, 30, 255})))
	wide := write(t, d, "wide.jpg", jpegBytes(t, solid(2000, 1000, color.NRGBA{10, 20, 30, 255}), 80))
	tall := write(t, d, "tall.jpg", jpegBytes(t, solid(400, 1000, color.NRGBA{10, 20, 30, 255}), 80))
	narrow := write(t, d, "narrow.jpg", jpegBytes(t, solid(200, 200, color.NRGBA{10, 20, 30, 255}), 80))
	wb, _ := base64.StdEncoding.DecodeString(tinyWebP)
	wp := write(t, d, "a.webp", wb)

	cases := []struct {
		profile, file string
		ok            bool
		want          string
	}{
		{"instagram", okJPG, true, ""},
		{"instagram", pngf, false, "format png"},
		{"instagram", wide, false, "above the maximum"},
		{"instagram", tall, false, "below the minimum"},
		{"instagram", narrow, false, "width 200 is below"},
		{"x", pngf, true, ""},
		{"x", okJPG, true, ""},
		{"x", wp, true, ""},
		{"linkedin", okJPG, true, ""},
		{"linkedin", pngf, true, ""},
		{"linkedin", wp, false, "format webp"},
	}
	for _, c := range cases {
		l, err := load(c.file)
		if err != nil {
			t.Fatal(err)
		}
		pr := checkInfo(mustProfile(t, c.profile), l.Info)
		if (len(pr) == 0) != c.ok {
			t.Errorf("%s %s: problems %v", c.profile, filepath.Base(c.file), pr)
		}
		if c.want != "" && !strings.Contains(strings.Join(pr, ";"), c.want) {
			t.Errorf("%s %s: want %q in %v", c.profile, filepath.Base(c.file), c.want, pr)
		}
	}
	// byte limits on the real profiles, with a file just over and just under.
	l, _ := load(okJPG)
	x := *mustProfile(t, "x")
	x.MaxBytes = l.Bytes
	if len(checkInfo(&x, l.Info)) != 0 {
		t.Error("exactly at the limit should pass")
	}
	x.MaxBytes = l.Bytes - 1
	if len(checkInfo(&x, l.Info)) == 0 {
		t.Error("one byte over should fail")
	}
	// aspect boundary values pass
	for _, dim := range [][2]int{{800, 1000}, {1910, 1000}} {
		pi := Info{Format: "jpeg", Width: dim[0], Height: dim[1], Bytes: 1}
		if pr := checkInfo(mustProfile(t, "instagram"), pi); len(pr) != 0 && dim[0] <= 1440 {
			t.Errorf("%v: %v", dim, pr)
		}
	}
}

func TestCheckFlagsEXIFOrientation(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	f := write(t, d, "o.jpg", withEXIF(jpegBytes(t, solid(1000, 600, color.NRGBA{1, 1, 1, 255}), 80), 6, false))
	l, _ := load(f)
	pr := checkInfo(mustProfile(t, "x"), l.Info)
	if len(pr) != 1 || !strings.Contains(pr[0], "EXIF orientation 6") {
		t.Errorf("%v", pr)
	}
}

// ---- EXIF parser ----

func TestEXIFParser(t *testing.T) {
	base := jpegBytes(t, solid(8, 8, color.NRGBA{1, 2, 3, 255}), 80)
	for o := 1; o <= 8; o++ {
		for _, be := range []bool{false, true} {
			if got := jpegOrientation(withEXIF(base, o, be)); got != o {
				t.Errorf("orientation %d bigEndian=%v: got %d", o, be, got)
			}
		}
	}
	if jpegOrientation(base) != 0 {
		t.Error("no exif should be 0")
	}
	if jpegOrientation(withEXIF(base, 9, false)) != 0 || jpegOrientation(withEXIF(base, 0, true)) != 0 {
		t.Error("out-of-range values must be ignored")
	}
	// every truncation of the segment, and garbage, must not panic or misreport
	full := withEXIF(base, 6, false)
	for n := 0; n < 40 && n < len(full); n++ {
		if got := jpegOrientation(full[:n]); got != 0 && n < 36 {
			t.Errorf("truncated at %d reported %d", n, got)
		}
	}
	bad := append([]byte{}, full...)
	for i := 10; i < 30; i++ { // corrupt the TIFF header and IFD
		c := append([]byte{}, bad...)
		c[i] ^= 0xFF
		_ = jpegOrientation(c)
	}
	if jpegOrientation([]byte("not a jpeg")) != 0 {
		t.Error("non-jpeg")
	}
}

// ---- fit ----

func TestFitConvertsPNGToJPEGAndFlattens(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "logo.png", pngBytes(t, solid(1000, 1000, color.NRGBA{255, 0, 0, 0}))) // fully transparent
	r := fitOK(t, mustProfile(t, "instagram"), in, opts())
	if r.Out != filepath.Join(d, "logo.instagram.jpg") || r.Format != "jpeg" {
		t.Fatalf("%+v", r)
	}
	txt := changesText(r)
	if !strings.Contains(txt, "converted PNG to JPEG") || !strings.Contains(txt, "flattened transparency onto #FFFFFF") {
		t.Errorf("changes: %s", txt)
	}
	if c := decodeFile(t, r.Out).At(500, 500); !near(c, white(), 3) {
		t.Errorf("transparent pixel became %v, want white", c)
	}
	// custom background
	o := opts()
	o.Background, o.BGHex = color.NRGBA{0, 0, 255, 255}, "#0000FF"
	r = fitOK(t, mustProfile(t, "instagram"), in, fitOptions{Out: filepath.Join(d, "blue.jpg"), Mode: "pad", Background: o.Background, BGHex: o.BGHex})
	if c := decodeFile(t, r.Out).At(500, 500); !near(c, color.NRGBA{0, 0, 255, 255}, 6) {
		t.Errorf("got %v want blue", c)
	}
}

func TestPadVersusCropToNearestRatio(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	red := color.NRGBA{200, 0, 0, 255}
	ig := mustProfile(t, "instagram")

	// Too tall: 400x1000 (0.4). Allowed minimum is 0.8.
	tall := write(t, d, "tall.png", pngBytes(t, solid(400, 1000, red)))
	r := fitOK(t, ig, tall, opts())
	if r.Width != 800 || r.Height != 1000 {
		t.Errorf("pad tall: %dx%d", r.Width, r.Height)
	}
	img := decodeFile(t, r.Out)
	if !near(img.At(20, 500), white(), 4) || !near(img.At(400, 500), red, 6) {
		t.Error("padding should be white at the sides and keep the content in the middle")
	}
	if !strings.Contains(changesText(r), "padded 400x1000 to 800x1000") {
		t.Errorf("%s", changesText(r))
	}
	o := opts()
	o.Mode, o.Out = "crop", filepath.Join(d, "tall.crop.jpg")
	r = fitOK(t, ig, tall, o)
	if r.Width != 400 || r.Height != 500 {
		t.Errorf("crop tall: %dx%d", r.Width, r.Height)
	}

	// Too wide: 1000x300 (3.33). Allowed maximum is 1.91.
	wide := write(t, d, "wide.png", pngBytes(t, solid(1000, 300, red)))
	r = fitOK(t, ig, wide, opts())
	if r.Width != 1000 || r.Height != 524 {
		t.Errorf("pad wide: %dx%d", r.Width, r.Height)
	}
	o = opts()
	o.Mode, o.Out = "crop", filepath.Join(d, "wide.crop.jpg")
	r = fitOK(t, ig, wide, o)
	if r.Width != 573 || r.Height != 300 {
		t.Errorf("crop wide: %dx%d", r.Width, r.Height)
	}
	if !strings.Contains(changesText(r), "cropped 1000x300 to 573x300") {
		t.Errorf("%s", changesText(r))
	}
}

func TestCropIsCentered(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	// 100x400 image: red top, green middle band, blue bottom. Crop to 0.8 keeps the middle 100x125.
	img := image.NewNRGBA(image.Rect(0, 0, 100, 400))
	for y := 0; y < 400; y++ {
		c := color.NRGBA{255, 0, 0, 255}
		if y >= 137 && y < 263 {
			c = color.NRGBA{0, 255, 0, 255}
		} else if y >= 263 {
			c = color.NRGBA{0, 0, 255, 255}
		}
		for x := 0; x < 100; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	p := &Profile{Name: "t", Formats: []string{"png"}, MinAspect: 0.8}
	in := write(t, d, "a.png", pngBytes(t, img))
	o := opts()
	o.Mode = "crop"
	r := fitOK(t, p, in, o)
	if r.Width != 100 || r.Height != 125 {
		t.Fatalf("%dx%d", r.Width, r.Height)
	}
	out := decodeFile(t, r.Out)
	if !near(out.At(50, 2), color.NRGBA{0, 255, 0, 255}, 4) || !near(out.At(50, 122), color.NRGBA{0, 255, 0, 255}, 4) {
		t.Error("crop kept the wrong part of the image")
	}
}

func TestScaleDownToMaxWidth(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "big.jpg", jpegBytes(t, solid(4000, 3000, color.NRGBA{30, 90, 150, 255}), 80))
	r := fitOK(t, mustProfile(t, "instagram"), in, opts())
	if r.Width != 1440 || r.Height != 1080 {
		t.Fatalf("%dx%d", r.Width, r.Height)
	}
	if !strings.Contains(changesText(r), "scaled 4000x3000 to 1440x1080") {
		t.Errorf("%s", changesText(r))
	}
}

func TestScaleUpOnlyToTheMinimumWidth(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "small.jpg", jpegBytes(t, solid(200, 250, color.NRGBA{30, 90, 150, 255}), 80))
	r := fitOK(t, mustProfile(t, "instagram"), in, opts())
	if r.Width != 320 || r.Height != 400 {
		t.Fatalf("%dx%d", r.Width, r.Height)
	}
	// A profile with no minimum never upscales, even when something else is wrong.
	p := &Profile{Name: "t", Formats: []string{"jpeg"}}
	png1 := write(t, d, "p.png", pngBytes(t, solid(50, 40, color.NRGBA{1, 2, 3, 255})))
	r = fitOK(t, p, png1, opts())
	if r.Width != 50 || r.Height != 40 {
		t.Errorf("upscaled to %dx%d", r.Width, r.Height)
	}
	// An image that is in range is not rescaled to 1440.
	mid := write(t, d, "mid.png", pngBytes(t, solid(700, 700, color.NRGBA{1, 2, 3, 255})))
	r = fitOK(t, mustProfile(t, "instagram"), mid, opts())
	if r.Width != 700 || r.Height != 700 {
		t.Errorf("%dx%d", r.Width, r.Height)
	}
}

func TestScaleKeepsAspectInsideRange(t *testing.T) {
	// A ratio right on the edge must stay legal after rounding.
	isolate(t)
	d := t.TempDir()
	p := &Profile{Name: "t", Formats: []string{"jpeg"}, MinWidth: 100, MaxWidth: 300, MinAspect: 0.8, MaxAspect: 1.91}
	for _, dim := range [][2]int{{601, 751}, {1000, 523}, {777, 971}, {999, 523}, {399, 499}} {
		in := write(t, d, "e.png", pngBytes(t, solid(dim[0], dim[1], color.NRGBA{9, 9, 9, 255})))
		o := opts()
		o.Out = filepath.Join(d, "e.out.jpg")
		o.Force = true
		r := fitOK(t, p, in, o)
		if pr := dimProblems(p, r.Width, r.Height); len(pr) != 0 {
			t.Errorf("%v -> %dx%d: %v", dim, r.Width, r.Height, pr)
		}
	}
}

func TestQualityLadderUnderTightByteLimit(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "n.png", pngBytes(t, noise(600, 600)))
	full := jpegBytes(t, noise(600, 600), 92)
	p := &Profile{Name: "t", Formats: []string{"jpeg"}, MaxBytes: int64(len(full)) * 80 / 100}
	r := fitOK(t, p, in, opts())
	if r.Bytes > p.MaxBytes {
		t.Fatalf("%d bytes over limit %d", r.Bytes, p.MaxBytes)
	}
	if r.Width != 600 || r.Height != 600 {
		t.Errorf("should not have shrunk: %dx%d (%s)", r.Width, r.Height, changesText(r))
	}
	if !strings.Contains(changesText(r), "recompressed at quality") || strings.Contains(changesText(r), "quality 92") {
		t.Errorf("%s", changesText(r))
	}
	// Much tighter: quality alone cannot do it, so it must shrink.
	p.MaxBytes = int64(len(full)) / 6
	o := opts()
	o.Out = filepath.Join(d, "tight.jpg")
	r = fitOK(t, p, in, o)
	if r.Bytes > p.MaxBytes || r.Width >= 600 {
		t.Errorf("bytes %d (limit %d), %dx%d", r.Bytes, p.MaxBytes, r.Width, r.Height)
	}
	if !strings.Contains(changesText(r), "to get under") {
		t.Errorf("%s", changesText(r))
	}
}

func TestByteLimitAndMinimumSizeConflictFails(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "n.png", pngBytes(t, noise(400, 400)))
	p := &Profile{Name: "t", Formats: []string{"jpeg"}, MaxBytes: 3000, MinWidth: 400}
	_, err := fitFile(p, in, opts())
	if err == nil || !strings.Contains(err.Error(), "minimum size") {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(d); len(entries) != 1 {
		t.Errorf("a failed fit left files behind: %v", entries)
	}
}

func TestLargePNGFallsBackToJPEG(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "n.png", pngBytes(t, noise(500, 500)))
	p := &Profile{Name: "t", Formats: []string{"png", "jpeg"}, MaxBytes: 200000}
	r := fitOK(t, p, in, opts())
	if r.Format != "jpeg" || !strings.Contains(changesText(r), "converted PNG to JPEG") {
		t.Errorf("%+v %s", r, changesText(r))
	}
}

func TestKeepsInputFormatWhenAllowed(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	// PNG too big for the limit but PNG-only profile: shrinks, stays PNG.
	in := write(t, d, "n.png", pngBytes(t, noise(300, 300)))
	p := &Profile{Name: "t", Formats: []string{"png"}, MaxBytes: 150000}
	r := fitOK(t, p, in, opts())
	if r.Format != "png" || r.Bytes > 150000 {
		t.Errorf("%+v", r)
	}
	// static GIF is left as is when it already fits, converted only when it must be.
	g := write(t, d, "s.gif", gifBytes(t, 1))
	x := mustProfile(t, "x")
	if r := fitOK(t, x, g, opts()); r.Out != g || len(r.Changes) != 0 {
		t.Errorf("%+v", r)
	}
	r = fitOK(t, mustProfile(t, "instagram"), g, fitOptions{Mode: "pad", Background: white(), BGHex: "#FFFFFF"})
	if r.Format != "jpeg" || !strings.Contains(changesText(r), "converted GIF to JPEG") {
		t.Errorf("%+v", r)
	}
}

func TestEXIFOrientationsProducePixels(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	cols := map[string]color.NRGBA{
		"A": {230, 30, 30, 255}, "B": {30, 200, 30, 255}, "C": {30, 30, 230, 255},
		"D": {230, 230, 30, 255}, "E": {30, 230, 230, 255}, "F": {230, 30, 230, 255},
	}
	const cell = 24
	src := image.NewNRGBA(image.Rect(0, 0, 3*cell, 2*cell))
	for ci, k := range []string{"A", "B", "C", "D", "E", "F"} {
		cx, cy := (ci%3)*cell, (ci/3)*cell
		for y := 0; y < cell; y++ {
			for x := 0; x < cell; x++ {
				src.SetNRGBA(cx+x, cy+y, cols[k])
			}
		}
	}
	// Stored layout ABC/DEF; the expected displayed layout, rows separated by "/".
	want := map[int]string{
		2: "CBA/FED", 3: "FED/CBA", 4: "DEF/ABC",
		5: "AD/BE/CF", 6: "DA/EB/FC", 7: "FC/EB/DA", 8: "CF/BE/AD",
	}
	p := &Profile{Name: "any", Formats: []string{"jpeg"}}
	jb := jpegBytes(t, src, 95)
	for o := 2; o <= 8; o++ {
		for _, be := range []bool{false, true} {
			in := write(t, d, "o.jpg", withEXIF(jb, o, be))
			opt := opts()
			opt.Out = filepath.Join(d, "o.out.jpg")
			opt.Force = true
			r := fitOK(t, p, in, opt)
			rows := strings.Split(want[o], "/")
			if r.Width != len(rows[0])*cell || r.Height != len(rows)*cell {
				t.Fatalf("orientation %d: %dx%d", o, r.Width, r.Height)
			}
			img := decodeFile(t, r.Out)
			for ry, row := range rows {
				for rx, ch := range row {
					got := img.At(rx*cell+cell/2, ry*cell+cell/2)
					if !near(got, cols[string(ch)], 25) {
						t.Errorf("orientation %d be=%v cell (%d,%d): want %c (%v) got %v", o, be, rx, ry, ch, cols[string(ch)], got)
					}
				}
			}
			if !strings.Contains(changesText(r), "applied EXIF orientation") {
				t.Errorf("%s", changesText(r))
			}
			if loaded, _ := load(r.Out); loaded.Orientation != 0 {
				t.Error("output should carry no orientation")
			}
		}
	}
	// orientation 1 is already upright: nothing to do.
	in := write(t, d, "one.jpg", withEXIF(jb, 1, false))
	if r := fitOK(t, p, in, opts()); r.Out != in || len(r.Changes) != 0 {
		t.Errorf("%+v", r)
	}
}

func TestOrientationAppliedBeforeAspectAndScale(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	// Stored 1000x400 (wide) with orientation 6 is displayed 400x1000 (too tall), so it is padded, not cropped sideways.
	in := write(t, d, "p.jpg", withEXIF(jpegBytes(t, solid(1000, 400, color.NRGBA{200, 10, 10, 255}), 85), 6, false))
	r := fitOK(t, mustProfile(t, "instagram"), in, opts())
	if r.Width != 800 || r.Height != 1000 {
		t.Errorf("%dx%d (%s)", r.Width, r.Height, changesText(r))
	}
}

func TestAnimatedGIF(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	g := write(t, d, "anim.gif", gifBytes(t, 3))
	// satisfies x: passes through untouched
	r := fitOK(t, mustProfile(t, "x"), g, opts())
	if r.Out != g || len(r.Changes) != 0 {
		t.Errorf("%+v", r)
	}
	// with --out it is copied byte for byte
	o := opts()
	o.Out = filepath.Join(d, "copy.gif")
	r = fitOK(t, mustProfile(t, "x"), g, o)
	a, _ := os.ReadFile(g)
	b, _ := os.ReadFile(o.Out)
	if !bytes.Equal(a, b) || r.Out != o.Out || len(r.Changes) != 0 {
		t.Errorf("copy differs or wrong result: %+v", r)
	}
	// does not satisfy instagram: clear failure, nothing written
	_, err := fitFile(mustProfile(t, "instagram"), g, opts())
	if err == nil || !strings.Contains(err.Error(), "animated") || !strings.Contains(err.Error(), "cannot resize or convert") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(d, "anim.instagram.jpg")); err == nil {
		t.Error("wrote output for an animated GIF")
	}
	// too big for a custom limit: also fails
	p := &Profile{Name: "t", Formats: []string{"gif"}, MaxBytes: 10}
	if _, err := fitFile(p, g, opts()); err == nil {
		t.Error("expected failure")
	}
}

func TestNeverOverwritesAndForce(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	orig := jpegBytes(t, solid(1500, 1000, color.NRGBA{5, 6, 7, 255}), 80)
	in := write(t, d, "a.jpg", orig)
	ig := mustProfile(t, "instagram")
	existing := write(t, d, "a.instagram.jpg", []byte("precious"))
	_, err := fitFile(ig, in, opts())
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(existing); string(b) != "precious" {
		t.Error("existing output was overwritten")
	}
	o := opts()
	o.Force = true
	r := fitOK(t, ig, in, o)
	if b, _ := os.ReadFile(existing); string(b) == "precious" || r.Width != 1440 {
		t.Error("--force should replace the file")
	}
	if b, _ := os.ReadFile(in); !bytes.Equal(b, orig) {
		t.Error("the input was modified")
	}
	// the input itself is never a valid output, even with --force
	o.Out = in
	if _, err := fitFile(ig, in, o); err == nil || !strings.Contains(err.Error(), "over the input") {
		t.Errorf("got %v", err)
	}
	if b, _ := os.ReadFile(in); !bytes.Equal(b, orig) {
		t.Error("the input was modified")
	}
	// the same holds for the pass-through copy
	ok := write(t, d, "ok.jpg", jpegBytes(t, solid(800, 800, color.NRGBA{1, 1, 1, 255}), 80))
	exist2 := write(t, d, "dest.jpg", []byte("keep"))
	o = opts()
	o.Out = exist2
	if _, err := fitFile(ig, ok, o); err == nil {
		t.Error("copy overwrote without --force")
	}
	if b, _ := os.ReadFile(exist2); string(b) != "keep" {
		t.Error("copy target changed")
	}
}

func TestSatisfiedInputWritesNothing(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "ok.jpg", jpegBytes(t, solid(800, 800, color.NRGBA{1, 1, 1, 255}), 80))
	r := fitOK(t, mustProfile(t, "instagram"), in, opts())
	if r.Out != in || r.Changes == nil || len(r.Changes) != 0 {
		t.Errorf("%+v", r)
	}
	if entries, _ := os.ReadDir(d); len(entries) != 1 {
		t.Errorf("files written: %v", entries)
	}
	// JSON must say changes: [] not null
	var b bytes.Buffer
	printJSON(&b, r)
	if !strings.Contains(b.String(), `"changes": []`) {
		t.Errorf("%s", b.String())
	}
}

func TestOutputParentMustExist(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "a.png", pngBytes(t, solid(500, 500, color.NRGBA{1, 1, 1, 255})))
	o := opts()
	o.Out = filepath.Join(d, "nope", "out.jpg")
	_, err := fitFile(mustProfile(t, "instagram"), in, o)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(d, "nope")); err == nil {
		t.Error("created the directory")
	}
}

func TestRejectsNonImagesAndHugeDimensions(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	txt := write(t, d, "a.png", []byte("hello, I am not an image"))
	if _, err := fitFile(mustProfile(t, "x"), txt, opts()); err == nil || !strings.Contains(err.Error(), "not a decodable image") {
		t.Errorf("got %v", err)
	}
	if _, err := load(filepath.Join(d, "missing.png")); err == nil {
		t.Error("missing file")
	}
	if _, err := load(d); err == nil {
		t.Error("directory")
	}
	// A PNG header claiming 20000x20000 with no pixel data: refused from the header alone.
	bomb := pngHeader(20000, 20000)
	if _, err := load(write(t, d, "bomb.png", bomb)); err == nil || !strings.Contains(err.Error(), "pixel limit") {
		t.Errorf("got %v", err)
	}
	// Just under the pixel cap is accepted by the dimension check (header only).
	if err := checkPixels("f", 10000, 10000); err != nil {
		t.Errorf("100 megapixels exactly should pass: %v", err)
	}
	if err := checkPixels("f", 10001, 10000); err == nil {
		t.Error("over 100 megapixels should fail")
	}
}

func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestFinalCheckDeletesBadOutput(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	in := write(t, d, "a.png", pngBytes(t, solid(500, 500, color.NRGBA{1, 1, 1, 255})))
	encodeHook = func([]byte) []byte { return pngBytes(t, solid(500, 500, color.NRGBA{1, 1, 1, 255})) } // wrong format
	defer func() { encodeHook = nil }()
	_, err := fitFile(mustProfile(t, "instagram"), in, opts())
	if err == nil || !strings.Contains(err.Error(), "final check") {
		t.Fatalf("got %v", err)
	}
	entries, _ := os.ReadDir(d)
	if len(entries) != 1 {
		t.Errorf("bad output left behind: %v", entries)
	}
}

// ---- CLI ----

func runCLI(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestCLIProfilesInfoCheckFit(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	code, out, _ := runCLI("profiles")
	if code != 0 {
		t.Fatal(code)
	}
	var pr struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &pr); err != nil || len(pr.Profiles) != 3 {
		t.Fatalf("%v %s", err, out)
	}
	png1 := write(t, d, "a.png", pngBytes(t, solid(1000, 500, color.NRGBA{1, 1, 1, 255})))
	code, out, _ = runCLI("info", png1)
	var in Info
	if code != 0 || json.Unmarshal([]byte(out), &in) != nil || in.Format != "png" || in.Width != 1000 {
		t.Fatalf("%d %s", code, out)
	}
	code, out, _ = runCLI("check", "--for", "x", png1)
	if code != 0 || !strings.Contains(out, `"ok": true`) {
		t.Errorf("%d %s", code, out)
	}
	code, out, _ = runCLI("check", "--for", "instagram", png1)
	if code != 1 || !strings.Contains(out, `"ok": false`) || !strings.Contains(out, "problems") {
		t.Errorf("%d %s", code, out)
	}
	// flags after the file, and the MCP style: --flag=value then -- FILE
	code, out, errs := runCLI("fit", png1, "--for", "instagram", "--mode", "crop")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	code, _, _ = runCLI("fit", "--for=instagram", "--force", "--mode=pad", "--", png1)
	if code != 0 {
		t.Fatalf("mcp-style call failed: %d", code)
	}
	code, out, _ = runCLI("check", "--for", "instagram", filepath.Join(d, "a.instagram.jpg"))
	if code != 0 {
		t.Errorf("the fitted file fails check: %s", out)
	}
	code, _, errs = runCLI("info", write(t, d, "junk.png", []byte("junk")))
	if code != 1 || errs == "" {
		t.Errorf("%d %q", code, errs)
	}
}

func TestCLIUsageErrorsExit2(t *testing.T) {
	isolate(t)
	d := t.TempDir()
	f := write(t, d, "a.png", pngBytes(t, solid(10, 10, color.NRGBA{1, 1, 1, 255})))
	cases := [][]string{
		{},
		{"frobnicate"},
		{"info"},
		{"info", f, f},
		{"info", "--bogus", f},
		{"check", f},
		{"check", "--for", "x"},
		{"check", "--for", "nosuch", f},
		{"fit", "--for", "x"},
		{"fit", "--for", "x", "--mode", "stretch", f},
		{"fit", "--for", "x", "--background", "red", f},
		{"fit", "--for", "x", "--background", "#12345", f},
		{"fit", "--for", "nosuch", f},
		{"profiles", "extra"},
	}
	for _, c := range cases {
		if code, _, errs := runCLI(c...); code != 2 || errs == "" {
			t.Errorf("%v: exit %d stderr %q", c, code, errs)
		}
	}
	if code, _, _ := runCLI("help"); code != 0 {
		t.Error("help should exit 0")
	}
}

func TestCLIBrokenProfileFileIsAnErrorNotUsage(t *testing.T) {
	isolate(t)
	profileDir(t, "garbage [[")
	code, _, errs := runCLI("profiles")
	if code != 1 || !strings.Contains(errs, "p.toml") {
		t.Errorf("%d %q", code, errs)
	}
}

func TestCustomProfileEndToEnd(t *testing.T) {
	isolate(t)
	profileDir(t, "[[profile]]\nname='square'\nformats=['png']\nmin_aspect=1\nmax_aspect=1\nmax_width=200\nsource='test'\n")
	d := t.TempDir()
	in := write(t, d, "a.jpg", jpegBytes(t, solid(600, 300, color.NRGBA{40, 40, 40, 255}), 85))
	code, out, errs := runCLI("fit", "--for", "square", in)
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	var r FitResult
	json.Unmarshal([]byte(out), &r)
	if r.Width != 200 || r.Height != 200 || r.Format != "png" || !strings.HasSuffix(r.Out, "a.square.png") {
		t.Errorf("%+v", r)
	}
}
