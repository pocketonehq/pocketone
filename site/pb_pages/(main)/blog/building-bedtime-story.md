---
title: "Building Bedtime Story: six model calls and a little CSS"
description: How sleep.pocketone.dev turns a topic into a narrated, illustrated story with music. A walkthrough of the pipeline, the player, the guards that keep a public copy cheap, and the tuning screen in the dashboard.
date: 2026-11-22
author: Ben Allfree
tags: examples, AI, Pages, llm
---

I've really come to enjoy those sleepy-time YouTube videos, especially the ones that imitate a certain popular British physicist. I was curious how they're made, and I realized the Vultr inference cloud had all the primitives for it: a writer, a narrator, pictures, music. So I thought it would be fun to make my own bedtime story app, just to see how close I could get. That's [sleep.pocketone.dev](https://sleep.pocketone.dev).

Vultr even has voice cloning, where you give it a short recording of a real voice and it reads in that voice. I started with one of the simulated voices, but then I thought it would be funny if the creator of PocketOne was the one telling you a bedtime story. So now it's me. I recorded about twenty seconds of myself reading slowly, and every story is narrated in my voice.

Give it a topic, or leave it empty and let it pick one: the universe, the mind, a moment in history. A minute later it plays the story. One narrator reading, pictures that slowly pan and zoom, the words lighting up as they're said, quiet music underneath. Every story is saved, so you can replay it and share the link.

![The player mid-story: a galaxy through a window, and the caption "everything we see is matter" with the word being spoken lit up](/blog/img/bedtime-player-galaxy.jpg)

It's still a plain PocketOne app with no Go. Source is at [`ext/llm/examples/bedtime`](https://github.com/pocketonehq/pocketone/tree/main/ext/llm/examples/bedtime).

![The front page: a box for the topic, minutes and pictures, Tell me a story and Inspire me, then the stories told so far](/blog/img/bedtime-home.jpg)

```
pb_migrations/         stories, listens, each call's cost, the saved tuning, audio samples
pb_hooks/
  admin_plugins.pb.js  registers the tuning screen with the dashboard
pb_admin_ext/
  bedtime/main.js      the tuning screen: Settings → Bedtime Story
pb_pages/
  index.ets            ask for a story, or Inspire me
  story/+api.ts        POST /story: saves the record, starts making the story
  story/[id].ets       progress while it's made, then the player
  inspire/+api.ts      POST /inspire: a topic no story has covered yet
  view/+api.ts         POST /view: counts a listen
  admin/...            the tuning and sample endpoints (superusers only)
  _private/story.ts    the defaults, the limits and the six steps
```

### Don't make the request wait

Making a story takes longer than any request should hang around. So `POST /story` saves a `stories` record with status `queued`, kicks off the work without awaiting it, and answers right away with the id:

```ts
const r = new Record(ctx.app.findCollectionByNameOrId('stories'))
r.set('prompt', (prompt || '').trim().slice(0, 500) || story.ANY_TOPIC)
r.set('status', 'queued')
ctx.app.save(r)

// not awaited: the story is made after this request has answered
story.make(ctx.app, r.id)
return ctx.res.json(200, { id: r.id })
```

`make()` saves its progress on the record after every step ("Writing the story," "Reading it aloud," "Listening for when each word is said"...). The story page just polls the record through PocketBase's normal records API and reloads into the player when the status hits `done`. The collection's view rule is open, so anyone with the link can watch it being made.

So the job queue is just a record and a status field.

### Six steps

All of it lives in `_private/story.ts`.

**1. Write.** `$llm.text.completeAsync` with a JSON schema returns the title, the topic, a description of what recurs in the pictures, and the story one sentence per item. It writes in whatever language you asked in.

**2. Read it aloud.** `$llm.speech.speakAsync` records one voiceover of the whole story, so the narrator sounds the same from start to finish. That call carries my recording as a `reference`, which is how it ends up in my voice (more on that below). If the main voice model doesn't speak your language, a second one reads it, and it clones too, so it's still me, just in another language.

**3. Align.** This is the step that makes the player work. `$llm.align.alignAsync` takes the voiceover and the sentences and returns when each sentence and each word is said:

```ts
const a = await $llm.align.alignAsync({
  model: t.models.align,
  audio: voice.file,
  text: story.sentences.join('\n'),
  mode: 'speech',
  language: story.language,
})
story.lines = a.lines.map((l) => ({ index: l.index, text: l.text, start: l.start, end: l.end }))
story.words = a.words.map((x) => ({ line: x.line, text: x.text, start: x.start, end: x.end }))
```

Nothing is transcribed. Every word that comes back is one the model wrote. `llm.align/v1` is new in this release because of this app.

**4. Place the pictures.** The timed transcript goes back to the text model, which picks the sentence where each picture begins and what it shows. It also writes one style line for every picture and a prompt for the music.

**5 and 6. Paint and compose, at the same time.** The pictures come from `$llm.image.generateAsync`, all at once, and the music from `$llm.music.createAsync({ wait })` alongside them. The pictures look like they belong together because every prompt repeats the same style line and the same description of what recurs.

The voiceover, pictures and music land on the record as ordinary PocketBase files with `$llm.file()`. The story and its timings go in a JSON field.

### The player

`story/[id].ets` is plain HTML, CSS and a little JavaScript. One `<audio>` element plays the voiceover and everything else follows its current time.

- The picture whose start has passed fades in. Its slow pan and zoom is a CSS animation, and after a seek it's put back in place with a negative `animation-delay`.
- The caption shows the sentence being read, and each word brightens at its start time.
- The music loops underneath at low volume.
- A scrubber, the space bar and the arrow keys move through the story.

There's no video file and no canvas. The browser does all of it.

![A Good Day's Work at 0:15: a desk lamp, tea and an open notebook, with the caption reading along](/blog/img/bedtime-player.jpg)

![The same story at 0:54, a later picture: the lamp and a candle lit beside the tea and notebook](/blog/img/bedtime-player-later.jpg)

It works the same on a phone. The picture fills the screen and the caption sits above the controls.

<p style="display: flex; gap: 1rem; justify-content: center; flex-wrap: wrap"><img src="/blog/img/bedtime-phone-home.jpg" alt="The front page on a phone" width="260"> <img src="/blog/img/bedtime-phone-player.jpg" alt="The player on a phone: a candle beside an open book, the caption about Aquinas" width="260"></p>

### Inspire me and listens

**Inspire me** sends the titles and topics of every story told so far to the text model and asks for one none of them cover. So the list keeps growing in new directions.

**Listens** count after 10 seconds of actual playing, not scrubbing. The player posts `/view`, which bumps the counter in one SQL update so two listens at the same moment both count:

```ts
ctx.app.db()
  .newQuery("UPDATE stories SET views = views + 1 WHERE id = {:id} AND status = 'done'")
  .bind({ id })
  .execute()
```

The front page lists stories by listens.

### Keeping a public copy cheap

Anyone can make a story on the live site, and it spends my money. So there are guards.

- **A daily budget.** Every model call's `usage.costMicroUSD` is saved as a row. Once today's total (from 00:00 UTC) hits the budget, $1 by default, the form switches off with "Out of tokens for today" and the API refuses new stories.
- **Health checks.** Before anything is made, the app checks that each model's provider is set up, answering and offering that model. If not, it refuses politely and the reason names the provider to add.
- **Limits.** At most 5 minutes and 5 pictures, held in the form and again in the API.

A 1-minute story with three pictures takes under a minute and costs a few cents on Vultr. Most of that is the pictures and the music.

### Tuning it from the dashboard

The voice, the prompts, the picture style, the music, which models run and the daily budget all change without touching code. They live in **Settings → Bedtime Story** in the PocketBase dashboard, and every part has a **Sample** button, so I can hear a new voice or see a new style before any listener does.

The screen is a dashboard extension, registered from `pb_hooks` the way [PocketHost's guide](https://pockethost.io/docs/admin-extensions) shows:

```js
// pb_hooks/admin_plugins.pb.js
$app.onServe().bindFunc((e) => {
  e.uiExtensions.push({ name: 'bedtime', fs: $os.dirFS(`${__hooks}/../pb_admin_ext/bedtime`) })
  e.next()
})
```

PocketBase serves that folder and loads its `main.js` into the dashboard, which adds the Settings entry and the page using the dashboard's own pieces.

When I first wrote that hook, it didn't work. In QuickJS, `$os.dirFS` turned into a string on its way back to Go, and the app stopped at startup. Inside a Pocker instance, the sandbox wouldn't let an app bind its own serve hook at all. Both are fixed in [alpha.13](/blog/pocketone-v0-0-1-alpha-13). Building example apps turns up bugs like these, which is a big part of why I make them.

Saved tuning goes in a `settings` record that only superusers can read. It's merged over the defaults in `_private/story.ts` each time a story starts. Samples merge the unsaved values on top of that, so trying something never changes what listeners get. Each story also remembers how it was made: the time it took, the cost, and the models and voice. The story's splash shows them.

![A story's splash: the title over its first picture, Play the story, then "Made Nov 22, 2026 in 18 s for $0.057" and the model behind each step](/blog/img/bedtime-splash.jpg)

### Reading in my voice

Cloning turned out to be one more field on the speech call. You give it the clip and, optionally, the words in the clip, and the model reads your text in that voice:

```ts
const clip = app.findRecordById('voices', t.voice.clip)
const voice = await $llm.speech.speakAsync({
  model: 'vultr/auk',
  text: story.sentences.join(' '),
  reference: { audio: $llm.fromRecord(clip, 'audio'), text: clip.getString('passage') },
})
```

The recording lives in a `voices` collection that only superusers can see. The stories are public anyway, but I didn't want the raw clip sitting out there for anyone to download. `$llm.fromRecord` reads the stored file with its bytes, so it works the same whether files are on disk or in S3.

Recording happens right in the dashboard. On the Narrator screen there's a short passage to read, a Record button, and a way to listen back before you save it. I read it the way I want the stories told, slow and quiet, because the delivery comes from the recording. There's no "sound sleepy" instruction for a cloned voice. Vultr doesn't keep the voice either, so the clip goes along with every story, which is one more reason to keep it short.

I made this general while I was in there. `llm.speech/v1` has the `reference` field for every provider, and each voice says whether it's built in, designed or a clone, so an app can tell which models clone. Vultr and OpenRouter do it. Cloudflare and other OpenAI-compatible APIs say so plainly with a `not_supported` error instead of quietly reading in some other voice.

Before I recorded myself, I checked it was really cloning and not just ignoring the clip. I fed it a clip of one of Vultr's male voices and measured the pitch: the clone came out at the same 125 Hz as the clip, and the default narrator, a woman's voice, sits around 186 Hz. Good enough to prove the point. One rule I'd keep, though: clone your own voice, or one you have permission to use.

### Run it yourself

```bash
cd ext/llm/examples/bedtime
pocketone superuser upsert admin@example.com password --dir=pb_data
pocketone serve --dir=pb_data
```

Add a provider in Settings → LLM. The defaults use Vultr models for writing, speech, alignment, pictures and music. On another provider, set each step's model in Settings → Bedtime Story.

The live copy runs on PocketHost, deployed with `pocketone deploy --restart`.

The [Bedtime Story walkthrough](/docs/examples/bedtime-story) in the docs has the full reference, including every tuning field. If you missed it, here's [how Docs Chat became the ask starter kit](/blog/building-docs-chat).
