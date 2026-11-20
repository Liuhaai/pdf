package pdf

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestReadsAES128WithUnencryptedMetadata(t *testing.T) {
	// Synthetic Standard security dictionary with an empty user password.
	// O and U were generated from PDF 32000-1 algorithms 2 through 5 and
	// verified by opening the document with Poppler.
	const id = "2a9f8f680e1f40e3a3b9f509a2e7523f"
	const owner = "0f98dfb52e1947863c72a02353b9fc887d2189baff2badc77406a66e562e027e"
	const user = "87cc8822b18eabad485b483751170b2000000000000000000000000000000000"
	var data bytes.Buffer
	data.WriteString("%PDF-1.6\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources <<>> >>",
		fmt.Sprintf(`<< /Filter /Standard /V 4 /R 4 /Length 128 /EncryptMetadata false
/CF << /StdCF << /CFM /AESV2 /Length 16 /AuthEvent /DocOpen >> >>
/StmF /StdCF /StrF /StdCF /O <%s> /U <%s> /P -4 >>`, owner, user),
	}
	var offsets []int
	for i, object := range objects {
		offsets = append(offsets, data.Len())
		fmt.Fprintf(&data, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := data.Len()
	data.WriteString("xref\n0 5\n0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&data, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&data, "trailer\n<< /Size 5 /Root 1 0 R /Encrypt 4 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n", id, id, xref)

	r, err := NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.NumPage(); got != 1 {
		t.Fatalf("NumPage() = %d, want 1", got)
	}
}

// A one-line page encrypted by pdfcpu with 40-bit RC4 (/V 1 /R 2) and an empty
// user password. PDF 32000-1 Algorithm 1 truncates the per-object key to
// min(n/8+5, 16) bytes: 10 here, where the whole digest is only right for a
// 128-bit key.
func TestReads40BitRC4WithEmptyUserPassword(t *testing.T) {
	for _, bits := range []string{"40"} {
		t.Run(bits+"-bit", func(t *testing.T) {
			f, err := os.Open("testdata/rc4-" + bits + ".pdf")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			st, _ := f.Stat()

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("reader panicked: %v", r)
				}
			}()
			r, err := NewReaderEncrypted(f, st.Size(), func() string { return "" })
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			text, err := r.Page(1).GetPlainText(nil)
			if err != nil {
				t.Fatalf("page text: %v", err)
			}
			if !strings.Contains(text, "forty bit rc4 reads this line") {
				t.Fatalf("page text %q does not carry the line", text)
			}
		})
	}
}
