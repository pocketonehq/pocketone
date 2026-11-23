# Bedtime Story

A PocketOne app with no Go (`pb_pages`, one migration and the stock `pocketone` binary) that makes a bedtime story
for adults: one topic (the universe, the mind, a moment in history) told slowly and in depth, from a prompt or a topic
it picks itself. It's read aloud by one narrator, with pictures that slowly pan and zoom, the words lit up as they're said, and
a quiet song underneath. You pick its length in minutes and how many pictures it gets. Each story is saved, so it can
be replayed and shared. It runs live at [sleep.pocketone.dev](https://sleep.pocketone.dev).

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
each step's progress on the record, and the page polls the record through the records API (`listRule` and `viewRule`
are open, so anyone with the link can watch and play) until it's done. Every `$llm` call is awaited, so the models work
off the app's JS VM and the app keeps serving pages meanwhile.

**Inspire me** sends the titles and topics of every story told (and the page's earlier suggestions) to the text model,
which suggests a topic none of them covers; the page drops it into the prompt box. **Listens:** once a listener has
heard 10 seconds (time playing, not where the scrubber is), the player posts `/view`, which adds one in SQL
(`views = views + 1`); the front page lists stories by it.

```
pb_migrations/1763800000_stories.js   the stories collection: prompt, length, status, progress, the story with its timings, files
pb_migrations/1763800001_stories_views.js   views: listens of 10 seconds or more
pb_migrations/1763800002_costs.js       costs: each model call's cost, for the daily budget
pb_migrations/1763800003_settings.js    settings: the saved tuning
pb_migrations/1763800004_samples.js     samples: audio samples the dashboard plays
pb_hooks/admin_plugins.pb.js            registers the dashboard screen (a superuser UI extension)
pb_admin_ext/bedtime/main.js            Settings → Bedtime Story in the dashboard: tuning, with Sample buttons
pb_pages/
  index.ets            ask for a story (prompt, minutes, pictures, Inspire me); stories told, most listened to first
  inspire.ts           POST /inspire {avoid?}: a topic no story has covered yet (the model sees every title)
  admin/tuning.ts      GET/POST /admin/tuning: the tuning, for the dashboard screen (superusers only)
  admin/sample.ts      POST /admin/sample: tries a voice, music, a picture, a story's text or an idea with unsaved tuning
  view.ts              POST /view {id}: counts a listen (the player sends it after 10 seconds heard)
  story.ts             POST /story {prompt?, minutes?, pictures?}: saves the record, starts making it (no prompt: it picks the topic)
  story/[id].ets       progress while it's made, then the player (scrubber, space to pause, arrows to skip 10 s)
  _private/story.ts    the models, the limits and the six steps
```

## Run it

```bash
cd ext/llm/examples/bedtime
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

Settings → Bedtime Story in the PocketBase dashboard (`/_/`) holds what shapes every story: the narrator (voice,
instructions, seed, speed), the writing, picture-choosing and Inspire me prompts, what's added to each picture and music
prompt, the models and the daily budget. Each part has a **Sample** button that tries it with what's on the screen,
saved or not: the voice reads a paragraph, the music plays 20 seconds, a picture is painted, a 1-minute story is
written, or an idea suggested. **Save changes** stores it in the `settings` collection; new stories use it, on top of
the defaults in `_private/story.ts`. A seed makes `auk`'s generated voice the same from story to story.

The screen is a superuser UI extension registered from `pb_hooks`, the way
[PocketHost's guide](https://pockethost.io/docs/admin-extensions) shows: `$app.onServe()` pushes
`{ name: "bedtime", fs: $os.dirFS(...) }` onto `e.uiExtensions`, and PocketBase loads `pb_admin_ext/bedtime/main.js`
into the dashboard. The dashboard only plays audio from its own origin, so audio samples are saved in a `samples`
collection (the last 20, viewable by their random id) and played from `/api/files/...`.

Each story also keeps how it was made (seconds taken, the models and the voice), shown on its splash with the date and
cost, since the tuning can change.

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
  and how long it has run, both kept current by polling the records API.

A story that was being made when the app stopped shows as unfinished after 15 minutes.
