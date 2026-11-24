package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	llmc "pocketone.dev/ext/contracts/llm"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
)

// References make a Vultr image call an edit: /images/edits with each picture as a data: URL (or its own URL), the
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

	in := imagev1.GenerateInput{Prompt: "same style; the fox asleep", AspectRatio: "4:3", Options: llmc.Options{"seed": 7},
		References: []llmc.File{{Type: "image/png", Data: []byte("anchor")}, {URL: "https://example.com/b.png"}}}
	r, err := d.image(ctx, "qwen-image-2.1", in)
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

	if _, err := d.image(ctx, "z-image-turbo", imagev1.GenerateInput{Prompt: "a fox"}); err != nil {
		t.Fatal(err)
	}
	if b := got["/v1/images/generations"]; b == nil || b["images"] != nil || b["aspect_ratio"] != nil {
		t.Fatalf("generation: %v", b)
	}
}

// An image model whose image input lists references has the feature "reference images"; a video model that draws
// stills from references (minimax-h3) is an image model too; a text-only image model has neither.
func TestVultrReferenceImagesFeature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[
			{"id":"qwen","input_modalities":[{"type":"text"},{"type":"image","supported_inputs":{"references":{"type":"integer","min":1,"max":10}}}],
			 "output_modalities":[{"type":"image"}]},
			{"id":"mm","input_modalities":[{"type":"text"},{"type":"image","supported_inputs":{"references":{"type":"integer","min":1,"max":9}}}],
			 "output_modalities":[{"type":"video"},{"type":"image"}]},
			{"id":"vid","input_modalities":[{"type":"text"},{"type":"image","supported_inputs":{}}],
			 "output_modalities":[{"type":"video"},{"type":"image"}]},
			{"id":"z","input_modalities":[{"type":"text"}],"output_modalities":[{"type":"image"}]}]}`)
	}))
	defer srv.Close()
	ms, err := newVultr(Provider{Name: "vultr", Type: TypeVultr, Endpoint: srv.URL + "/v1"}).models(context.Background())
	if err != nil || len(ms) != 4 {
		t.Fatalf("%+v %v", ms, err)
	}
	refs := func(m llmc.ModelInfo) bool { return slices.Contains(m.Features, llmc.FeatureReferenceImages) }
	image := func(m llmc.ModelInfo) bool { return slices.Contains(m.Kinds, llmc.KindImage) }
	if !refs(ms[0]) || !image(ms[0]) {
		t.Fatalf("qwen: %+v", ms[0])
	}
	if !refs(ms[1]) || !image(ms[1]) || !slices.Contains(ms[1].Kinds, llmc.KindVideo) {
		t.Fatalf("minimax: %+v", ms[1])
	}
	if refs(ms[2]) || image(ms[2]) {
		t.Fatalf("a video model without references: %+v", ms[2])
	}
	if refs(ms[3]) || !image(ms[3]) {
		t.Fatalf("z: %+v", ms[3])
	}
}
