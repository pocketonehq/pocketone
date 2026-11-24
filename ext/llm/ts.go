package llm

// TypeScript implements the jsvm types hook: $llm's declarations, over the llm.* contracts' own types.
func (*Extension) TypeScript() string { return typeScript }

const typeScript = `/** A generated or sent file: data is base64. $llm.file(f) makes it a PocketBase file for record.set. */
type LlmFile = { name?: string; type?: string; data?: string; url?: string }

type LlmUsage = { inputTokens?: number; outputTokens?: number; cachedTokens?: number; reasoningTokens?: number; seconds?: number; /** what the call cost, in millionths of a US dollar (1000000 = $1); 0 when free or not known */ costMicroUSD: number; costEstimated?: boolean }

type LlmModels = {
  models: { id: string; provider: string; type: string; name?: string; kinds: string[]; inputs?: string[]; reasoning?: boolean;
    contextTokens?: number;
    /** USD per unit ("1M input tokens", "1M output tokens", "1M cached input tokens", "1M tokens", "second", "megapixel",
     * "image", "1K characters", "request"): listed by the provider, or measured from this app's billed calls (calls: how
     * many). None: the price isn't known. */
    prices?: { unit: string; usd: number; source?: "listed" | "measured"; calls?: number }[]; description?: string;
    /** parameters, as its maker names them: "1.7B", "30B (3B active)" */
    size?: string;
    /** "tools", "structured output", "vision", "audio input", "voice cloning", "voice design", "built-in voices", "lyrics" */
    features?: string[];
    /** ISO 639-1 codes */
    languages?: string[] }[]
}

type LlmJob = { id: string; status: "queued" | "running" | "done" | "failed"; progress?: number; files?: LlmFile[]; outputs?: number; error?: string; model: string; usage: LlmUsage }

type LlmMessage = { role: "system" | "user" | "assistant"; content: string }

type LlmTextInput = {
  /** "provider/model", e.g. "vultr/glm-5.3". */
  model: string
  enrich?: LlmEnrich
  system?: string
  prompt?: string
  messages?: LlmMessage[]
  /** A JSON schema the answer must match; the parsed answer is then in data. */
  schema?: object | string
  maxTokens?: number
  temperature?: number
  stop?: string[]
  /** How much a reasoning model thinks first: "" (its default), "off", "low", "medium", "high". */
  reasoning?: "" | "off" | "low" | "medium" | "high"
  /** The provider's own fields, passed through. */
  options?: Record<string, any>
  /** Cancels the call, provider request included: the promise rejects with signal.reason. */
  signal?: AbortSignal
}

/** A passage enrich added to the prompt, from a search bucket. */
type LlmPassage = { bucket: string; document: string; heading?: string; text: string; score: number }

/** Adds the best passages from the app's search buckets after the prompt: the buckets, or { buckets, limit?, query? }. */
type LlmEnrich = string[] | { buckets: string[]; limit?: number; query?: string;
  /** who the answer is for (ctx.auth, or null for a guest): passages from records they can't view are left out */
  viewer?: core.Record | null }

type LlmTextAnswer = { text: string; data?: any; reasoning?: string; stopReason?: string; model: string; usage: LlmUsage;
  /** what enrich added to the prompt, best first */
  passages?: LlmPassage[] }

type LlmModelsOpts = { provider?: string; signal?: AbortSignal }

/**
 * Cloud models (the llm.* contracts): each call names provider/model; providers and keys are set in Settings → LLM.
 * Every call that reaches a provider is async (its name ends in Async) and runs off the app's VM, so other requests keep
 * being served while a model works: await it in an async handler. Each options object takes a signal (an AbortSignal):
 * { signal: AbortSignal.timeout(30_000) } gives up after 30 s and cancels the provider's request.
 */
type LlmAPI = {
  text: {
    /** Answers a prompt or a conversation: model is "provider/model". With schema, the answer's data is JSON in that shape. */
    completeAsync(opts: LlmTextInput): Promise<LlmTextAnswer>
    /** Like completeAsync, calling onText with each piece of the answer as it's written (false stops it). */
    streamAsync(opts: LlmTextInput, onText?: (text: string) => boolean | void, onReasoning?: (text: string) => boolean | void): Promise<LlmTextAnswer>
    /** About how many tokens a text or a conversation is (four characters each); local, not a provider call. */
    count(textOrMessages: string | LlmMessage[] | { text?: string; system?: string; messages?: LlmMessage[] }): number
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  image: {
    /**
     * Makes pictures from a prompt: n of them, at a size or aspect ratio the provider supports. references are pictures
     * it draws from: one to change, or one whose style and characters a new scene keeps. Only models with the feature
     * "reference images" take them (Vultr, OpenRouter).
     */
    generateAsync(opts: { model: string; prompt: string; references?: LlmFile[]; n?: number; size?: string; aspectRatio?: string; options?: Record<string, any>; signal?: AbortSignal }): Promise<{ files: LlmFile[]; model: string; usage: LlmUsage }>
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  speech: {
    /** reference clones a voice: the text is read in the voice of that clip (leave voice and instructions out). Providers that don't clone refuse it. */
    speakAsync(opts: { model: string; text: string; voice?: string; format?: string; speed?: number; instructions?: string; reference?: { audio: LlmFile; text?: string }; options?: Record<string, any>; signal?: AbortSignal }): Promise<{ file: LlmFile; model: string; usage: LlmUsage }>
    /** A model clones when one of its voices has mode "clone". */
    voicesAsync(model: string): Promise<{ id: string; name?: string; language?: string; gender?: string; mode?: "builtin" | "design" | "clone"; description?: string }[]>
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  transcribe: {
    /** Writes down what's said in a recording. */
    transcribeAsync(opts: { model: string; audio: LlmFile; language?: string; prompt?: string; options?: Record<string, any>; signal?: AbortSignal }): Promise<{ text: string; language?: string; model: string; usage: LlmUsage }>
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  /** Forced alignment: when each line and word of a known text is said in its audio (captions, karaoke). */
  align: {
    /** When each line and word of text is said in audio. */
    alignAsync(opts: { model: string; audio: LlmFile; text: string; mode?: "speech" | "lyrics"; language?: string; options?: Record<string, any>; signal?: AbortSignal }): Promise<{
      duration: number
      lines: { index: number; text: string; start: number; end: number; score: number; certain: boolean; tag?: string }[]
      words: { line: number; text: string; start: number; end: number; score: number }[]
      unaligned?: { line: number; text: string; reason: string }[]
      model: string
      usage: LlmUsage
    }>
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  /** Music is a job: createAsync with wait ("5m") resolves once it's done, files included; without, follow it with getAsync. */
  music: {
    /** Starts a piece of music; with wait ("5m") it resolves once it's done, files included. */
    createAsync(opts: { model: string; prompt: string; lyrics?: string; seconds?: number; wait?: string; options?: Record<string, any>; signal?: AbortSignal }): Promise<LlmJob>
    /** A job by id: its status, and its files once it's done. */
    getAsync(id: string): Promise<LlmJob>
    /** A finished job's file (index for jobs with several), downloaded. */
    contentAsync(id: string, index?: number): Promise<LlmFile>
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  video: {
    /** Starts a video, optionally from a picture; with wait it resolves once it's done. */
    createAsync(opts: { model: string; prompt: string; image?: LlmFile; seconds?: number; size?: string; aspectRatio?: string; wait?: string; options?: Record<string, any>; signal?: AbortSignal }): Promise<LlmJob>
    /** A job by id: its status, and its files once it's done. */
    getAsync(id: string): Promise<LlmJob>
    /** A finished job's file (index for jobs with several), downloaded. */
    contentAsync(id: string, index?: number): Promise<LlmFile>
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  embed: {
    /** Turns each text into a vector, for search and similarity. */
    embedAsync(opts: { model: string; input: string[]; dimensions?: number; options?: Record<string, any>; signal?: AbortSignal }): Promise<{ vectors: number[][]; model: string; usage: LlmUsage }>
    /** This kind's models across the providers in Settings → LLM (or one: { provider }), as provider/model. */
    modelsAsync(opts?: LlmModelsOpts): Promise<LlmModels>
  }
  /** A generated file (with its bytes) as a PocketBase file: record.set("pictures+", $llm.file(r.files[0])). */
  file(f: LlmFile): any
  /** The same, downloading a file the provider gave only a URL for. */
  fileAsync(f: LlmFile): Promise<any>
  /** A record's stored file (the first in a file field) as an LlmFile with its bytes, to send to a model: speakAsync({ reference: { audio: $llm.fromRecord(rec, "clip"), text } }). */
  fromRecord(record: core.Record, field: string): LlmFile
  /** Stored conversations (llm.chat/v1, the llm-chat extension). Only sendAsync reaches a provider. */
  chat: {
    /** Starts a chat and returns its id. */
    new(opts?: { title?: string; meta?: Record<string, any> }): string
    /** A stored chat by id, with the options every send uses. */
    open(id: string, opts?: LlmChatOptions): LlmChat
    /** Chats, most recently updated first; meta keeps those whose meta has each key with that value. */
    list(opts?: { meta?: Record<string, any>; limit?: number; offset?: number }): LlmChatInfo[]
    /** Deletes every chat whose meta matches; returns how many. */
    forget(opts: { meta: Record<string, any> }): number
  }
  /** Buckets of the app's content, searched by meaning (llm.search/v1, the llm-search extension). Only searchAsync
   * reaches a provider (it embeds the query); everything else is local (a URL or collection is read in the background). */
  search: {
  /** Every bucket, by name. */
  buckets(): SearchBucket[]
  bucket(name: string): SearchBucket
  /** Makes the bucket if it's missing (call it at startup for the bucket your app needs) and returns it. */
  ensure(o: string | { name: string; title?: string; description?: string; owner?: string }): SearchBucket
  update(o: { name: string; title?: string; description?: string; maxDocuments?: number; maxBytes?: number }): SearchBucket
  /** Deletes a bucket and everything in it. */
  deleteBucket(name: string): number
  /** A bucket's sources: files, text, folders, zips, pages, repositories, collections. */
  sources(bucket: string): SearchSource[]
  /** Adds a file or text as its own source (replacing the source of that name, or the one named by replace). */
  add(o: { bucket: string; name: string; text: string; kind?: "file" | "text"; replace?: string }): SearchSource
  /** Adds a web page or a Git repository (kind decided from the URL), read now and every hour, day or week. */
  addURL(o: { bucket: string; url: string; kind?: "page" | "git"; every?: "hour" | "day" | "week"; commits?: number; exclude?: string[] }): SearchSource
  /** Adds a collection: a document per record the filter keeps, kept in sync as records are saved and deleted. */
  follow(o: { bucket: string; collection: string; fields: string[]; filter?: string }): SearchSource
  /** Adds a folder of the app's own files on the server (path relative to the app's folder: "pb_pages/docs"), read
   * now and again every few minutes. url ("/docs/{path}") links each result to the page its file is published at, with
   * the section's anchor. */
  addFolder(o: { bucket: string; path: string; include?: string[]; exclude?: string[]; url?: string }): SearchSource
  /** Reads a source again now. */
  refresh(bucket: string, source: string): SearchSource
  /** Takes sources out of a bucket, with their documents. */
  remove(bucket: string, sources: string | string[]): number
  documents(bucket: string | { bucket: string; source?: string; prefix?: string }): SearchDocument[]
  /** Chunks picked at random from the buckets named (every bucket when none): material to draw from, not matches.
   * Local and free (nothing is embedded). count defaults to 5, at most 50; prefix keeps documents whose name starts with it. */
  sample(o: string | { buckets?: string | string[]; bucket?: string; count?: number; prefix?: string }): SearchResult[]
  /** The app's embedding model and how much is embedded. */
  status(): { model: string; chunks: number; embedded: number; note?: string }
  /** The best matches for a query across buckets (all of them when none are named), best first. viewer (an auth record,
   * or null for a guest) leaves out records they can't view; left out, the search sees everything. */
  searchAsync(o: { query: string; buckets?: string | string[]; bucket?: string; limit?: number; prefix?: string; viewer?: core.Record | null; signal?: AbortSignal }):
    Promise<{ results: SearchResult[]; model?: string; usage: { costMicroUSD: number; inputTokens?: number } }>
  }
  /**
   * The ledger: what calls used and cost. Local reads (not provider calls), so plain. On $llm.for(e), remaining and
   * spent answer about that request's user or guest; on plain $llm, name a subject (or none: the whole app).
   */
  ledger: {
    /** For each limit in Settings → LLM that counts the subject's calls, how much is used and left: the tightest first. */
    remaining(opts?: { subject?: string }): LlmLimitLeft[]
    /** What the subject's calls used: "day" or "month" (UTC), or { since, until }; optionally one kind or model. */
    spent(window?: "day" | "month" | LlmLedgerWindow & { subject?: string; kind?: string; model?: string }): LlmTotals
    /** Totals grouped by model (the default), kind, subject or day, the costliest (or, by day, latest) first. */
    summary(opts?: LlmLedgerWindow & { by?: "model" | "kind" | "subject" | "day"; subject?: string; kind?: string; model?: string; limit?: number }): (LlmTotals & { key: string })[]
    /** The ledger's rows, newest first. */
    calls(opts?: LlmLedgerWindow & { subject?: string; kind?: string; model?: string; status?: "ok" | "refused"; limit?: number; offset?: number }): { calls: LlmLedgerCall[]; more: boolean }
  }
}

type LlmLedgerWindow = { window?: "day" | "month"; since?: string; until?: string }

type LlmTotals = { calls: number; refused: number; input: number; cached: number; output: number; seconds: number; costMicroUSD: number }

type LlmLimitLeft = {
  /** The limit in words: "each guest, $0.10 a day". */
  limit: string
  scope: "app" | "user" | "guest" | "guests"
  window: "day" | "month"
  metric: "cost" | "input" | "output" | "calls"
  kind?: string
  model?: string
  /** In the metric's unit: US dollars for cost, else tokens or calls. */
  amount: number
  used: number
  left: number
  /** When the window ends (RFC 3339, UTC). */
  resets: string
}

type LlmLedgerCall = {
  id: string; created: string; kind: string; model: string; subject: string; via: string
  input: number; cached: number; output: number; seconds: number; costMicroUSD: number; estimated: boolean
  /** Why a limit refused the call. */
  refused?: string
  job?: string
}

/** A billable call on its way to a provider, as $llm.onCall handlers see it. */
type LlmCallEvent = {
  kind: "text" | "image" | "speech" | "transcribe" | "align" | "music" | "video" | "embed"
  /** The operation: "complete", "stream", "generate", "speak", "transcribe", "align", "create" or "embed". */
  op: string
  /** "provider/model"; change it to send the call to another model. */
  model: string
  /** Whom the call is for: "user:<collection>/<id>", "guest:<id>", "guest:ip:<address>", "" or your own ("team:acme"). Change it to count the call elsewhere. */
  subject: string
  /** The signed-in user the call is for, or null. */
  user: { collection: string; id: string } | null
  /** The call is for a guest. */
  guest: boolean
  /** The operation's input (prompt, maxTokens, ...); change it as you like. */
  input: Record<string, any>
  /** Set it when this handler caps what guests may spend, so guest calls don't need a limit on all guests together. */
  guestsCapped: boolean
  /** Lets the call go on. A handler that neither calls it nor throws refuses the call. */
  next(): void
  /** An error refusing the call with your message (code "limit", 429): throw e.refuse("..."). */
  refuse(message: string): Error
}

/** A call that reached a provider, as $llm.onCallDone handlers see it. */
type LlmCallDoneEvent = {
  kind: string
  op: string
  model: string
  subject: string
  user: { collection: string; id: string } | null
  guest: boolean
  /** What it used; usage.costMicroUSD is what it cost, in millionths of a dollar. */
  usage: LlmUsage
  /** A music or video job's id (its event comes once the job is done and its cost known). */
  job?: string
  next(): void
}

/**
 * Cloud models, as $llm. $llm.for(e) is the same API with every call counted for whom the request is for (its user,
 * else its guest), so the limits in Settings → LLM can cap each user and each guest: pass a route's e or a page's ctx.
 * A call a limit refuses throws with code "limit" (429), naming the limit and when it resets.
 */
declare var $llm: LlmAPI & {
  /** The same API with every call counted for the request's signed-in user, else its guest: pass a page's ctx or a route's e. */
  for(e: core.RequestEvent | { req: { event: core.RequestEvent } }): LlmAPI
  /**
   * Runs before every call that reaches a provider, every kind: change e.model, e.subject or e.input, then e.next();
   * or throw e.refuse("...") to refuse it. Runs before the limits in Settings → LLM, which count what it settled on.
   */
  onCall(handler: (e: LlmCallEvent) => void | Promise<void>): void
  /** Runs after every call that reached a provider, with what it used and cost. It can't refuse; the call happened. */
  onCallDone(handler: (e: LlmCallDoneEvent) => void | Promise<void>): void
}

type SearchBucket = { name: string; title: string; description: string; owner: string;
  maxDocuments: number; maxBytes: number; sources: number; documents: number; chunks: number; bytes: number; pending: number;
  errors: number; created: string; updated: string }

type SearchSource = { bucket: string; name: string; kind: "file" | "text" | "folder" | "dir" | "zip" | "page" | "git" | "collection";
  url?: string; every?: "hour" | "day" | "week"; commits?: number; exclude?: string[]; path?: string; include?: string[]; pageURL?: string;
  collection?: string; fields?: string[]; filter?: string; hash?: string;
  status: "reading" | "ready" | "error"; error?: string; documents: number; chunks: number; bytes: number; pending: number;
  costMicroUSD: number; refreshed?: string; next?: string; created: string }

type SearchRecordRef = { collection: string; id: string }

type SearchDocument = { bucket: string; source: string; name: string; kind: "markdown" | "code" | "text"; size: number; hash: string;
  status: "queued" | "embedding" | "ready" | "error"; error?: string; chunks: number; record?: SearchRecordRef; tokens: number;
  costMicroUSD: number; created: string; updated: string }

type SearchResult = { bucket: string; source: string; document: string; text: string; heading?: string; startLine: number;
  endLine: number; record?: SearchRecordRef;
  /** where the passage is published: a web page's URL, or a server folder's url pattern with the section's anchor */
  url?: string; score: number }

type LlmChatOptions = {
  /** "provider/model": every send needs one, here or on the send. */
  model?: string
  /** passages from search buckets, added after each question (and saved with it) */
  enrich?: LlmEnrich
  reasoning?: "" | "off" | "low" | "medium" | "high"
  system?: string
  /** Sent after the question (history shows the question alone): text, or a function of the question. */
  context?: string | ((question: string) => string)
  /** Messages sent before the summary takes over (default 12). */
  window?: number
  /** Fold the oldest messages into the summary once this many are past the window (default 6). */
  summarizeEvery?: number
  maxTokens?: number
  temperature?: number
}

type LlmChatInfo = { id: string; title: string; summary: string; meta: any; messages: number; created: string; updated: string }

type LlmChat = {
  id: string
  /** Asks and saves both turns. With onText, streams the answer to it as it's written. */
  sendAsync(text: string, opts?: LlmChatOptions & { onText?: (text: string) => boolean | void; onReasoning?: (text: string) => boolean | void; schema?: object | string; signal?: AbortSignal }): Promise<LlmTextAnswer>
  get(): LlmChatInfo
  history(opts?: { limit?: number; before?: string }): { id: string; role: "user" | "assistant"; content: string; created: string }[]
  summary(): string
  title(text?: string): string
  meta(obj?: Record<string, any>): any
  forget(): void
}
`
