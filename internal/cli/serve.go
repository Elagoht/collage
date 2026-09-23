package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/term"
)

// serveUsage is "collage help serve"'s own usage text.
const serveUsage = `Usage: collage serve [-dir dir] [-host name] [-port n]

Serves a static export the way a static host would, so what you see is what you
will get after deploying it.

That is the whole reason this exists rather than "open dist/index.html": a file://
page has no root, so every absolute link and stylesheet in the export is broken,
and a plain file server is not a static host either. This one answers a
directory with its index.html and nothing else — no listings — serves 404.html
with a 404 when a path resolves to nothing, and sends no caching headers, so
re-exporting and reloading shows the new output rather than the old.

It serves files. It does not run the project: for that, "collage dev".

  -dir dir    directory to serve (default "dist")
  -host name  interface to listen on (default "localhost")
  -port n     port to listen on (default 4000)
`

// runServe implements the "serve" command.
func (c *CLI) runServe(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), serveUsage) }
	dir := fs.String("dir", "dist", "directory to serve")
	host := fs.String("host", "localhost", "interface to listen on")
	// 4000 rather than 3000, so this and a project running under "collage dev"
	// can be up at the same time — which is exactly when somebody compares them.
	port := fs.Int("port", 4000, "port to listen on")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(c.stderr(), "collage: serve takes no positional arguments")
		fmt.Fprintln(c.stderr())
		fs.Usage()
		return 2
	}

	handler, count, err := staticHandler(*dir)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: serve: %v\n", err)
		return 1
	}

	address := net.JoinHostPort(*host, fmt.Sprint(*port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: serve: %v\n", err)
		return 1
	}

	s := term.NewStyle(c.stdout())
	fmt.Fprintf(c.stdout(), "\n%s %s\n    %s\n\n",
		s.OK(s.Mark("✓", "+")),
		s.Bold("http://"+address),
		s.Dim(fmt.Sprintf("serving %s · %d files · Ctrl-C to stop", *dir, count)))

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		return 0
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(c.stderr(), "collage: serve: %v\n", err)
			return 1
		}
		return 0
	}
}

// staticHandler returns a handler over dir and the number of files in it.
//
// It reports a directory that is not there, or holds nothing, rather than serving
// an empty site: "collage serve" right after "collage export" is the usual order,
// and the usual mistake is running it before.
func staticHandler(dir string) (http.Handler, int, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w — run \"collage export\" first", dir, err)
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("%s is not a directory", dir)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, 0, err
	}
	fsys := root.FS()

	count := 0
	_ = fs.WalkDir(fsys, ".", func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			count++
		}
		return nil
	})
	if count == 0 {
		return nil, 0, fmt.Errorf("%s is empty — run \"collage export\" first", dir)
	}

	return &staticSite{fsys: fsys}, count, nil
}

// staticSite serves an exported site the way a static host does.
type staticSite struct{ fsys fs.FS }

func (s *staticSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Nothing here may be cached. The point of this command is to look at what
	// was just exported, and a browser holding the previous export is the one
	// thing that stops it.
	w.Header().Set("Cache-Control", "no-store")

	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}

	if file, ok := s.open(name); ok {
		s.write(w, r, name, file)
		return
	}
	// A path with no extension is a page, and a page is a directory holding an
	// index.html — which is the shape "collage export" writes and every static
	// host resolves.
	if path.Ext(name) == "" {
		if file, ok := s.open(path.Join(name, "index.html")); ok {
			s.write(w, r, path.Join(name, "index.html"), file)
			return
		}
	}

	s.notFound(w, r)
}

// open returns a readable regular file, or reports that there is none.
//
// A directory is not one: serving a listing would show what an export contains to
// anyone who asks, and no static host does it.
func (s *staticSite) open(name string) (fs.File, bool) {
	if !fs.ValidPath(name) {
		return nil, false
	}
	file, err := s.fsys.Open(name)
	if err != nil {
		return nil, false
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		file.Close()
		return nil, false
	}
	return file, true
}

// write sends one file.
func (s *staticSite) write(w http.ResponseWriter, r *http.Request, name string, file fs.File) {
	defer file.Close()

	if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "cannot stat", http.StatusInternalServerError)
		return
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "not seekable", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

// notFound answers with the export's own 404.html when it has one, which is what a
// static host serves and therefore what should be seen here.
func (s *staticSite) notFound(w http.ResponseWriter, r *http.Request) {
	if file, ok := s.open("404.html"); ok {
		defer file.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, file)
		}
		return
	}
	http.Error(w, "404 page not found", http.StatusNotFound)
}
