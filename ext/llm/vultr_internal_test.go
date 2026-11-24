package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	llmc "pocketone.dev/ext/contracts/llm"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
)

// Pictures in make a Vultr image call an edit: /images/edits with each picture as a data: URL (or its own URL), the
// aspect ratio and options alongside; with none it's a generation.
func TestVultrImageEdit(t *testing.T) {
	got := map[string]map[string]any{}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nxxxx"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got[r.URL.Path] = body
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"`+png+`"}],"usage":{"megapixels":1,"cost":0.02}}`)
	}))
	defer srv.Close()
	d := newVultr(Provider{Name: "vultr", Type: TypeVultr, Endpoint: srv.URL + "/v1", Key: "k"})
	ctx := context.Background()

	in := imagev1.GenerateInput{Prompt: "same style; the fox asleep", AspectRatio: "4:3", Options: llmc.Options{"seed": 7}}
	r, err := d.image(ctx, "qwen-image-2.1", in, []llmc.File{
		{Type: "image/png", Data: []byte("anchor")},
		{URL: "https://example.com/b.png"},
	})
	if err != nil || len(r.Files) != 1 || r.Files[0].Type != "image/png" || r.Usage.CostMicroUSD != 20000 {
		t.Fatalf("%+v %v", r, err)
	}
	b := got["/v1/images/edits"]
	want := `[{"image_url":"data:image/png;base64,YW5jaG9y"},{"image_url":"https://example.com/b.png"}]`
	if j, _ := json.Marshal(b["images"]); string(j) != want {
		t.Fatalf("images: %s", j)
	}
	if b["model"] != "qwen-image-2.1" || b["aspect_ratio"] != "4:3" || b["seed"] != float64(7) || b["response_format"] != "b64_json" {
		t.Fatalf("body: %v", b)
	}

	if _, err := d.image(ctx, "z-image-turbo", imagev1.GenerateInput{Prompt: "a fox"}, nil); err != nil {
		t.Fatal(err)
	}
	if b := got["/v1/images/generations"]; b == nil || b["images"] != nil || b["aspect_ratio"] != nil {
		t.Fatalf("generation: %v", b)
	}
}
