package asset

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
)

// ContentType returns the type to serve the file name holds, reading the start
// of content when the name says nothing, and leaving content where it found it.
//
// Sniffed rather than refused, because what readers upload often has no
// extension — "/uploads/logo" — and an image plugin, or a browser, needs to be
// told it is an image. But sniffed only into what cannot run: a file that looks
// like markup is text/plain, never text/html or text/xml, which would run its
// script on this site's origin. With nosniff beside it the browser keeps to
// that.
func ContentType(name string, content io.ReadSeeker) string {
	if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
		return ctype
	}
	var head [512]byte
	n, _ := io.ReadFull(content, head[:])
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return "application/octet-stream"
	}
	ctype := http.DetectContentType(head[:n])
	if strings.HasPrefix(ctype, "text/html") || strings.HasPrefix(ctype, "text/xml") {
		return "text/plain; charset=utf-8"
	}
	return ctype
}
