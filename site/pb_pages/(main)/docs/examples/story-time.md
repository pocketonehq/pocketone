---
title: Story Time
description: A story told on any topic, read aloud with pictures and music - the six steps, the player, the guards, and the settings screen where its title, lengths, voice and prompts are set.
discussion: pocketone.dev/docs/examples/story-time
---

# Story Time

Try it at [sleep.pocketone.dev](https://sleep.pocketone.dev), where it's set up as Bedtime Story. Source:
[`ext/llm/examples/storytime`](https://github.com/pocketonehq/pocketone/tree/main/ext/llm/examples/storytime).

You give it a topic, or leave it empty and it picks one. A minute or two later it plays the story: one narrator
reading, pictures that slowly pan and zoom, the words lit up as they're said, and quiet music underneath. A slider picks
a short or a long story, within the lengths the app's admin allows. Each story is saved, so it can be replayed and
shared.

What it's for is set, not coded: its title and caption, the story lengths and pictures per minute, the narrator (one of
the model's voices, or a clone of a real one), the prompts and the models are all on its settings screen. The shipped
defaults (`_private/defaults.json`) make it a bedtime story app for adults: one topic told slowly and in depth.

```
pb_migrations/         stories, their listens, each call's cost, audio samples, recorded voices
pb_hooks/
  admin_plugins.pb.js  registers the settings screen with the dashboard
pb_admin_ext/
  storytime/main.js    the settings screen: Story Time in the top bar, with Set up (step by step, with a chat helper)
pb_pages/
  index.ets            ask for a story, or Inspire me; stories told, most listened to first
  inspire.ts           POST /inspire: a topic no story has covered yet
  view.ts              POST /view: counts a listen
  story.ts             POST /story: saves the record, starts making the story
  story/[id].ets       progress while it's made, then the player
  admin/+middleware.ts lets only superusers through to anything under /admin
  admin/tuning.ts      GET/POST /admin/tuning: the settings (superusers only)
  admin/sample.ts      POST /admin/sample: tries a piece with unsaved settings (superusers only)
  admin/export.ts      GET /admin/export, POST /admin/import: the settings and recordings as JSON (superusers only)
  admin/check.ts       POST /admin/check: transcribes a recording and compares it with its passage (superusers only)
  admin/stories.ts     POST /admin/stories: adds told stories missing from the stories bucket (superusers only)
  admin/assist.ts      POST /admin/assist: one turn of Set up's chat helper: new values for the fields you meant (superusers only)
pb_hooks/stories.pb.js  a deleted story leaves the stories bucket
  _private/defaults.json the defaults, in the export format
  _private/story.ts    the settings, the limits and the six steps
```

**Making a story takes longer than a request should wait**, so `POST /story` saves a `stories` record with status
`queued`, starts the work without awaiting it, and answers at once with the record's id. The work runs on in the
background, saving its progress on the record after each step. The story page polls the record through PocketBase's
records API (the collection's list and view rules are open, so anyone with the link can watch and play), and reloads
into the player when the status is `done`.

**The six steps** (`_private/story.ts`):

1. **Write.** `$llm.text.completeAsync` with a JSON schema returns the title, the topic, a description of what recurs
   in the pictures, and the story one sentence per item, in the prompt's language.
2. **Read it aloud.** `$llm.speech.speakAsync` records one voiceover of the whole story, so the narrator's voice stays
   the same throughout.
3. **Align.** `$llm.align.alignAsync` takes the voiceover and the sentences and returns when each sentence and each word
   is said. Nothing is transcribed: every word that comes back is one that was written.
4. **Place the pictures.** The timed transcript goes back to the text model, which chooses the sentence where each
   picture begins and what it shows, plus one style line for all of them and a prompt for the music.
5. **Paint** the pictures with `$llm.image.generateAsync`, all at once, and
6. **compose** the music with `$llm.music.createAsync({ wait })`, at the same time.

The files (the voiceover, the pictures, the music) are saved on the record with `$llm.file()`, and the story with its
timings in a JSON field. The pictures look alike because every prompt repeats the same style line and the same
description of what recurs.

**The player** (`story/[id].ets`) is plain HTML, CSS and a little JavaScript. One audio element plays the voiceover and
drives everything else from its current time: the picture whose start has passed fades in, its slow pan and zoom is a
CSS animation, placed by a negative `animation-delay` after a seek, and the caption shows the sentence being read with
each word brightening at its start time. The music loops under the voice at low volume. A scrubber, the space bar and
the arrow keys move through the story.

**Inspire me** sends the titles and topics of every story told so far, and the topics it already suggested on this
page, to the text model, which suggests one that none of them covers. **Listens:** after 10 seconds of actual listening
(time playing, not scrubbing), the player posts `/view`, which adds one to the story's `views` in a single SQL update so
listens at the same moment all count. The front page lists stories by it.

## The settings screen

Everything that shapes the app (what the front page says, how long stories may be, the narrator's voice, what the
models are told, how pictures and music are asked for, which models run and how much a day may cost) can be changed
without touching code, from **Story Time** in the top bar
of the PocketBase dashboard (`/_/`, superusers only), one sidebar entry per part. Each part has a **Sample** button, so you hear or see a change
before any listener does.

**Set up (experimental)**, first in the sidebar, goes a step at a time with a chat helper beside the fields. Step 1
asks what the site's general vibe should be, and your answer fills the title, the tagline under it and the writer
preamble (what every story is written from). Then keep talking: "I don't like the title" changes only the title, "make
the stories calmer" only the preamble. One call to the text model per turn gets the step's fields as they are and
answers, through a schema limited to those fields, with new values for the ones you meant. You can still edit the
fields by hand, and nothing is saved until **Save and next**. More steps (knowledge sources, research, the story) are
coming.

| Part | What you set | Sample |
|---|---|---|
| **Front page** | The title (its last word is highlighted), the caption under it, the prompt's placeholder and the note beside the buttons. | |
| **Knowledge** | The [LLM Search](/docs/extensions/llm-search) buckets the stories know (a Git repository, docs, files). The writer gets the best passages for each story's topic, and Inspire me draws topics from them. The told stories are a bucket of their own (`storytime-stories`), kept by the app, so Inspire me can tell what's been told. **Skip:** patterns for documents not worth a story (`go.mod`, `*.lock`, `toc*.md`), one per line. | **Inspire me** shows the candidates it drew, the told stories closest to each, and why it picked one. |
| **Mix** | How every story sounds when it plays: the voice's level (dBFS) and how far the music sits under it. The player measures each story's voice and music once and sets them to these, so quiet cloned voices, hot generated ones and loud songs all play balanced. | |
| **Story length** | The slider's ends (shortest and longest, in whole minutes) and where it starts, the narrator's pace in words per minute (how many words a story of that length gets), pictures per minute, and the most pictures a story gets. Shortest equal to longest hides the slider. | |
| **Narrator** | **Voice:** `default`, a calm middle-aged woman with a low, clear voice, generated afresh each call; `design`, a new voice made from the description in Instructions; or a clone, the voice of a recording you make on the screen (record, listen back, keep or delete; each take is compressed (3:1) and levelled before it's saved, and **Compress and level** does the same to an older one; **Check** transcribes a recording and shows how much of the passage it heard; recordings are superuser-only and sent with every story as speech's `reference`). **Instructions:** how `default` reads, or the voice itself for `design` ("A deep, calm older man's voice, slow and soft"). **Seed:** the same seed gives the same voice every time; empty lets it vary a little. **Speed:** 1 is normal. **Sample text:** what Sample reads. | Reads the sample text in that voice. `design` with no description is refused. |
| **Writing the story** | The writer's preamble: what the stories are like (who tells them, about what, the tone). The app adds what every story needs after it: the length, what to do when the listener leaves the topic to it, that it's read aloud, and the answer's fields. | Writes a 1-minute story on the topic you give (or one it picks) and shows its sentences. |
| **Pronunciation** | How words in this field are said, with examples ("WASM is said wazzum, SES is spelled out"). A cloned voice takes no instructions, so the writer lists the words a narrator could get wrong with a respelling; odd-looking words it left out (acronyms, names with capitals inside) are asked about in one small call. The voice reads the respelled text and the captions keep the written words. | |
| **Choosing the pictures** | The instructions that turn the timed transcript into pictures, their style and the music prompt (placeholder `{n}`), and the words added after every picture's prompt. | Paints one picture in the style you give. |
| **Music** | The words added after every story's music prompt, and how many seconds of music to make (it loops). | Plays 20 seconds for the music prompt you give. |
| **Inspire me** | The instructions for suggesting a topic no story has covered, and, with knowledge buckets, how a passage becomes a story request (placeholder `{count}`). | Suggests one. |
| **Models and budget** | Each step's model as `provider/model` (a provider in Settings → LLM), and the daily budget in US dollars, with what's been spent today. | |

**Sample** uses what's on the screen, saved or not, and shows which model answered, how long it took and what it cost.
Samples are recorded with every other model call, but the daily budget never stops them. **Save changes** stores the
settings; every story started after that uses it. **Put back the defaults** forgets them. **Export** downloads the saved
settings and every recording (audio included) as one JSON file, and **Import** loads one, so settings and a recorded voice
move between copies of the app (local to PocketHost and back). Import keeps recordings it already has and points the
narrator at its copy.

**What stories remember.** Since the settings can change, each story keeps how it was made: the seconds it took and the
models and voice that made it. The story's splash shows them with its date and cost, for example "Made Nov 22, 2026 in
21 s for $0.057", then writing `vultr/mimo-v2.6-flash`, voice `vultr/auk`, narrator `default, seed 7`.

**How it's built.**

- **Where the settings live.** The defaults are `_private/defaults.json`, an export file, so a new copy of the app starts
  from whatever you put there (models and budget can be overridden from the environment). A save stores the whole
  settings as one JSON blob in PocketOne's key-value store (`$kv`, no collection or migration), and `tuning(app)` merges
  it over the defaults each time a story or a suggestion starts.
  `/admin/sample` merges the screen's unsaved values over that again, so a sample never changes what listeners get.
- **The screen is a dashboard extension**, registered from `pb_hooks` the way
  [PocketHost's guide](https://pockethost.io/docs/admin-extensions) shows. The hook hands PocketBase the folder
  holding the screen:

  ```js
  // pb_hooks/admin_plugins.pb.js
  $app.onServe().bindFunc((e) => {
    e.uiExtensions.push({ name: "storytime", fs: $os.dirFS(`${__hooks}/../pb_admin_ext/storytime`) })
    e.next()
  })
  ```

  PocketBase serves the folder at `/_/extensions/storytime/` and loads its `main.js` into the dashboard. `main.js` adds
  the top-bar link (`app.store.headerLinks`) and a page per section (`app.routes.superuserOnly("#/storytime/narrator", ...)`),
  built from the dashboard's own pieces, and calls the `/admin/...` endpoints with `app.pb.send`, which carries
  the signed-in superuser's token. `pb_pages/admin/+middleware.ts` lets only superusers through to any of them.
- **Audio samples are saved, then played.** The dashboard plays media only from its own address (its
  Content-Security-Policy blocks `data:` audio), so each audio sample is saved in a `samples` collection, viewable by
  its random id only and trimmed to the last 20, and played from `/api/files/...`. Picture samples come back inline.

## Guards for a public copy

Every model call's cost (`usage.costMicroUSD`) is saved as a row; once the day's total
(from 00:00 UTC) reaches the budget, $1 by default, the form turns off with "Out of tokens for today" and the API
refuses new stories. Before anything is made, the app checks that each model's provider is set up, answering and
offering that model, and refuses the same way when it isn't. Stories stay within the lengths on the settings screen,
held both by the slider and by the API.

A 1-minute story with three pictures takes under a minute to make and costs a few cents on Vultr, most of it the
pictures and the music.
