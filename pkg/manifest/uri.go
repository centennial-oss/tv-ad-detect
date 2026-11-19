// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// uriScheme returns the scheme of an absolute URI, or "" for a plain path or
// relative reference. Single-letter schemes are treated as Windows drive
// letters and are not reported.
func uriScheme(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		case c == ':':
			if i >= 2 {
				return s[:i]
			}
			return ""
		default:
			return ""
		}
	}
	return ""
}

// IsRemote reports whether uri names a non-local resource: anything with a
// URI scheme other than file. Plain paths and file:// URIs are local.
func IsRemote(uri string) bool {
	scheme := uriScheme(uri)
	return scheme != "" && !strings.EqualFold(scheme, schemeFile)
}

// LocalPath converts a plain path or file:// URI into a local filesystem
// path. Plain paths are returned unchanged. It fails with ErrUnsupported for
// any other scheme or a file URI naming a remote host.
func LocalPath(uri string) (string, error) {
	scheme := uriScheme(uri)
	if scheme == "" {
		return uri, nil
	}
	if !strings.EqualFold(scheme, schemeFile) {
		return "", errf(ErrUnsupported, "%q is not a local path or file URI", uri)
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", errf(ErrInvalid, msgMalformedFileURI, uri)
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", errf(ErrUnsupported, "file URI %q names a remote host", uri)
	}
	p := u.Path
	if p == "" && u.Opaque != "" {
		if p, err = url.PathUnescape(u.Opaque); err != nil {
			return "", errf(ErrInvalid, msgMalformedFileURI, uri)
		}
	}
	if p == "" {
		return "", errf(ErrInvalid, "file URI %q has no path", uri)
	}
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:] // "/C:/dir" -> "C:/dir"
	}
	return filepath.FromSlash(p), nil
}

// refBase is a base URI prepared for resolving many references.
type refBase struct {
	raw    string
	remote bool
	u      *url.URL // remote only, parsed lazily
	dir    string   // local only: cleaned directory prefix with trailing separator, or ""

	lastRef, lastOut string // one-entry cache (byte-range playlists repeat one URI)
}

// newBase prepares base (a resolved URI or local path) for reference
// resolution. A base ending in a separator is a directory.
func (l *loader) newBase(base string) *refBase {
	b := &refBase{raw: base, remote: IsRemote(base)}
	if b.remote {
		return b
	}
	i := strings.LastIndexByte(base, '/')
	if filepath.Separator != '/' {
		if j := strings.LastIndexByte(base, filepath.Separator); j > i {
			i = j
		}
	}
	if i < 0 {
		return b
	}
	dir := filepath.Clean(base[:i+1])
	if !os.IsPathSeparator(dir[len(dir)-1]) {
		dir += string(filepath.Separator)
	}
	b.dir = dir
	return b
}

// isSimpleRef reports whether ref is a plain relative path that needs no
// decoding or normalization: no scheme, query, fragment, escapes, dot
// segments, empty segments or trailing slash.
func isSimpleRef(ref string) bool {
	if ref == "" || ref[0] == '/' || ref[0] == '.' || ref[len(ref)-1] == '/' {
		return false
	}
	prev := byte(0)
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch c {
		case '%', '?', '#', ':', '\\':
			return false
		case '.', '/':
			if prev == '/' {
				return false
			}
		}
		prev = c
	}
	return true
}

// resolve resolves ref against b following RFC 3986 for remote bases and
// local path rules (with percent-decoding) for local bases. Remote results
// are refused unless Options.AllowRemote is set.
func (l *loader) resolve(b *refBase, ref string) (string, error) {
	if ref == b.lastRef && b.lastOut != "" {
		return b.lastOut, nil
	}
	var out string
	if !b.remote && filepath.Separator == '/' && isSimpleRef(ref) {
		out = ref
		if b.dir != "" {
			out = b.dir + ref
		}
	} else {
		var err error
		if out, err = l.resolveSlow(b, ref); err != nil {
			return "", err
		}
	}
	b.lastRef, b.lastOut = ref, out
	return out, nil
}

func (l *loader) resolveSlow(b *refBase, ref string) (string, error) {
	if scheme := uriScheme(ref); scheme != "" {
		if strings.EqualFold(scheme, schemeFile) {
			return LocalPath(ref)
		}
		return ref, l.checkRemote(ref)
	}
	if b.remote {
		if b.u == nil {
			u, err := url.Parse(b.raw)
			if err != nil {
				return "", errf(ErrInvalid, "malformed base URI %q", b.raw)
			}
			b.u = u
		}
		r, err := url.Parse(ref)
		if err != nil {
			return "", errf(ErrInvalid, "malformed URI reference %q", ref)
		}
		out := b.u.ResolveReference(r).String()
		return out, l.checkRemote(out)
	}
	// Local base: the reference is a URI reference naming a local file.
	p := ref
	if i := strings.IndexByte(p, '#'); i >= 0 {
		p = p[:i]
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if strings.IndexByte(p, '%') >= 0 {
		dec, err := url.PathUnescape(p)
		if err != nil {
			return "", errf(ErrInvalid, "malformed URI reference %q", ref)
		}
		p = dec
	}
	if p == "" {
		return b.raw, nil // same-document reference
	}
	keepSlash := false
	switch last := p[strings.LastIndexByte(p, '/')+1:]; last {
	case "", currentDir, "..":
		keepSlash = true
	}
	var out string
	if p[0] == '/' {
		out = filepath.Clean(filepath.FromSlash(p))
	} else {
		out = filepath.Join(b.dir, filepath.FromSlash(p))
	}
	if keepSlash && !os.IsPathSeparator(out[len(out)-1]) {
		out += string(filepath.Separator)
	}
	return out, nil
}

// checkRemote refuses remote URIs unless AllowRemote is set.
func (l *loader) checkRemote(uri string) error {
	if l.opts.AllowRemote || !IsRemote(uri) {
		return nil
	}
	return errf(ErrUnsupported, "remote resource %q (set AllowRemote to pass remote URIs to the resolver)", uri)
}

// normalizeTop validates the top-level manifest URI.
func (l *loader) normalizeTop(uri string) (string, error) {
	if uri == "" {
		return "", errf(ErrInvalid, "empty manifest URI")
	}
	if IsRemote(uri) {
		return uri, l.checkRemote(uri)
	}
	return LocalPath(uri)
}

// FileResolver reads local files (plain paths or file:// URIs). Zero value is usable.
type FileResolver struct{}

// Open implements [Resolver]. With offset 0 and length < 0 the returned
// reader is the *os.File itself.
func (FileResolver) Open(ctx context.Context, uri string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := LocalPath(uri)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, errf(ErrInvalid, "negative offset %d for %s", offset, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if length < 0 {
		return f, nil
	}
	return &rangeReader{f: f, n: length}, nil
}

// Fingerprint implements [Fingerprinter]: the file's size and modification
// time.
func (FileResolver) Fingerprint(ctx context.Context, uri string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := LocalPath(uri)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("size=%d;mtime=%d", st.Size(), st.ModTime().UnixNano()), nil
}

// rangeReader limits reads of a file to n bytes.
type rangeReader struct {
	f *os.File
	n int64
}

func (r *rangeReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.n {
		p = p[:r.n]
	}
	n, err := r.f.Read(p)
	r.n -= int64(n)
	return n, err
}

func (r *rangeReader) Close() error { return r.f.Close() }
