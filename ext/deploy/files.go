package deploy

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	fsv1 "pocketone.dev/ext/contracts/instance/fs/v1"
	"sort"
	"strconv"
	"strings"
)

// File access over HTTPS, the replacement for SFTP: list, download, upload (chunked) and delete files in an instance
// folder. Reach: instance.fs/v1's (the code set, node_modules, patches/); never pb_data.

const (
	maxFileChunkBytes = 64 << 20
	maxListEntries    = 50_000
	uploadSuffix      = ".ph-upload"
)

// resolveFilePath maps a client path to a filesystem path through instance.fs/v1's rules: never pb_data or .git*,
// never across a symlink or out of the instance folder.
func resolveFilePath(c *reqCtx, raw string) (rel, fsPath string, err error) {
	rel, fsPath, err = fsv1.Resolve(c.dir, raw)
	var pe *fsv1.PathError
	if errors.As(err, &pe) {
		return "", "", errf(400, "%s is %s", pe.Path, pe.Reason)
	}
	return rel, fsPath, err
}

type fileEntry struct {
	Path        string `json:"path"`
	IsDirectory bool   `json:"isDirectory"`
	Size        int64  `json:"size"`
	Mtime       int64  `json:"mtime"`
}

func mtimeMs(fi os.FileInfo) int64 {
	return int64(math.Round(float64(fi.ModTime().UnixNano()) / 1e6))
}

func flag(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "1" || v == "true"
}

func (a *API) listFiles(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, false)
	if err != nil {
		return err
	}
	rel, fsPath, err := resolveFilePath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	recursive := flag(r, "recursive")
	st, err := os.Stat(fsPath)
	if err != nil {
		name := rel
		if name == "" {
			name = "/"
		}
		return errf(404, "%s not found", name)
	}
	out := []fileEntry{}
	if !st.IsDir() {
		out = append(out, fileEntry{rel, false, st.Size(), mtimeMs(st)})
	} else {
		var walk func(dir string) error
		walk = func(dir string) error {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, name := range names {
				if len(out) >= maxListEntries {
					return nil
				}
				child := filepath.Join(dir, name)
				childRel, _ := filepath.Rel(c.dir, child)
				childRel = filepath.ToSlash(childRel)
				if !fsv1.IsReachable(childRel) || strings.HasSuffix(name, uploadSuffix) {
					continue
				}
				fi, err := os.Lstat(child)
				if err != nil || fi.Mode()&fs.ModeSymlink != 0 {
					continue
				}
				size := fi.Size()
				if fi.IsDir() {
					size = 0
				}
				out = append(out, fileEntry{childRel, fi.IsDir(), size, mtimeMs(fi)})
				if fi.IsDir() && recursive {
					if err := walk(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := walk(fsPath); err != nil {
			return err
		}
	}
	writeJSON(w, 200, map[string]any{"path": rel, "entries": out, "truncated": len(out) >= maxListEntries})
	return nil
}

func (a *API) readFile(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, false)
	if err != nil {
		return err
	}
	rel, fsPath, err := resolveFilePath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	f, err := os.Open(fsPath)
	if err != nil {
		return errf(404, "%s not found", rel)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errf(400, "%s is a folder", rel)
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	h.Set("Cache-Control", "no-store")
	h.Set("X-File-Mtime", strconv.FormatInt(mtimeMs(st), 10))
	w.WriteHeader(200)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
	return nil
}

// writeFile: PUT /files/raw?path=…&offset=…&final=1 writes one chunk to <path>.ph-upload; offset 0 starts over, any
// other offset must equal the bytes received so far; final moves the file into place.
func (a *API) writeFile(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, true)
	if err != nil {
		return err
	}
	rel, fsPath, err := resolveFilePath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	if rel == "" {
		return errf(400, "path is required")
	}
	offset := float64(0)
	if v := r.URL.Query().Get("offset"); strings.TrimSpace(v) != "" {
		if offset, err = strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
			return errf(400, "Bad offset")
		}
	}
	if offset < 0 || offset != math.Trunc(offset) || offset > math.MaxInt64/2 {
		return errf(400, "Bad offset")
	}
	off := int64(offset)
	final := flag(r, "final")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileChunkBytes))
	if err != nil {
		return err
	}
	tmp := fsPath + uploadSuffix

	unlock, err := c.lock(r.Context())
	if err != nil {
		return err
	}
	defer unlock()
	if st, err := os.Stat(fsPath); err == nil && st.IsDir() {
		return errf(400, "%s is a folder", rel)
	}
	if err := os.MkdirAll(filepath.Dir(fsPath), 0o755); err != nil {
		return err
	}
	have := int64(0)
	if off != 0 {
		have = -1
		if st, err := os.Stat(tmp); err == nil {
			have = st.Size()
		}
	}
	if have != off {
		return errf(409, "Upload out of order: server has %d bytes, got offset %d", max(have, 0), off)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if off != 0 {
		flags = os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(tmp, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if final {
		if err := os.Rename(tmp, fsPath); err != nil {
			return err
		}
	}
	writeJSON(w, 200, map[string]any{"path": rel, "size": off + int64(len(body)), "final": final})
	return nil
}

func (a *API) deleteFile(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, true)
	if err != nil {
		return err
	}
	rel, fsPath, err := resolveFilePath(c, r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	if rel == "" || !strings.Contains(rel, "/") {
		name := rel
		if name == "" {
			name = "the instance root"
		}
		return errf(400, "Refusing to delete %s; delete files inside it instead", name)
	}
	recursive := flag(r, "recursive")
	unlock, err := c.lock(r.Context())
	if err != nil {
		return err
	}
	defer unlock()
	st, err := os.Lstat(fsPath)
	if errors.Is(err, fs.ErrNotExist) {
		return errf(404, "%s not found", rel)
	} else if err != nil {
		return err
	}
	if st.IsDir() {
		if !recursive {
			entries, err := os.ReadDir(fsPath)
			if err != nil {
				return err
			}
			if len(entries) > 0 {
				return errf(409, "%s is a folder with files in it; pass recursive to delete it", rel)
			}
		}
		err = os.RemoveAll(fsPath)
	} else {
		err = os.Remove(fsPath)
	}
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"path": rel, "deleted": true})
	return nil
}

// readBundle: POST /files/bundle {"paths": [...]} answers the files' contents as one gzipped bundle, in the upload's
// format (bundle.go: content-addressed, every entry its sha256), so pull and dev fetch many files in one request
// instead of one each. A path that's missing, a folder or unreachable is left out: the caller compares the hashes it
// expected (from the tree) with what came. The files together may hold at most maxBundleRawBytes.
func (a *API) readBundle(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, false)
	if err != nil {
		return err
	}
	body, err := readJSON(w, r)
	if err != nil {
		return err
	}
	raw, _ := body["paths"].([]any)
	if len(raw) > maxListEntries {
		return errf(400, "Too many paths: ask for at most %d", maxListEntries)
	}
	type want struct{ fsPath string }
	var files []want
	var total int64
	for _, v := range raw {
		p, _ := v.(string)
		_, fsPath, err := resolveFilePath(c, p)
		if err != nil {
			continue
		}
		st, err := os.Stat(fsPath)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if total += st.Size(); total > maxBundleRawBytes {
			return errf(400, "The files are more than %d MB together: ask for fewer at a time", maxBundleRawBytes>>20)
		}
		files = append(files, want{fsPath})
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	gz := gzip.NewWriter(w)
	if _, err := gz.Write(bundleMagic); err != nil {
		return errHeadersSent
	}
	for _, f := range files {
		data, err := os.ReadFile(f.fsPath)
		if err != nil {
			continue // gone since the stat: the caller sees it missing
		}
		sum := sha256.Sum256(data)
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		if _, err := gz.Write(sum[:]); err != nil {
			return errHeadersSent
		}
		_, _ = gz.Write(n[:])
		if _, err := gz.Write(data); err != nil {
			return errHeadersSent
		}
	}
	if err := gz.Close(); err != nil {
		return errHeadersSent
	}
	return nil
}
