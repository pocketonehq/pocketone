# Story Time

A PocketOne app with no Go (`pb_pages`, a few migrations and the stock `pocketone` binary) that tells a story from a
prompt or a topic it picks itself. What kind of story is set, not coded: its title and caption, the story lengths, the
narrator and the prompts are all on its settings screen, and the shipped defaults (`pb_pages/_private/defaults.json`)
make it a bedtime story app for adults: one topic (the universe, the mind, a moment in history) told slowly and in
depth. It's read aloud by one narrator, with pictures that slowly pan and zoom, the words lit up as they're said, and
a quiet song underneath. A slider picks a short or a long story, within the lengths the admin allows; the pictures follow from the length. Each story is saved, so it can
be replayed and shared. It runs live at [sleep.pocketone.dev](https://sleep.pocketone.dev), set up as Bedtime Story.

Every sentence is read aloud and shown as a caption lined up with the voice, so the sentences themselves must be speakable: the app strips Markdown and symbols, and has the writer rewrite any sentence that still looks like code (a path, a command, a flag) in plain words before it's narrated.

It shows `$llm` doing a job longer than one request:

1. **write** (`$llm.text.completeAsync` with a schema): a title, the topic, what recurs in the pictures (a place, a
   person, an object) described for an illustrator, and the story one sentence per item, in the prompt's language;
2. **read it aloud** (`$llm.speech.speakAsync`): one voiceover of the whole story;
3. **align** (`$llm.align.alignAsync`): when each sentence and word is said in the voiceover;
4. **place the pictures** (`completeAsync` again): the model reads the timed transcript and chooses where each picture
   begins and what it shows, plus the look shared by every picture and a prompt for the song;
5. **paint** the pictures (`$llm.image.generateAsync`, all at once) and 6. **compose** the song (`$llm.music.createAsync`
   with `wait`), at the same time.

The pictures stay consistent because every prompt repeats the same description of what recurs and the same style
line. A story in a language `auk` doesn't speak (it speaks English and Chinese) is read by `chatterbox`.

`POST /story` saves a `stories` record and starts the work without awaiting it, then answers at once. The work saves
each step's progress and result on the record, and the page polls `GET /progress?id=` until it's done (`listRule` and
`viewRule` are open, so anyone with the link can watch and play). Every `$llm` call is awaited, so the models work
off the app's JS VM and the app keeps serving pages meanwhile.

**Tell me a story** picks the topic itself: with knowledge buckets, **Inspire me** sends the titles and topics of every
story told to the text model, which suggests a topic none of them covers, and the story is made from it; without them,
the writer chooses. A link under the button opens a prompt
box for a story about something in particular (`POST /inspire` still answers a topic on its own). **Listens:** once a listener has
heard 10 seconds (time playing, not where the scrubber is), the player posts `/view`, which adds one in SQL
(`views = views + 1`); the front page lists stories by it.

```
pb_migrations/1763800000_stories.js   the stories collection: prompt, length, status, progress, the story with its timings, files
pb_migrations/1763800001_stories_views.js   views: listens of 10 seconds or more
pb_migrations/1763800002_costs.js       costs: each model call's cost, for the daily budget
pb_migrations/1763800004_samples.js     samples: audio samples the dashboard plays
pb_migrations/1763800005_voices.js      voices: recordings a cloned narrator imitates (superusers only)
pb_migrations/1763800006_stories_log.js   log and attempts: each story's step-by-step log (hidden) and how many times it was tried
pb_hooks/admin_plugins.pb.js            registers the dashboard screen (a superuser UI extension)
pb_admin_ext/storytime/main.js          Settings → Story Time in the dashboard: the settings, with Sample buttons
pb_pages/
  index.ets            ask for a story (one click and it picks the topic, or a prompt; minutes, pictures); stories told, most listened to first
  inspire.ts           POST /inspire {avoid?}: a topic no story has covered yet (the model sees every title)
  admin/+middleware.ts lets only superusers through to anything under /admin
  admin/tuning.ts      GET/POST /admin/tuning: the settings, for the dashboard screen (superusers only)
  admin/sample.ts      POST /admin/sample: tries a voice, music, a picture, a story's text or an idea with unsaved settings
  admin/export.ts      GET /admin/export: the saved settings and every recording as one JSON file, the format of _private/defaults.json (superusers only)
  admin/import.ts      POST /admin/import: loads that file, keeping recordings already here (superusers only)
  admin/check.ts       POST /admin/check {clip}: transcribes a recording and compares it with its passage (superusers only)
  admin/stories.ts     POST /admin/stories: adds told stories missing from the stories bucket (superusers only)
  view.ts              POST /view {id}: counts a listen (the player sends it after 10 seconds heard)
  story.ts             POST /story {prompt?, minutes?}: saves the record, starts making it (no prompt: it picks the topic)
  story/[id].ets       progress while it's made, then the player (scrubber, space to pause, arrows to skip 10 s)
  progress.ts          GET /progress?id=: a story's status and progress, for its page's poll; retries a stalled story
  _private/story.ts    the models, the limits and the six steps
```

## Run it

```bash
cd ext/llm/examples/storytime
pocketone superuser upsert admin@example.com password --dir=pb_data
pocketone serve --dir=pb_data
```

Add a Vultr provider named `vultr` in Settings → LLM (the dashboard at `/_/`), then open `http://127.0.0.1:8090`. A
1-minute story with 3 pictures takes under a minute to make and costs a few cents on Vultr, most of it the song and the
pictures; a 5-minute story with 5 pictures, about a minute and a half and 7 cents.

The models are set in [`_private/story.ts`](pb_pages/_private/story.ts), each overridable from the environment:
`STORY_TEXT_MODEL`, `STORY_IMAGE_MODEL`, `STORY_SPEECH_MODEL`, `STORY_ALIGN_MODEL`, `STORY_MUSIC_MODEL`. So are the
limits for a public copy: `STORY_MAX_MAKING` stories in the making at once (3), `STORY_MAX_PER_HOUR` started an hour
(20), and `STORY_DAILY_BUDGET` dollars of model calls a day, counted from 00:00 UTC (1). A story is at most 5 minutes
with 5 pictures (`MAX`); the form and `POST /story` both hold to it.

## Tuning, in the dashboard

Settings → Story Time in the PocketBase dashboard (`/_/`) holds what shapes the app: the front page's title and caption, the story lengths (the slider's shortest and longest, where it starts, the narrator's words per minute, pictures per minute, the most pictures), the narrator (voice,
instructions, seed, speed), the writing, picture-choosing and Inspire me prompts, how words are said (**Pronunciation**), what's added to each picture and music
prompt, the models and the daily budget. Each part has a **Sample** button that tries it with what's on the screen,
saved or not: the voice reads a paragraph, the music plays 20 seconds, a picture is painted, a 1-minute story is
written, or an idea suggested. **Save changes** stores it as one JSON blob in PocketOne's key-value store (`$kv`); new stories use it, on top of
the defaults in `_private/defaults.json`. A seed makes `auk`'s generated voice the same from story to story. The narrator
can also be a clone: record a passage on the screen, and every story is read in that voice (`reference` on the speech
call, from the `voices` collection). A cloned voice is as loud as its recording, so each take is compressed and levelled in the browser before it's saved (**Compress and level** fixes an older one), and the player does the final mix: it measures each story's voice and music and levels them to the **Mix** settings. **Export** and **Import** move the saved settings and the recordings between copies
of the app as one JSON file.

**Knowledge:** tick LLM Search buckets on the settings screen and the stories know them. The writer's call carries
the best passages for the topic, each labelled with what it is part of (the nearest folder with a README in the bucket,
by the README's title), so the story names its subject before the details. Inspire me draws a topic in four steps:
random passages from the buckets (`$llm.search.sample`, at most one per README'd folder, none matching **Skip**), one
story request per passage written with the **Ideas from knowledge** prompt, the told stories nearest each candidate (each finished
story goes into the `storytime-stories` bucket; `pb_hooks/stories.pb.js` takes a deleted one out), and the candidate
those stories cover least. A story asked for with no topic gets one from Inspire me first.

`profiles/` holds settings files to Import: `pocketone.json` turns the app into PocketOne Stories, stories about
PocketOne told by its creator (the front page, the prompts, how developers say words, the documents to skip, the sample
text). Everything specific to a body of knowledge lives in settings, never in the code, so a profile is all it takes to
point the app at other content. Import lays a file over the current
settings, so a profile changes only what it has: the voice, models, budget and lengths stay.

The screen is a superuser UI extension registered from `pb_hooks`, the way
[PocketHost's guide](https://pockethost.io/docs/admin-extensions) shows: `$app.onServe()` pushes
`{ name: "storytime", fs: $os.dirFS(...) }` onto `e.uiExtensions`, and PocketBase loads `pb_admin_ext/storytime/main.js`
into the dashboard. The dashboard only plays audio from its own origin, so audio samples are saved in a `samples`
collection (the last 20, viewable by their random id) and played from `/api/files/...`.

Each story also keeps how it was made (seconds taken, the models and the voice), shown on its splash with the date and
cost, since the settings can change.

## Guards

- **Spending:** every model call's cost (`usage.costMicroUSD`, millionths of a dollar) is saved as a row in `costs`.
  Once the day's rows add up to the budget, the front page says "Out of tokens for today; try again tomorrow. Please
  enjoy something from our archives." and turns the form off, and `POST /story` and `POST /inspire` answer 429 with the
  same message. A story already being made finishes.
- **Models:** before anything is made, `health()` checks that `$llm` exists and that each model's provider is in
  Settings → LLM, answers, and offers that model (Vultr lists its aligner only for a working key, so a wrong key shows
  too). Checked at most once a minute. If not, the front page says the storyteller can't reach its models and turns
  the form off, and both endpoints answer 503, with the reason for admins in `reason` and the server log.
- **Progress:** the front page lists stories being made with their progress, and a story's page shows the six steps
  and how long it has run, kept current by polling.
- **Log:** every step writes to the story's `log` (when it started and ended, how long it took, any error), a hidden
  field superusers read on the record in the dashboard.
- **Stalls and retries:** each step has a time limit (`STEP_TIMEOUT` in `_private/story.ts`: 5 minutes for writing and
  reading aloud, 9 for the pictures and song). A story whose step runs past it (a model call that never answers, or
  the app stopped mid-story) is noticed the next time its page polls or someone starts a story, since a timer can't
  outlive the request that started the story. A step that fails, or stalls, is tried again once: the retry skips every
  step already saved on the record (the story, the narration, the timings, the pictures' places), and the record counts
  the attempts, so a story is never tried a third time and a stalled call that answers late is ignored. After the
  second attempt the story fails, with the reason in `error`.
