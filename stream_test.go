package pdf

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand"
	"strings"
	"testing"
)

func TestValueReaderOnNonStream(t *testing.T) {
	v := testValue(int64(1))
	rd := v.Reader()
	data, err := io.ReadAll(rd)
	if err == nil {
		t.Fatal("expected error reading non-stream value")
	}
	if !strings.Contains(err.Error(), "stream not present") {
		t.Fatalf("error = %q, want it to mention 'stream not present'", err)
	}
	if len(data) != 0 {
		t.Fatalf("read %q, want empty", data)
	}
}

func TestPNGPredictorUp(t *testing.T) {
	// Two rows of width 2 encoded with the PNG "Up" predictor (filter byte 2).
	// Row 1: raw [5, 10]; Row 2: raw [1, 1] (differences from the previous row).
	input := []byte{2, 5, 10, 2, 1, 1}
	r := &pngPredictorReader{
		r: bytes.NewReader(input), bytesPerPixel: 1, hist: make([]byte, 2), tmp: make([]byte, 3),
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// hist accumulates: after row1 [5,10]; after row2 [5+1, 10+1] = [6,11].
	want := []byte{5, 10, 6, 11}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded = %v, want %v", got, want)
	}
}

func TestPNGPredictorMixedRGBRows(t *testing.T) {
	// Five rows with two RGB pixels each.
	input := []byte{
		0, 5, 10, 15, 20, 25, 30,
		1, 6, 12, 18, 19, 23, 27,
		2, 2, 2, 0, 5, 5, 10,
		3, 5, 10, 11, 12, 16, 23,
		4, 2, 2, 2, 2, 6, 5,
	}
	want := []byte{
		5, 10, 15, 20, 25, 30,
		6, 12, 18, 25, 35, 45,
		8, 14, 18, 30, 40, 55,
		9, 17, 20, 31, 44, 60,
		11, 19, 22, 33, 50, 65,
	}
	var encoded bytes.Buffer
	zw := zlib.NewWriter(&encoded)
	if _, err := zw.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	params := testValue(dict{"Predictor": int64(12), "Columns": int64(2), "Colors": int64(3)})
	got, err := io.ReadAll(applyFilter(&encoded, "FlateDecode", params))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("decoded = %v, %v, want %v", got, err, want)
	}
}

func TestPNGPredictorRejectsBadFilter(t *testing.T) {
	input := []byte{5, 5, 10} // PNG has no filter type 5.
	r := &pngPredictorReader{
		r: bytes.NewReader(input), bytesPerPixel: 1, hist: make([]byte, 2), tmp: make([]byte, 3),
	}
	_, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "malformed PNG predictor") {
		t.Fatalf("err = %v, want malformed PNG predictor error", err)
	}
}

func zlibCompress(t *testing.T, b []byte) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return &out
}

// pngIDAT returns the IDAT data of a PNG file, joined: one zlib stream.
func pngIDAT(b []byte) []byte {
	var idat []byte
	for b = b[8:]; len(b) >= 12; {
		n := binary.BigEndian.Uint32(b)
		if string(b[4:8]) == "IDAT" {
			idat = append(idat, b[8:8+n]...)
		}
		b = b[12+n:]
	}
	return idat
}

// TestPNGPredictorMatchesImagePNG decodes the IDAT data that image/png writes. It has the
// layout PDF's PNG predictors use: each row is a filter type, then the filtered bytes. The
// encoder picks a filter for each row, so all five types get used.
func TestPNGPredictorMatchesImagePNG(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	used := map[byte]int{}
	for i := 0; i < 300; i++ {
		w, h := 1+rng.Intn(40), 1+rng.Intn(20)
		// Gradients with some noise, so that the encoder picks every filter type.
		sample := func(x, y, c int) uint8 {
			if rng.Intn(4) == 0 {
				return uint8(rng.Intn(256))
			}
			return uint8(x*7 + y*13 + c*50 + rng.Intn(3))
		}
		var img image.Image
		var pix []byte
		var colors, bits int64
		switch i % 3 {
		case 0:
			m := image.NewGray(image.Rect(0, 0, w, h))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					m.SetGray(x, y, color.Gray{sample(x, y, 0)})
				}
			}
			img, pix, colors, bits = m, m.Pix, 1, 8
		case 1:
			m := image.NewNRGBA(image.Rect(0, 0, w, h))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					// Alpha below 255 keeps the encoder from dropping the alpha channel.
					m.SetNRGBA(x, y, color.NRGBA{sample(x, y, 0), sample(x, y, 1), sample(x, y, 2), 200 + uint8(rng.Intn(55))})
				}
			}
			img, pix, colors, bits = m, m.Pix, 4, 8
		case 2:
			m := image.NewGray16(image.Rect(0, 0, w, h))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					m.SetGray16(x, y, color.Gray16{uint16(sample(x, y, 0))<<8 | uint16(sample(x, y, 1))})
				}
			}
			img, pix, colors, bits = m, m.Pix, 1, 16
		}
		var enc bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&enc, img); err != nil {
			t.Fatal(err)
		}
		idat := pngIDAT(enc.Bytes())
		zr, err := zlib.NewReader(bytes.NewReader(idat))
		if err != nil {
			t.Fatal(err)
		}
		filtered, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		rowBytes := w * int(colors*bits/8)
		for y := 0; y < h; y++ {
			used[filtered[y*(rowBytes+1)]]++
		}

		params := testValue(dict{"Predictor": int64(12), "Columns": int64(w), "Colors": colors, "BitsPerComponent": bits})
		got, err := io.ReadAll(applyFilter(bytes.NewReader(idat), "FlateDecode", params))
		if err != nil || !bytes.Equal(got, pix) {
			t.Fatalf("image %d (%dx%d, %d colors, %d bits): err %v, decoded bytes differ", i, w, h, colors, bits, err)
		}
	}
	for f := byte(0); f <= 4; f++ {
		if used[f] == 0 {
			t.Errorf("no row used filter type %d", f)
		}
	}
}

// Values 10 to 15 all mean PNG prediction with a filter type on each row.
func TestPNGPredictorValues(t *testing.T) {
	input := []byte{0, 7, 2, 1, 1, 3} // without /Columns a row is one byte: None 7, Up +1, Sub 3
	for pred := int64(10); pred <= 15; pred++ {
		got, err := io.ReadAll(applyFilter(zlibCompress(t, input), "FlateDecode", testValue(dict{"Predictor": pred})))
		if want := []byte{7, 8, 3}; err != nil || !bytes.Equal(got, want) {
			t.Errorf("Predictor %d: %v, %v; want %v", pred, got, err, want)
		}
	}
}

func TestPNGPredictorRejectsBadParameters(t *testing.T) {
	for _, p := range []dict{
		{"Predictor": int64(12), "Columns": int64(50_000_000_000)}, // would allocate 100 GB
		{"Predictor": int64(12), "Columns": int64(0)},
		{"Predictor": int64(12), "Colors": int64(0)},
		{"Predictor": int64(12), "Colors": int64(33)},
		{"Predictor": int64(12), "BitsPerComponent": int64(3)},
	} {
		func() {
			defer func() {
				if r := recover(); r != "invalid PNG predictor parameters" {
					t.Errorf("%v: recovered %v, want invalid PNG predictor parameters", p, r)
				}
			}()
			applyFilter(zlibCompress(t, []byte{0, 0}), "FlateDecode", testValue(p))
		}()
	}
}

func TestCBCReader(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{0x05}, 16)
	plaintext := []byte("abcdefghijklmnopabcdefghijklmnop") // 32 bytes (2 blocks)
	ct := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plaintext)

	r := &cbcReader{
		cbc: cipher.NewCBCDecrypter(block, iv),
		rd:  bytes.NewReader(ct),
		buf: make([]byte, 16),
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestCryptKeyDeterministicAndSaltAware(t *testing.T) {
	k1 := cryptKey([]byte("secret"), false, objptr{id: 1, gen: 0})
	k2 := cryptKey([]byte("secret"), false, objptr{id: 1, gen: 0})
	if !bytes.Equal(k1, k2) {
		t.Fatal("cryptKey is not deterministic")
	}
	if k3 := cryptKey([]byte("secret"), true, objptr{id: 1, gen: 0}); bytes.Equal(k1, k3) {
		t.Fatal("cryptKey should differ when useAES (salt) differs")
	}
	if k4 := cryptKey([]byte("secret"), false, objptr{id: 2, gen: 0}); bytes.Equal(k1, k4) {
		t.Fatal("cryptKey should differ when object id differs")
	}
	// PDF 32000-1:2008 Algorithm 1 step e: the object key is the first
	// min(n/8+5, 16) bytes of the digest, so 11 for this 6-byte key and 16
	// only once the document key is 11 bytes or longer.
	if len(k1) != 11 {
		t.Fatalf("object key length for a 6-byte key = %d, want 11", len(k1))
	}
	if k5 := cryptKey([]byte("0123456789abcdef"), false, objptr{id: 1, gen: 0}); len(k5) != 16 {
		t.Fatalf("object key length for a 16-byte key = %d, want 16", len(k5))
	}
}

func TestDecryptStringRC4RoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	ptr := objptr{id: 3, gen: 1}
	plaintext := "hello, encrypted world"

	// RC4 is a stream cipher; applying the same keystream twice recovers the
	// plaintext, so decryptString is its own inverse here.
	enc := decryptString(key, false, ptr, plaintext)
	if enc == plaintext {
		t.Fatal("RC4 pass did not transform the plaintext")
	}
	if got := decryptString(key, false, ptr, enc); got != plaintext {
		t.Fatalf("round trip = %q, want %q", got, plaintext)
	}
}

func TestDecryptStringAESRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 16)
	ptr := objptr{id: 7, gen: 2}

	derived := cryptKey(key, true, ptr)
	block, err := aes.NewCipher(derived)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{0x01}, 16)
	plaintext := []byte("0123456789abcdef") // exactly one block, no padding
	ct := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plaintext)

	sealed := string(append(append([]byte{}, iv...), ct...))
	if got := decryptString(key, true, ptr, sealed); got != string(plaintext) {
		t.Fatalf("round trip = %q, want %q", got, plaintext)
	}
}

func TestOkayV4(t *testing.T) {
	valid := dict{
		name("CF"): dict{
			name("StdCF"): dict{
				name("CFM"):       name("AESV2"),
				name("Length"):    int64(16),
				name("AuthEvent"): name("DocOpen"),
			},
		},
		name("StmF"): name("StdCF"),
		name("StrF"): name("StdCF"),
	}
	if !okayV4(valid) {
		t.Fatal("okayV4(valid) = false, want true")
	}

	if okayV4(dict{name("StmF"): name("A"), name("StrF"): name("B")}) {
		t.Fatal("okayV4 should reject StmF != StrF")
	}
	badCFM := dict{
		name("CF"):   dict{name("StdCF"): dict{name("CFM"): name("V2"), name("Length"): int64(16)}},
		name("StmF"): name("StdCF"),
		name("StrF"): name("StdCF"),
	}
	if okayV4(badCFM) {
		t.Fatal("okayV4 should reject CFM != AESV2")
	}
	badLength := dict{
		name("CF"):   dict{name("StdCF"): dict{name("CFM"): name("AESV2"), name("Length"): int64(8)}},
		name("StmF"): name("StdCF"),
		name("StrF"): name("StdCF"),
	}
	if okayV4(badLength) {
		t.Fatal("okayV4 should reject non-16-byte key length")
	}
	badCFType := dict{
		name("CF"):   dict{name("StdCF"): int64(1234)}, // CF value is not a dict
		name("StmF"): name("StdCF"),
		name("StrF"): name("StdCF"),
	}
	if okayV4(badCFType) {
		t.Fatal("okayV4 should reject a non-dict CF entry")
	}
}
