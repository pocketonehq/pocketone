// -------------------------------------------------------------------
// PocketOne (ext/jsvm): what this engine adds to PocketBase's JS API.
// Hand-written; internal/typegen appends it to the generated declarations.
// -------------------------------------------------------------------

/**
 * The arguments JS passes to Go function `F`: all of them, or all but the first when it is a `context.Context`
 * (the engine supplies that one).
 *
 * @group PocketOne
 */
type GoArgs<F> = F extends (ctx: infer C, ...rest: infer R) => any
  ? 0 extends 1 & C
    ? Parameters<F> // a first parameter typed any isn't a context
    : [C] extends [context.Context]
      ? [context.Context] extends [C]
        ? R | Parameters<F>
        : Parameters<F>
      : Parameters<F>
  : Parameters<F>;

/** Options for `fn.asyncWith`. @group PocketOne */
interface AsyncOptions {
  /** Aborts the call: the promise rejects with `signal.reason` and the Go function's context is cancelled. */
  signal?: AbortSignal | null;
}

/**
 * Every Go function and method has an `.async` twin: it runs off the app's JS loop and returns a promise, so the app
 * serves other requests while it waits.
 *
 * ```js
 * const post = await $app.findRecordById.async("posts", id)
 * await sleep.async(100)
 * ```
 *
 * A Go function whose first parameter is a `context.Context` gets one from the engine (pass the other arguments):
 * it is cancelled when the request or hook the call runs for is given up (its client left, `AwaitLimit` passed),
 * and when the call's `AbortSignal` aborts (`asyncWith`).
 *
 * Only Go functions have it at runtime; on a JS function it's undefined.
 *
 * @group PocketOne
 */
interface Function {
  async<F extends (...args: any[]) => any>(this: F, ...args: GoArgs<F>): Promise<Awaited<ReturnType<F>>>;
  /**
   * `.async` that an `AbortSignal` can stop: when it aborts, the promise rejects with `signal.reason` (a
   * `DOMException` named `"TimeoutError"` for `AbortSignal.timeout`, `"AbortError"` for `controller.abort()`) and
   * the Go function's context is cancelled. A signal that has already aborted rejects without calling Go. A Go
   * function that takes no context runs on to its end; only its promise is let go.
   *
   * ```js
   * const text = await fetchPage.asyncWith({ signal: AbortSignal.timeout(30_000) }, url)
   * ```
   */
  asyncWith<F extends (...args: any[]) => any>(
    this: F,
    options: AsyncOptions | undefined | null,
    ...args: GoArgs<F>
  ): Promise<Awaited<ReturnType<F>>>;
}

/**
 * An error with a `name`, as in browsers: AbortSignal's reasons are `DOMException`s named `"AbortError"` (code 20)
 * and `"TimeoutError"` (code 23).
 *
 * @group PocketOne
 */
interface DOMException extends Error {
  readonly name: string;
  readonly message: string;
  readonly code: number;
}
/** @group PocketOne */
declare var DOMException: {
  prototype: DOMException;
  new (message?: string, name?: string): DOMException;
};

/** The event an AbortSignal's listeners get. @group PocketOne */
interface AbortEvent {
  readonly type: "abort";
  readonly target: AbortSignal;
  readonly currentTarget: AbortSignal;
}

/**
 * Says when to stop, as in browsers: pass one to `fn.asyncWith({ signal }, ...)` (or an API that takes `{ signal }`).
 * `AbortSignal.timeout(ms)` runs on a Go timer that keeps going after the request is answered, so background work
 * can bound its calls where `setTimeout` is refused. It counts against `MaxTimers` until it fires.
 *
 * ```js
 * const signal = AbortSignal.any([AbortSignal.timeout(60_000), controller.signal])
 * ```
 *
 * @group PocketOne
 */
interface AbortSignal {
  readonly aborted: boolean;
  readonly reason: any;
  onabort: ((this: AbortSignal, ev: AbortEvent) => any) | null;
  /** Throws `reason` if the signal has aborted. */
  throwIfAborted(): void;
  addEventListener(type: "abort", listener: (this: AbortSignal, ev: AbortEvent) => any, options?: boolean | { once?: boolean }): void;
  removeEventListener(type: "abort", listener: (this: AbortSignal, ev: AbortEvent) => any): void;
}
/** @group PocketOne */
declare var AbortSignal: {
  prototype: AbortSignal;
  /** A signal that has already aborted with `reason` (an `"AbortError"` DOMException by default). */
  abort(reason?: any): AbortSignal;
  /** A signal that aborts with a `"TimeoutError"` DOMException after `ms` milliseconds. */
  timeout(ms: number): AbortSignal;
  /** A signal that aborts when any of `signals` does, with its reason. */
  any(signals: Iterable<AbortSignal>): AbortSignal;
};

/**
 * Makes a signal and aborts it, as in browsers.
 *
 * ```js
 * const c = new AbortController()
 * cancelButtonPressed.then(() => c.abort())
 * await work.asyncWith({ signal: c.signal })
 * ```
 *
 * @group PocketOne
 */
interface AbortController {
  readonly signal: AbortSignal;
  /** Aborts the signal with `reason` (an `"AbortError"` DOMException by default); later calls do nothing. */
  abort(reason?: any): void;
}
/** @group PocketOne */
declare var AbortController: {
  prototype: AbortController;
  new (): AbortController;
};

/**
 * Runs `callback(...args)` once after `ms` milliseconds (at least 1), as its own task. A timer set while a request is
 * served is cleared once the request is answered or its client goes away; one set at the top of a file runs until
 * cleared. Returns the id for `clearTimeout`.
 *
 * ```js
 * const word = await new Promise((resolve) => setTimeout(resolve, 500, "later"))
 * ```
 *
 * @group PocketOne
 */
declare function setTimeout<A extends any[]>(callback: (...args: A) => void, ms?: number, ...args: A): number;

/**
 * Runs `callback(...args)` every `ms` milliseconds (at least 1); the next run is timed from the end of the last one.
 * Cleared like `setTimeout`. Returns the id for `clearInterval`.
 *
 * @group PocketOne
 */
declare function setInterval<A extends any[]>(callback: (...args: A) => void, ms?: number, ...args: A): number;

/** Cancels a `setTimeout` (an unknown id does nothing). @group PocketOne */
declare function clearTimeout(id: number | undefined | null): void;

/** Cancels a `setInterval` (an unknown id does nothing). @group PocketOne */
declare function clearInterval(id: number | undefined | null): void;

/** Runs `callback` after the current task, before any timer. @group PocketOne */
declare function queueMicrotask(callback: () => void): void;

/** @group PocketOne */
interface ImportMeta {
  /** The module's file URL, `file:///.../pb_hooks/main.pb.mts`. */
  url: string;
  /** The module's file path. */
  filename: string;
  /** The module's folder. */
  dirname: string;
}

/** @group PocketOne */
interface Console {
  log(...data: any[]): void;
  error(...data: any[]): void;
  warn(...data: any[]): void;
  info(...data: any[]): void;
  debug(...data: any[]): void;
  trace(...data: any[]): void;
}
/** Writes to the app's log. @group PocketOne */
declare var console: Console;

/**
 * Loads a CommonJS module (`.js`, `.cjs`, `.cts`, `.json`, a `node_modules` package) or an ES module without top-level
 * await (`.mjs`, `.ts`, `.mts`), relative to the calling file. A module loads once per app.
 *
 * @group PocketOne
 */
declare function require(id: string): any;

/** Inside a CommonJS module: the module. @group PocketOne */
declare var module: { exports: any; id: string; filename: string; loaded: boolean };
/** Inside a CommonJS module: `module.exports`. @group PocketOne */
declare var exports: any;
/** Inside a CommonJS module: its file path. @group PocketOne */
declare var __filename: string;
/** Inside a CommonJS module: its folder. @group PocketOne */
declare var __dirname: string;

/** @group PocketOne */
declare var process: {
  env: { [key: string]: string | undefined };
  argv: string[];
  platform: string;
};

/**
 * A small Node-style Buffer: bytes with utf8, base64, base64url, hex and latin1 conversions.
 *
 * @group PocketOne
 */
declare class Buffer extends Uint8Array {
  static from(value: string, encoding?: BufferEncoding): Buffer;
  static from(value: ArrayBuffer | ArrayLike<number> | Iterable<number>): Buffer;
  static alloc(size: number, fill?: number | string): Buffer;
  static isBuffer(value: any): value is Buffer;
  static byteLength(value: string, encoding?: BufferEncoding): number;
  static concat(list: ArrayLike<Uint8Array>): Buffer;
  toString(encoding?: BufferEncoding): string;
  toJSON(): { type: "Buffer"; data: number[] };
}
/** @group PocketOne */
type BufferEncoding = "utf8" | "utf-8" | "base64" | "base64url" | "hex" | "latin1" | "binary";
