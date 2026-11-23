// Package searchv1 is the llm.search/v1 contract: named buckets of content an app searches. An app makes the bucket it
// needs (Ensure); its admins fill it from the dashboard's Search screen. A bucket holds sources, each one tracked unit
// however many documents it brings: a file, pasted text, a folder, a zip, a web page, a Git repository or one of the
// app's collections, or a folder on the server. Adding a source of the same name again replaces it: unchanged documents cost nothing, missing
// ones go. URL, repository and collection sources keep themselves current. The provider splits each document into
// chunks with their source (document, section, lines, record), embeds them with the app's one embedding model
// (llm.embed/v1, picked automatically; identical chunks anywhere in the app are embedded once), and returns the chunks
// that best match a query across the buckets asked for.
//
// A wire contract at /api/_c/llm.search/v1. In page code it's $llm.search (bound by the llm extension); llm.text/v1 and llm.chat/v1 calls take `enrich` to
// add a search's best passages to their prompt ([Enrich]).
package searchv1

//go:generate go run pocketone.dev/ext/contracts/cmd/contractgen

import (
	"context"
	"time"

	"pocketone.dev/ext/contracts/llm"
	"pocketone.dev/ext/extkit"
)

// Search keeps an app's search buckets and searches them.
//
//contract:id llm.search/v1
//contract:kind wire
//contract:scope search
//contract:js hooks
type Search interface {
	// Status is the app's embedding model and how far embedding has got across every bucket.
	//
	//op: GET status scope=search:read
	Status(ctx context.Context, a extkit.Auth) (Status, error)
	// SetModel changes the app's embedding model ("provider/model" from llm.embed/v1's models; "" picks one again).
	// Every bucket is embedded again in the background; until a chunk is, keywords still find it.
	//
	//op: POST set-model
	SetModel(ctx context.Context, a extkit.Auth, in ModelInput) (Status, error)
	// Buckets lists the app's buckets with their counts.
	//
	//op: GET buckets scope=search:read
	Buckets(ctx context.Context, a extkit.Auth) (BucketList, error)
	// Bucket returns one bucket with its counts.
	//
	//op: GET bucket scope=search:read
	Bucket(ctx context.Context, a extkit.Auth, in Ref) (Bucket, error)
	// Ensure makes a bucket if it's missing and returns it. An existing bucket keeps its settings, except that an
	// empty title, description or owner is filled in. Apps call it at startup for the bucket they need.
	//
	//op: POST ensure
	Ensure(ctx context.Context, a extkit.Auth, in EnsureInput) (Bucket, error)
	// Update changes a bucket's title, description or limits (fields left out stay).
	//
	//op: POST update
	Update(ctx context.Context, a extkit.Auth, in UpdateInput) (Bucket, error)
	// Delete removes a bucket and everything in it.
	//
	//op: POST delete
	Delete(ctx context.Context, a extkit.Auth, in Ref) (Removed, error)
	// Sources lists a bucket's sources, by name.
	//
	//op: GET sources scope=search:read
	Sources(ctx context.Context, a extkit.Auth, in BucketRef) (SourceList, error)
	// Add puts a file or pasted text in a bucket as its own source (one document), replacing the source of that name.
	// The same text again costs nothing; changed text embeds only its new chunks. It returns at once; the document is
	// embedded in the background (its status says how far it got).
	//
	//op: POST add
	Add(ctx context.Context, a extkit.Auth, in AddInput) (Source, error)
	// Upload adds a file sent as the request body: ?bucket=<name>&name=<file name>[&replace=<source>]. A .zip is one
	// source with a document per text file inside it; anything else is a file source.
	//
	//op: POST upload
	Upload(ctx context.Context, a extkit.Auth, in extkit.RawIn) (Source, error)
	// Put adds one file to a folder source (made on the first one), the way the dashboard sends a dropped folder;
	// Prune ends it.
	//
	//op: POST put
	Put(ctx context.Context, a extkit.Auth, in PutInput) (Document, error)
	// Prune removes the documents of a folder source that aren't in Keep: the files the folder no longer has.
	//
	//op: POST prune
	Prune(ctx context.Context, a extkit.Auth, in PruneInput) (Source, error)
	// AddURL adds a web page or a Git repository as a source (named by its URL, replacing one of that name) and reads
	// it in the background; Every keeps it current.
	//
	//op: POST add-url
	AddURL(ctx context.Context, a extkit.Auth, in URLInput) (Source, error)
	// AddFolder adds a folder of the app's own files on the server (named by its path, replacing one of that name):
	// every text file in it, read in the background and read again every few minutes, so changed files are embedded
	// again and removed ones leave. With URL, each result from it links to the page that file is published at.
	//
	//op: POST add-folder
	AddFolder(ctx context.Context, a extkit.Auth, in FolderInput) (Source, error)
	// DetectURL says how AddURL would read a URL: as a Git repository when its server answers the Git protocol (any
	// host, with or without .git), otherwise as a web page.
	//
	//op: POST detect-url scope=search:read
	DetectURL(ctx context.Context, a extkit.Auth, in URLRef) (URLKind, error)
	// Follow adds one of the app's collections as a source (named by the collection): each record the filter keeps is
	// a document, kept in sync as records are saved and deleted.
	//
	//op: POST follow
	Follow(ctx context.Context, a extkit.Auth, in FollowInput) (Source, error)
	// Refresh reads a source again now: fetches a URL or repository, rereads a collection, resplits a file or zip.
	//
	//op: POST refresh
	Refresh(ctx context.Context, a extkit.Auth, in SourceRef) (Source, error)
	// Remove takes sources out of a bucket, with their documents.
	//
	//op: POST remove
	Remove(ctx context.Context, a extkit.Auth, in RemoveInput) (Removed, error)
	// Documents lists a bucket's documents (or one source's), by source and name.
	//
	//op: GET documents scope=search:read
	Documents(ctx context.Context, a extkit.Auth, in DocumentsInput) (DocumentList, error)
	// Search returns the chunks that best match a query across the buckets named, best first.
	//
	//op: POST search scope=search:read
	Search(ctx context.Context, a extkit.Auth, in SearchInput) (Results, error)
	// Sample returns chunks picked at random from the buckets named: material to draw from ("surprise me", a topic
	// nobody asked about), not matches. No query, no embedding, so it costs nothing. Like Search without a viewer, it
	// may return any chunk, a followed record's included.
	//
	//op: POST sample scope=search:read
	Sample(ctx context.Context, a extkit.Auth, in SampleInput) (Results, error)
}

// Status is the app's embedding model and its progress.
type Status struct {
	// Model is the embedding model every bucket uses ("provider/model"); "" until one is picked (none available yet).
	Model string `json:"model"`
	// Chunks is how many chunks the app's buckets hold; Embedded how many have the model's vectors.
	Chunks   int `json:"chunks"`
	Embedded int `json:"embedded"`
	// Note says what's missing when nothing can be embedded (no embedding model among the app's providers).
	Note string `json:"note,omitempty"`
}

// ModelInput picks the app's embedding model.
type ModelInput struct {
	Model string `json:"model"`
}

// Ref names a bucket.
type Ref struct {
	Name string `json:"name"`
}

// BucketRef names a bucket, for operations on its sources.
type BucketRef struct {
	Bucket string `json:"bucket"`
}

// Bucket is a named set of sources searched together.
type Bucket struct {
	// Name is the bucket's id: letters, digits, - and _, starting with a letter or digit (at most 64).
	Name  string `json:"name"`
	Title string `json:"title"`
	// Description says what belongs in the bucket: the app that made it tells its admins what to load.
	Description  string    `json:"description"`
	Owner        string    `json:"owner"` // who made it (the app's name, say), for the dashboard
	MaxDocuments int       `json:"maxDocuments"`
	MaxBytes     int64     `json:"maxBytes"`
	Sources      int       `json:"sources"`
	Documents    int       `json:"documents"`
	Chunks       int       `json:"chunks"`
	Bytes        int64     `json:"bytes"`
	Pending      int       `json:"pending"` // documents waiting to be embedded
	Errors       int       `json:"errors"`  // documents or sources that failed
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
}

// BucketList is every bucket, by name.
type BucketList struct {
	Buckets []Bucket `json:"buckets"`
}

// EnsureInput is the bucket an app needs.
type EnsureInput struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

// UpdateInput changes a bucket.
type UpdateInput struct {
	Name         string  `json:"name"`
	Title        *string `json:"title,omitempty"`
	Description  *string `json:"description,omitempty"`
	MaxDocuments *int    `json:"maxDocuments,omitempty"`
	MaxBytes     *int64  `json:"maxBytes,omitempty"`
}

// Source kinds.
const (
	KindFile       = "file"       // one dropped or uploaded file
	KindText       = "text"       // pasted text
	KindFolder     = "folder"     // a dropped folder, file by file
	KindDir        = "dir"        // a folder of the app's files on the server, read again every few minutes
	KindZip        = "zip"        // a zip, a document per text file inside
	KindPage       = "page"       // a web page, fetched
	KindGit        = "git"        // a Git repository: the default branch's files and recent commits
	KindCollection = "collection" // one of the app's collections, a document per record
)

// Refresh intervals for URL and repository sources.
const (
	EveryHour = "hour"
	EveryDay  = "day"
	EveryWeek = "week"
)

// Source is one tracked unit in a bucket.
type Source struct {
	Bucket string `json:"bucket"`
	// Name identifies it in the bucket: the file, folder or zip name, the URL, or the collection.
	Name string `json:"name"`
	Kind string `json:"kind"`
	// URL, Every, Commits, Exclude: a page or repository source's settings.
	URL     string   `json:"url,omitempty"`
	Every   string   `json:"every,omitempty"`
	Commits int      `json:"commits,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// Path, Include, PageURL: a server folder's settings (Exclude too).
	Path    string   `json:"path,omitempty"`
	Include []string `json:"include,omitempty"`
	PageURL string   `json:"pageURL,omitempty"`
	// Collection, Fields, Filter: a collection source's settings.
	Collection string   `json:"collection,omitempty"`
	Fields     []string `json:"fields,omitempty"`
	Filter     string   `json:"filter,omitempty"`
	// Hash is the sha256 of a file's or zip's content as uploaded, so the dashboard can tell an exact duplicate
	// under another name.
	Hash string `json:"hash,omitempty"`
	// Status is reading (fetching or splitting), ready or error; Error says why.
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	Documents    int        `json:"documents"`
	Chunks       int        `json:"chunks"`
	Embedded     int        `json:"embedded"` // chunks with the app's model's vectors
	Bytes        int64      `json:"bytes"`
	Pending      int        `json:"pending"` // documents waiting to be embedded
	CostMicroUSD int64      `json:"costMicroUSD"`
	Refreshed    *time.Time `json:"refreshed,omitempty"` // when it was last read
	Next         *time.Time `json:"next,omitempty"`      // when Every reads it next
	Created      time.Time  `json:"created"`
}

// SourceList is a bucket's sources.
type SourceList struct {
	Sources []Source `json:"sources"`
}

// SourceRef names a source.
type SourceRef struct {
	Bucket string `json:"bucket"`
	Source string `json:"source"`
}

// AddInput is a file or pasted text.
type AddInput struct {
	Bucket string `json:"bucket"`
	// Name is the source's (and its document's) name, usually a file name ("install.md"). Its extension picks the
	// splitter: Markdown by headings, code and the rest by blank lines.
	Name string `json:"name"`
	Text string `json:"text"`
	// Kind is file (the default) or text, for the dashboard.
	Kind string `json:"kind,omitempty"`
	// Replace names an existing source this one replaces under the new name (a renamed new version: notes-v2.zip
	// for notes-v1.zip). Without it, only a source of the same name is replaced.
	Replace string `json:"replace,omitempty"`
}

// PutInput is one file of a dropped folder.
type PutInput struct {
	Bucket string `json:"bucket"`
	Source string `json:"source"` // the folder's name
	Path   string `json:"path"`   // the file's path inside the folder
	Text   string `json:"text"`
	// Replace names an existing source the folder replaces under its name (on its first file).
	Replace string `json:"replace,omitempty"`
}

// PruneInput ends a folder upload.
type PruneInput struct {
	Bucket string   `json:"bucket"`
	Source string   `json:"source"`
	Keep   []string `json:"keep"` // every path the folder has now
}

// URLInput is a web page or a Git repository.
type URLInput struct {
	Bucket string `json:"bucket"`
	URL    string `json:"url"` // http or https
	// Kind is page or git; "" asks the server (DetectURL).
	Kind  string `json:"kind,omitempty"`
	Every string `json:"every,omitempty"` // hour, day, week; "" reads it only when added or refreshed
	// Commits is how many recent commits of a repository become documents (subject, body, author, date, files; no
	// diffs): 0 is the default (300), -1 none.
	Commits int `json:"commits,omitempty"`
	// Exclude skips a repository's files whose path, or any folder in it, matches one of these patterns ("docs/old",
	// "*.min.js"), beside what's always skipped (binaries, .git, node_modules, vendor, lockfiles).
	Exclude []string `json:"exclude,omitempty"`
}

// FolderInput is a folder of the app's files on the server.
type FolderInput struct {
	Bucket string `json:"bucket"`
	// Path is the folder, relative to the app's own folder (the one holding pb_data, pb_pages and pb_public); it can't
	// leave that folder or be pb_data. "pb_pages/docs", say.
	Path string `json:"path"`
	// Include keeps only files matching one of these patterns (against the file name or its path in the folder:
	// "*.md"); none keeps every text file. Exclude leaves out matches, beside what's always skipped (dot folders and
	// dotfiles, node_modules, vendor, lockfiles, binaries).
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// URL is where each file is published, with {path} for the file's path in the folder without its extension (an
	// index file is its folder): "/docs/{path}" makes guides/install.md /docs/guides/install and guides/index.md
	// /docs/guides. Results from Markdown files add their section's anchor (#heading-id). "" gives no links.
	URL string `json:"url,omitempty"`
}

// URLRef is a URL.
type URLRef struct {
	URL string `json:"url"`
}

// URLKind is how a URL would be read: git or page.
type URLKind struct {
	Kind string `json:"kind"`
}

// FollowInput is a collection.
type FollowInput struct {
	Bucket     string `json:"bucket"`
	Collection string `json:"collection"`
	// Fields are searched; the first is each record's title.
	Fields []string `json:"fields"`
	Filter string   `json:"filter,omitempty"` // a PocketBase filter, e.g. "published = true"
}

// RemoveInput names sources.
type RemoveInput struct {
	Bucket  string   `json:"bucket"`
	Sources []string `json:"sources"`
}

// Removed says how many things a call affected.
type Removed struct {
	Count int `json:"count"`
}

// Document is one piece of a source: a file, a page, a commit or a record.
type Document struct {
	Bucket string `json:"bucket"`
	Source string `json:"source"`
	Name   string `json:"name"` // its path in the source, the URL, "commit <sha>" or "<collection>/<id>"
	Kind   string `json:"kind"` // markdown, code or text: how it was split
	Size   int64  `json:"size"`
	Hash   string `json:"hash"` // sha256 of the text
	// Status is queued (waiting to be embedded), embedding, ready or error.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Chunks int    `json:"chunks"`
	// Embedded is how many of its chunks have the app's model's vectors (Chunks when it's ready).
	Embedded     int        `json:"embedded"`
	Record       *RecordRef `json:"record,omitempty"` // the record it was made from, in a collection source
	Tokens       int        `json:"tokens"`           // the text's estimated size in tokens
	CostMicroUSD int64      `json:"costMicroUSD"`     // what embedding it has cost so far
	Created      time.Time  `json:"created"`
	Updated      time.Time  `json:"updated"`
}

// RecordRef names a record.
type RecordRef struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
}

// DocumentsInput picks a bucket's documents.
type DocumentsInput struct {
	Bucket string `json:"bucket"`
	Source string `json:"source,omitempty"` // only this source's
	Prefix string `json:"prefix,omitempty"` // only names starting with it
}

// DocumentList is a bucket's documents.
type DocumentList struct {
	Documents []Document `json:"documents"`
}

// Viewer is who a search is for. Matches from records they can't view (the collection's view rule, as PocketBase
// checks it) are left out; documents that aren't records are for everyone. A search without one sees everything.
type Viewer struct {
	// Collection and ID are the signed-in auth record; both empty is a guest (signed out).
	Collection string `json:"collection,omitempty"`
	ID         string `json:"id,omitempty"`
}

// SearchInput is a query.
type SearchInput struct {
	// Buckets are searched together; none searches every bucket. Bucket is one more, for a single-bucket search.
	Buckets []string `json:"buckets,omitempty"`
	Bucket  string   `json:"bucket,omitempty"`
	Query   string   `json:"query"`
	Limit   int      `json:"limit,omitempty"` // default 5, at most 50
	// Prefix keeps matches from documents whose name starts with it.
	Prefix string  `json:"prefix,omitempty"`
	Viewer *Viewer `json:"viewer,omitempty"`
}

// SampleInput is where to draw random chunks from.
type SampleInput struct {
	// Buckets are drawn from together; none draws from every bucket. Bucket is one more.
	Buckets []string `json:"buckets,omitempty"`
	Bucket  string   `json:"bucket,omitempty"`
	Count   int      `json:"count,omitempty"` // default 5, at most 50
	// Prefix keeps chunks from documents whose name starts with it.
	Prefix string `json:"prefix,omitempty"`
}

// Results are the best matches, best first (a Sample's are in no order, with no score).
type Results struct {
	Results []Result  `json:"results"`
	Model   string    `json:"model,omitempty"` // the model that embedded the query
	Usage   llm.Usage `json:"usage"`           // what embedding the query cost
}

// Result is one matching chunk and where it came from.
type Result struct {
	Bucket   string `json:"bucket"`
	Source   string `json:"source"`
	Document string `json:"document"`
	Text     string `json:"text"`
	// Heading is the section the chunk is in ("Install › macOS"), for Markdown, records and commits.
	Heading   string     `json:"heading,omitempty"`
	StartLine int        `json:"startLine"`
	EndLine   int        `json:"endLine"`
	Record    *RecordRef `json:"record,omitempty"`
	// URL links to where the passage is published, when its source knows: a web page's URL, or a server folder's URL
	// pattern with the section's anchor ("/docs/guides/install#macos").
	URL   string  `json:"url,omitempty"`
	Score float64 `json:"score"` // orders the results; higher is better
}

// Errors.
var (
	ErrNoBucket    = &extkit.Error{Code: "not_found", Status: 404, Message: "no bucket with that name"}
	ErrNoSource    = &extkit.Error{Code: "not_found", Status: 404, Message: "no source with that name in the bucket"}
	ErrInvalidName = &extkit.Error{Code: "bad_request", Status: 400, Message: "a bucket name is letters, digits, - and _, at most 64, starting with a letter or digit"}
	ErrTooLarge    = &extkit.Error{Code: "too_large", Status: 413, Message: "over the bucket's limit"}
	ErrNotText     = &extkit.Error{Code: "bad_request", Status: 400, Message: "only text files (Markdown, text, code) or a zip of them can be added"}
	ErrNoModel     = &extkit.Error{Code: "not_configured", Status: 503, Message: "no embedding model: add an AI provider with embedding models in Settings → LLM"}
)
