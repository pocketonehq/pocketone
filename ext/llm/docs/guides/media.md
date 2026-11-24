---
title: Pictures, speech and music
description: "Generate pictures, narration, music and video with $llm and save them on your records as files."
---

# Pictures, speech and music

Pictures, audio and video come back as files: `{ name, type, data }`, with the bytes in `data`. `$llm.file()` turns one
into a PocketBase file, so it goes straight into a record's file field.

## Pictures

```ts
const r = await $llm.image.generateAsync({ model: "vultr/z-image-turbo", prompt: "a small lighthouse at dusk, watercolor" })
const page = new Record($app.findCollectionByNameOrId("pages"))
page.set("picture", $llm.file(r.files[0]))
$app.save(page)
```

```go
r, err := imagev1.Contract.Must(app).Generate(ctx, extkit.Internal, imagev1.GenerateInput{
	Model: "vultr/z-image-turbo", Prompt: "a small lighthouse at dusk, watercolor"})
if err != nil {
	return err
}
f, _ := filesystem.NewFileFromBytes(r.Files[0].Data, r.Files[0].Name)
page.Set("picture", f)
```

`n` asks for several, `size` (`"1024x768"`) or `aspectRatio` (`"16:9"`) shapes them, as the model allows.
`$llm.image.editAsync({ model, prompt, images: [file] })` makes a picture from pictures you give it: changes one, or
draws a new scene in the same style and with the same characters as a reference ("same style and characters; show
them ..."). On OpenRouter, and on Vultr's `qwen-image-2.1` (up to 10 pictures, evaluation only under the Qwen Research
License); a Vultr model without reference editing answers its own error naming `images`. Cloudflare and other
OpenAI-compatible APIs answer `not_supported`.

## Speech

```ts
const r = await $llm.speech.speakAsync({ model: "vultr/qwen3-tts-12hz-1.7b", text: storyPage, voice: "vivian", instructions: "slow and calm" })
page.set("narration", $llm.file(r.file))
```

`$llm.speech.voicesAsync(model)` lists a model's voices, where the provider says. `format` picks `mp3` (the default), `wav`,
`opus` or what else the model makes.

### A voice of your own

Some models read in the voice of a clip you give them (voice cloning). Record 10 to 30 seconds read the way the story
should sound, store it in a file field, and send it as `reference` with what it says:

```ts
const clip = $app.findFirstRecordByData("voices", "name", "mine")
const r = await $llm.speech.speakAsync({
  model: "vultr/qwen3-tts-12hz-1.7b",
  text: storyPage,
  reference: { audio: $llm.fromRecord(clip, "audio"), text: clip.getString("passage") },
})
```

`$llm.fromRecord(record, field)` reads a stored file with its bytes, ready to send to a model. Leave out `voice` and
`instructions`: a cloned voice takes its delivery from the clip. A model clones when one of its voices from
`voicesAsync` has `mode: "clone"`. Nothing is saved at the provider, so the clip goes along on every call. Vultr and
OpenRouter clone; Cloudflare and other OpenAI-compatible APIs refuse a `reference` with `not_supported`. Clone only a
voice you have the right to use.

## Transcription

Send audio as a file with its bytes in `data` (base64) and a name that says its format:

```ts
const r = await $llm.transcribe.transcribeAsync({ model: "vultr/whisper-large-v3-turbo", audio: { name: "memo.mp3", data: base64Audio } })
// r.text: what was said
```

## Timing words to audio

When you already know the words (you wrote them, then had them spoken), alignment says when each line and word is
said. Give the audio and the text, one line per line; every word comes back with its start and end in seconds:

```ts
const text = "Once upon a time, a small fox lived at the edge of a quiet wood.\nEvery night, she counted the stars."
const speech = await $llm.speech.speakAsync({ model: "vultr/auk", text })
const r = await $llm.align.alignAsync({ model: "vultr/aligner-1", audio: speech.file, text, mode: "speech" })
// r.lines: [{ index: 0, text: "Once upon a time, ...", start: 0.3, end: 5.0, certain: true }, ...]
// r.words: [{ line: 0, text: "Once", start: 0.3, end: 0.44 }, ...]
```

Use `mode: "speech"` for a voice; the default, `"lyrics"`, first separates a sung vocal from its music. A line in square
brackets (`[Chorus]`) labels the lines under it and isn't timed. Vultr's `aligner-1` costs a fraction of a cent per
minute of audio.

## Music and video

These take a while, so they're jobs. The simplest way is to wait for it:

```ts
const job = await $llm.music.createAsync({ model: "vultr/ace-step-1.5", prompt: "gentle lullaby, music box and soft strings", seconds: 60, wait: "5m" })
if (job.status === "done") page.set("music", $llm.file(job.files[0]))
```

Or start it and look again later, when you'd rather not hold a request open:

```ts
const job = await $llm.music.createAsync({ model: "vultr/ace-step-1.5", prompt: "gentle lullaby", seconds: 60 })
page.set("music_job", job.id) // "vultr:music_02a2…"

// later, in a cron or the next request
const now = await $llm.music.getAsync(page.getString("music_job"))
if (now.status === "done") page.set("music", $llm.file(await $llm.music.contentAsync(now.id, 0)))
```

Video works the same with `$llm.video` (on Vultr and OpenRouter); `image` gives it a first frame to start from.

## What it costs

Media is billed per picture, per second or per character by your provider. `usage.costMicroUSD` says what a call cost, in millionths of a dollar: the provider's figure
when it reports one, else estimated from the model's listed prices (`usage.costEstimated`). `$llm.image.modelsAsync()` and
the others list each model's prices.
