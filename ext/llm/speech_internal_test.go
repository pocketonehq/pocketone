package llm

import (
	"encoding/json"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/filesystem"

	llmc "pocketone.dev/ext/contracts/llm"
	speechv1 "pocketone.dev/ext/contracts/llm/speech/v1"
)

// A voice-cloning clip goes to Vultr and OpenRouter as input_references: the audio part (format from the name, else
// the media type) and the text part only when there is text.
func TestSpeechReferences(t *testing.T) {
	if speechReferences(nil) != nil {
		t.Fatal("no reference should send nothing")
	}
	got, _ := json.Marshal(speechReferences(&speechv1.VoiceReference{Audio: llmc.File{Name: "Ben.WAV", Data: []byte("hi")}, Text: " Once upon a time "}))
	want := `[{"type":"input_audio","input_audio":{"data":"aGk=","format":"wav"}},{"type":"text","text":"Once upon a time"}]`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
	got, _ = json.Marshal(speechReferences(&speechv1.VoiceReference{Audio: llmc.File{Type: "audio/mpeg", Data: []byte("hi")}}))
	want = `[{"type":"input_audio","input_audio":{"data":"aGk=","format":"mp3"}}]`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}

func TestVoiceListClones(t *testing.T) {
	l := speechv1.VoiceList{Voices: []speechv1.Voice{{ID: "default", Mode: speechv1.ModeBuiltin}}}
	if l.Clones() {
		t.Fatal("no clone voice")
	}
	l.Voices = append(l.Voices, speechv1.Voice{ID: "clone", Mode: speechv1.ModeClone})
	if !l.Clones() {
		t.Fatal("has a clone voice")
	}
}

// fromRecord reads a stored clip back with its bytes, name and media type.
func TestFileFromRecord(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	col := core.NewBaseCollection("clips")
	col.Fields.Add(&core.FileField{Name: "audio", MaxSelect: 1, MaxSize: 1 << 20})
	if err := app.Save(col); err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	f, _ := filesystem.NewFileFromBytes([]byte("RIFFclip"), "ben.wav")
	rec.Set("audio", f)
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
	got, err := fileFromRecord(app, rec, "audio")
	if err != nil || string(got.Data) != "RIFFclip" || got.Type != "audio/wav" && got.Type != "audio/x-wav" || got.Name == "" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := fileFromRecord(app, core.NewRecord(col), "audio"); err == nil {
		t.Fatal("a record with no file should fail")
	}
}
