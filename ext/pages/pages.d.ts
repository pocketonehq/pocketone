// ---- ext/pages: Pages (pb_pages) ----
//
// Types for page code: middleware, route code (.ts), +config, JS plugins and JSX/TSX components. Nothing here is a
// global at runtime; use the types in your own files:
//
//   // pb_pages/products/[id].ts (beside [id].ets)
//   export default {
//     load(ctx) { ctx.data.product = ctx.app.findRecordById("products", ctx.params.id) },
//   } satisfies pages.Handlers
//
// To type ctx.data, or what a JS plugin adds to ctx, add to the interfaces in a .d.ts file of your own:
//
//   declare namespace pages {
//     interface Data { product?: core.Record }
//     interface Context { hello(): string }
//   }

/** Types for page code: `ctx` and its parts, handlers, middleware, +config and components. None of them is a global at runtime. */
declare namespace pages {
  /** What `ctx.data` holds: shared by middleware, handlers and templates. Add your own keys by redeclaring it. */
  interface Data {
    [key: string]: any;
  }

  /** Route parameters (`[id]` in a path), strings. Never includes query parameters. */
  interface Params {
    [name: string]: string;
  }

  /** Options for `ctx.res.cookie` and `ctx.res.clearCookie`. */
  interface CookieOptions {
    path?: string;
    domain?: string;
    /** Seconds. */
    maxAge?: number;
    /** A Date, milliseconds since the epoch, or a date string. */
    expires?: Date | number | string;
    /** Default true. */
    httpOnly?: boolean;
    /** Default: true on HTTPS. */
    secure?: boolean;
    /** Default "Lax". */
    sameSite?: "Lax" | "Strict" | "None" | "lax" | "strict" | "none";
  }

  /** `ctx.req`: the request. */
  interface Request {
    /** `"GET"`, `"POST"`, ... */
    readonly method: string;
    readonly url: { href: string; origin: string; pathname: string; search: string };
    /** Every query parameter's first value. */
    query(): { [name: string]: string };
    /** Every value of a query parameter. */
    query(name: string, opts: { all: true }): string[];
    /** A query parameter's first value, or `undefined`. */
    query(name: string, opts?: { all?: false }): string | undefined;
    /** A request header, `''` if absent. */
    header(name: string): string;
    /** A cookie parsed as JSON (`undefined` if absent or not JSON). */
    cookie<T = any>(name: string, opts: { json: true }): T | undefined;
    /** A cookie's value, or `undefined`. */
    cookie(name: string, opts?: { json?: false }): string | undefined;
    cookies(): { [name: string]: string };
    /** The parsed body: a JSON value, or form fields as `{ name: value }` (repeated names as arrays). */
    body<T = any>(): T;
    /** Uploaded files of a multipart field, ready for a record's file field. */
    files(name: string): filesystem.File[];
    /** The client's IP, as PocketBase resolves it. */
    readonly ip: string;
    /** The underlying PocketBase request event, for anything not covered here. */
    readonly event: core.RequestEvent;
  }

  /** What `ctx.res.sse()` returns. */
  interface EventStream {
    /** Sends an event; non-string data is JSON-encoded. */
    send(event: string, data: any, opts?: { id?: string | number; retry?: number }): void;
    comment(text: string): void;
    close(): void;
  }

  /** `ctx.res`: the response. Every method that sends ends the request (later steps are skipped). */
  interface Response {
    /** The status for the rendered page. */
    status(code: number): Response;
    /** Reads a response header back. */
    header(name: string): string;
    /** Sets a response header. */
    header(name: string, value: string | number): Response;
    /** Sets a cookie; a non-string value is JSON-encoded. */
    cookie(name: string, value: any, opts?: CookieOptions): Response;
    /** Expires a cookie (same `path`/`domain` as when it was set). */
    clearCookie(name: string, opts?: Pick<CookieOptions, "path" | "domain">): Response;
    /** Sends a redirect (3xx, default 302). `flash` sets a one-time message the next request reads with `ctx.flash()`. */
    redirect(location: string, opts?: number | { status?: number; flash?: any }): Response;
    json(data: any): Response;
    json(status: number, data: any): Response;
    /** HTML as-is, without layouts. */
    html(html: string): Response;
    html(status: number, html: string): Response;
    text(text: string): Response;
    text(status: number, text: string): Response;
    /** Sends a file from pb_pages (path relative to pb_pages). */
    file(path: string): Response;
    /** Starts a `text/event-stream` response. */
    sse(): EventStream;
    /** `layout(false)` renders this page without layouts (e.g. an htmx fragment). */
    layout(on: boolean): Response;
    /** True once a response has been sent. */
    readonly sent: boolean;
  }

  interface Route {
    /** The URL pattern without groups: `/products/[id]`. */
    path: string;
    /** The file name without its extension: `[id]`, `index`. */
    name: string;
    /** The file relative to pb_pages, groups included. */
    file: string;
  }

  interface Log {
    /** Shows only in dev mode (or with `debug: true`). */
    debug(...args: any[]): void;
    info(...args: any[]): void;
    warn(...args: any[]): void;
    error(...args: any[]): void;
  }

  /** `ctx.resolve` modes. */
  type ResolveMode = "require" | "raw" | "script" | "style";

  /** What every sign-in method returns. */
  interface SignedIn {
    token: string;
    record: core.Record;
  }

  /** `ctx.session`: cookie sessions on PocketBase auth. Present when the `auth` plugin is on. */
  interface Session {
    authMethods(opts?: { collection?: string }): {
      password: { enabled: boolean; identityFields: string[] };
      otp: { enabled: boolean };
      mfa: { enabled: boolean };
      oauth2: { enabled: boolean; providers: { name: string; displayName: string }[] };
    };
    /** Creates the record; sends the verification email when `verify`; signs in when `signIn` (else `token` is `''`). */
    register(
      email: string,
      password: string,
      opts?: { collection?: string; data?: { [field: string]: any }; verify?: boolean; signIn?: boolean },
    ): SignedIn;
    signInWithPassword(identity: string, password: string, opts?: { collection?: string }): SignedIn;
    /** Always returns an id (unknown emails get one that never validates). */
    requestOTP(email: string, opts?: { collection?: string; create?: boolean }): string;
    signInWithOTP(otpId: string, code: string, opts?: { collection?: string }): SignedIn;
    signInAnonymously(opts?: { collection?: string }): SignedIn;
    /** Returns the provider's URL; with `redirect` (default) it also sends the redirect. */
    requestOAuth2(provider: string, opts?: { collection?: string; redirectPath?: string; redirect?: boolean }): string;
    signInWithOAuth2(
      state: string,
      code: string,
      opts?: { createData?: { [field: string]: any } },
    ): SignedIn & { meta: { isNew: boolean; [key: string]: any } };
    /** Sets the session cookie for a record the app authenticated itself. */
    signIn(record: core.Record): SignedIn;
    signOut(): void;
    requestVerification(email: string, opts?: { collection?: string }): void;
    confirmVerification(token: string): void;
    requestPasswordReset(email: string, opts?: { collection?: string }): void;
    confirmPasswordReset(token: string, password: string, passwordConfirm: string): void;
    /** Needs `ctx.auth`. */
    requestEmailChange(newEmail: string): void;
    confirmEmailChange(token: string, password: string): void;
  }

  /** `ctx.realtime`: PocketBase realtime from page code. Present when the `realtime` plugin is on. */
  interface Realtime {
    /** Sends to every client subscribed to `topic` (`filter` only narrows that set). `data` is always JSON-encoded. */
    send(topic: string, data: any, opts?: { filter?: (client: subscriptions.Client) => boolean }): void;
    /** One client by id, or `null`. */
    client(id: string): subscriptions.Client | null;
  }

  /** The request context, passed to middleware, handlers, templates, components and plugins. */
  interface Context {
    req: Request;
    res: Response;
    data: Data;
    params: Params;
    route: Route;
    /** The authenticated record, or `null`. */
    auth: core.Record | null;
    /** The app (`$app`). */
    app: PocketBase;
    /** True in dev mode. */
    dev: boolean;
    /** The absolute path of this app's pb_pages folder. */
    readonly pagesDir: string;
    /** An environment variable, `''` if unset. */
    env(name: string): string;
    log: Log;
    /** Reads a page metadata value (title, description). */
    meta(key: string): any;
    /** Sets a page metadata value; returns it. */
    meta<T>(key: string, value: T): T;
    /** URL of a static file in pb_pages with its content hash: `/assets/app.3f9a1c2b7d4e.css`. */
    asset(path: string): string;
    /** Renders a `_private/` partial and returns its text (a promise when the partial is async). */
    include(name: string, locals?: { [key: string]: any }): any;
    /** Loads a module's exports from `_private/`. */
    resolve<T = any>(name: string, opts?: { mode?: "require" }): T;
    /** Reads a `_private/` file as text, or wrapped in `<script>`/`<style>`. */
    resolve(name: string, opts: { mode: "raw" | "script" | "style" }): string;
    /** Reads the app's in-memory store. */
    store<T = any>(name: string): T;
    /** Sets a value in the app's in-memory store; returns it. */
    store<T>(name: string, value: T): T;
    /** The one-time message set by `ctx.res.redirect(..., { flash })`, or `undefined`. */
    flash<T = any>(): T | undefined;
    /** A PocketBase JS SDK client acting as the signed-in user (or a guest), in-process. */
    pb(): any;
    /** An error to throw: sends `status` with `message` (JSON from an endpoint, unless the request asks for HTML). */
    httpError(status: number, message?: string): Error;
    /** Set while `+error` renders. */
    error?: { status: number; message: string };
    /** Needs the `auth` plugin in +config. */
    session: Session;
    /** Needs the `realtime` plugin in +config. */
    realtime: Realtime;
  }

  /** What a handler may return: a plain object is shallow-assigned into `ctx.data`. */
  type HandlerResult = Partial<Data> | Response | void | undefined;

  type Handler = (ctx: Context) => HandlerResult | Promise<HandlerResult>;

  /** The exports of a route's `.ts` file: a page's code, or an endpoint (all optional). */
  interface Handlers {
    /** Runs for every method, before the method's handler. */
    load?: Handler;
    get?: Handler;
    post?: Handler;
    put?: Handler;
    patch?: Handler;
    delete?: Handler;
  }

  /** A `+middleware.js` export. Call `next()` to continue and return its result. */
  type Middleware = (ctx: Context, next: () => any) => any;

  /** What a JS plugin factory returns. Every hook is optional. */
  interface Plugin {
    /** Required and unique. */
    name: string;
    /** Adds to `ctx`, before any `onRequest`. */
    extendContext?(ctx: Context): void;
    /** Before middleware. */
    onRequest?(ctx: Context): void | Promise<void>;
    /** After each page or layout renders (not partials); returns the new HTML. */
    transform?(e: { html: string; ctx: Context; file: string }): string;
  }

  /** A JS plugin: a factory that gets its entry's options from +config. */
  type PluginFactory = (config: { [key: string]: any }) => Plugin;

  /** The export of `+config.js`. */
  interface Config {
    /** Module names, built-in names (`auth`, `realtime`), factories, or `{ factory, ...options }`. */
    plugins?: (string | PluginFactory | ({ factory: PluginFactory } & { [option: string]: any }))[];
    /** Detailed error pages and `ctx.log.debug` output outside dev mode. */
    debug?: boolean;
  }

  /**
   * The props pages passes to a JSX/TSX page, layout or partial it renders (an `include`'s locals typed by `L`). A
   * component you import and use as `<Card />` gets only the props you give it, so type those yourself.
   */
  type Props<L = {}> = {
    ctx: Context;
    data: Data;
    params: Params;
    meta: Context["meta"];
    asset: Context["asset"];
    include: Context["include"];
    /** `slot(name)` reads a slot, `slot(name, content)` sets one, `slot()` is `children`. */
    slot: { (name?: string): any; (name: string, content: any): void };
    /** In a layout, the inner content. */
    children?: any;
  } & L;
}

/**
 * JSX in .jsx/.tsx pages renders straight to HTML on the server: any tag, any attribute (`class`, `hx-get`,
 * `data-on:click`). Type-check with `"jsx": "preserve"` in tsconfig.json.
 */
declare namespace JSX {
  type Element = any;
  interface ElementChildrenAttribute {
    children: {};
  }
  interface IntrinsicElements {
    [tag: string]: { [attr: string]: any };
  }
}
