// Package collagetest drives a collage application from a test the way a browser
// does: through its http.Handler, with cookies kept between requests and forms
// submitted with what the page put in them.
//
// It needs no server and no port. A test builds the application the way main
// does, hands its handler to New, and reads pages and submits their forms:
//
//	func TestLogin(t *testing.T) {
//		c := collagetest.New(t, newApp(t).Handler())
//
//		page := c.Get("/login").WantStatus(http.StatusOK)
//		res := c.Submit(page, "/login", url.Values{
//			"email":    {"ada@example.com"},
//			"password": {"correct horse"},
//		})
//
//		res.WantStatus(http.StatusSeeOther)
//		if res.Location() != "/panel" {
//			t.Errorf("Location = %q, want /panel", res.Location())
//		}
//	}
//
// Submit is what makes a form test short. A form's forgery token, and anything a
// plugin stamps into it — a honeypot's timestamp — is a hidden input the page
// rendered; Submit carries every hidden input of the form across, with the
// cookies the page set, so a test fills in only the fields a reader would.
//
// Redirects are not followed: a test usually wants to see the 303 and where it
// points. Follow takes it when the page behind it is what the test is about.
package collagetest

import (
	"bytes"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// origin is where requests are addressed when a test gives a path alone: the host
// net/http/httptest uses, over plain HTTP. A site whose cookies are Secure is
// tested with absolute https:// targets, which httptest marks as arriving over
// TLS, so the jar sends those cookies back.
var origin = &url.URL{Scheme: "http", Host: "example.com"}

// Client sends requests to one http.Handler and keeps the cookies it sets, as one
// browser would. It is not safe for concurrent use: a browser tab is one reader.
type Client struct {
	t       testing.TB
	handler http.Handler
	jar     *cookiejar.Jar
}

// New returns a Client for h, reporting through t. Each Client starts with no
// cookies; two readers are two Clients.
func New(t testing.TB, h http.Handler) *Client {
	t.Helper()
	if h == nil {
		t.Fatal("collagetest: New with a nil handler")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("collagetest: cookie jar: %v", err)
	}
	return &Client{t: t, handler: h, jar: jar}
}

// Response is what the handler answered, read in full.
type Response struct {
	// Status is the response's status code.
	Status int
	// Header is the response's header.
	Header http.Header
	// Body is the response's body.
	Body string
	// URL is the address the request was sent to, which a relative form action
	// or Location is resolved against.
	URL *url.URL
	// Method is the request's method.
	Method string

	t testing.TB
}

// Request returns a request for target — a path, or an absolute URL — with the
// client's cookies not yet attached; Do attaches them. It is for a request Get
// and Submit do not make: a JSON body, a header of its own.
//
//	req := c.Request(http.MethodPost, "/api/count", nil)
//	req.Header.Set("X-CSRF-Token", page.CSRFToken())
//	res := c.Do(req)
func (c *Client) Request(method, target string, body io.Reader) *http.Request {
	c.t.Helper()
	u, err := origin.Parse(target)
	if err != nil {
		c.t.Fatalf("collagetest: target %q: %v", target, err)
	}
	return httptest.NewRequest(method, u.String(), body)
}

// Do sends req with the cookies the client holds for its URL, keeps the cookies
// the response sets, and returns the response.
func (c *Client) Do(req *http.Request) *Response {
	c.t.Helper()
	for _, cookie := range c.jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	result := rec.Result()
	defer result.Body.Close()

	if cookies := result.Cookies(); len(cookies) > 0 {
		c.jar.SetCookies(req.URL, cookies)
	}
	body, err := io.ReadAll(result.Body)
	if err != nil {
		c.t.Fatalf("collagetest: %s %s: reading the body: %v", req.Method, req.URL, err)
	}
	return &Response{Status: result.StatusCode, Header: result.Header, Body: string(body), URL: req.URL, Method: req.Method, t: c.t}
}

// Get sends a GET for target, a path or an absolute URL.
func (c *Client) Get(target string) *Response {
	c.t.Helper()
	return c.Do(c.Request(http.MethodGet, target, nil))
}

// Submit submits the form of page whose action is action, the way a reader
// pressing its button would: with every hidden input the form carries, and
// values on top of them — a name in values replaces a hidden input of the same
// name rather than adding to it. action is matched after both it and the form's
// own action are resolved against the page's URL and compared decoded, so
// "/login", "login" from a page beside it, and an absolute URL all name the same
// form, and "/giriş" names one whose action is "/giri%c5%9f"; a form with no action
// submits to the page itself. An empty action means the page's only form.
//
// The form's method and enctype are honoured: a GET form puts its values in the
// query, a multipart/form-data form is sent as multipart, anything else as
// application/x-www-form-urlencoded. A page with no such form, or several, fails
// the test.
func (c *Client) Submit(page *Response, action string, values url.Values) *Response {
	c.t.Helper()
	if page == nil {
		c.t.Fatal("collagetest: Submit with a nil page")
	}
	f := c.findForm(page, action)

	target, err := page.URL.Parse(f.action)
	if err != nil {
		c.t.Fatalf("collagetest: form action %q: %v", f.action, err)
	}
	fields := url.Values{}
	for _, hidden := range f.hidden {
		fields.Add(hidden.name, hidden.value)
	}
	maps.Copy(fields, values)

	if f.method != "post" {
		target.RawQuery = fields.Encode()
		return c.Do(c.Request(http.MethodGet, target.String(), nil))
	}
	if f.enctype == "multipart/form-data" {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		for name, vs := range fields {
			for _, v := range vs {
				if err := w.WriteField(name, v); err != nil {
					c.t.Fatalf("collagetest: multipart field %q: %v", name, err)
				}
			}
		}
		if err := w.Close(); err != nil {
			c.t.Fatalf("collagetest: multipart body: %v", err)
		}
		req := c.Request(http.MethodPost, target.String(), &body)
		req.Header.Set("Content-Type", w.FormDataContentType())
		return c.Do(req)
	}
	req := c.Request(http.MethodPost, target.String(), strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.Do(req)
}

// findForm returns page's one form submitting to action, failing the test when
// there is not exactly one.
func (c *Client) findForm(page *Response, action string) form {
	c.t.Helper()
	all, _ := scan(page.Body)
	if action == "" {
		if len(all) != 1 {
			c.t.Fatalf("collagetest: %s has %d forms; name the one to submit by its action", page.URL, len(all))
		}
		return all[0]
	}

	want, err := page.URL.Parse(action)
	if err != nil {
		c.t.Fatalf("collagetest: action %q: %v", action, err)
	}
	var found []form
	var seen []string
	for _, f := range all {
		got, err := page.URL.Parse(f.action)
		if err != nil {
			continue
		}
		seen = append(seen, got.RequestURI())
		if got.Host == want.Host && got.Path == want.Path && got.RawQuery == want.RawQuery {
			found = append(found, f)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		c.t.Fatalf("collagetest: %s has no form submitting to %s; its forms submit to %v", page.URL, want.RequestURI(), seen)
	default:
		c.t.Fatalf("collagetest: %s has %d forms submitting to %s", page.URL, len(found), want.RequestURI())
	}
	return form{}
}

// Follow sends a GET for the Location res redirects to, resolved against the
// URL res answered. A response that is not a redirect, or names no Location,
// fails the test.
func (c *Client) Follow(res *Response) *Response {
	c.t.Helper()
	if res.Status < 300 || res.Status > 399 {
		c.t.Fatalf("collagetest: Follow: %s answered %d, not a redirect", res.URL, res.Status)
	}
	location := res.Header.Get("Location")
	if location == "" {
		c.t.Fatalf("collagetest: Follow: %s answered %d with no Location", res.URL, res.Status)
	}
	target, err := res.URL.Parse(location)
	if err != nil {
		c.t.Fatalf("collagetest: Follow: Location %q: %v", location, err)
	}
	return c.Get(target.String())
}

// Location returns the response's Location header as the handler wrote it.
func (r *Response) Location() string {
	return r.Header.Get("Location")
}

// WantStatus fails the test unless the response's status is status, showing the
// body, which usually says why. It returns r, so a request and its check read
// as one line.
func (r *Response) WantStatus(status int) *Response {
	r.t.Helper()
	if r.Status != status {
		r.t.Fatalf("%s %s = %d, want %d; body:\n%s", r.Method, r.URL, r.Status, status, r.Body)
	}
	return r
}

// CSRFToken returns the forgery token the page rendered — the value of its first
// hidden _csrf input, in a form or not — for a request that sends it in the X-CSRF-Token header
// rather than in a form. A page with none fails the test. A site that renamed
// the field reads it from the body itself.
func (r *Response) CSRFToken() string {
	r.t.Helper()
	_, hidden := scan(r.Body)
	for _, h := range hidden {
		if h.name == "_csrf" {
			return h.value
		}
	}
	r.t.Fatalf("collagetest: %s has no hidden _csrf input", r.URL)
	return ""
}
