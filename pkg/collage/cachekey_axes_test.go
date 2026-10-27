package collage_test

// What a cached page's entry is keyed on, from the request: the host, spelled
// one way whatever spelling the request used, and the query parameters the
// page declares. A client inventing hosts or tracking parameters does not get a
// render and an entry per request.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// hostEchoSite serves /hot, a Static page whose data handler writes the host
// and query its shared render was given, counting renders.
func hostEchoSite(t *testing.T, cacheParams ...string) (*collagetest.Site, *atomic.Int32) {
	t.Helper()
	var renders atomic.Int32
	echo := collage.NewFragment("echo", "echo.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			renders.Add(1)
			return "host=" + rc.Request.Host + " query=" + rc.Request.URL.RawQuery, nil
		})).Build()
	b := collage.NewPage("hot").WithContent(echo).WithPath("en", "/hot").Static()
	if cacheParams != nil {
		b = b.WithCacheParams(cacheParams...)
	}
	site := collagetest.NewSite(t,
		collagetest.WithTemplates(map[string]string{"echo.html": `<p>{{.}}</p>`}),
		collagetest.WithRegister(b.Build()),
		collagetest.WithConfig(func(cfg *collage.Config) {
			cfg.Cache = collage.CacheConfig{Enabled: true, Type: collage.CacheMemory, MaxEntries: 50, DefaultTTL: time.Hour}
		}))
	return site, &renders
}

func getHost(site *collagetest.Site, host, target string) *collagetest.Response {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = host
	return site.Do(r)
}

func TestCacheKey_HostSpellingsShareOneEntry(t *testing.T) {
	site, renders := hostEchoSite(t)
	spellings := []string{"example.com", "EXAMPLE.com:8080", "example.com.", "Example.COM.:443", "example.com:1"}
	for _, host := range spellings {
		res := getHost(site, host, "/hot").WantStatus(http.StatusOK)
		if !strings.Contains(res.Body, "host=example.com ") {
			t.Errorf("Host %q: body %q, want the render made for host=example.com", host, res.Body)
		}
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("renders = %d for %d spellings of one host, want 1", n, len(spellings))
	}
}

// The first request's spelling is not what everyone is served: the render sees
// the normalised host, port and case gone.
func TestCacheKey_RenderSeesTheNormalisedHost(t *testing.T) {
	site, _ := hostEchoSite(t)
	first := getHost(site, "EXAMPLE.com:8080", "/hot").WantStatus(http.StatusOK)
	second := getHost(site, "example.com", "/hot").WantStatus(http.StatusOK)
	for _, res := range []*collagetest.Response{first, second} {
		if strings.Contains(res.Body, "8080") || strings.Contains(res.Body, "EXAMPLE") {
			t.Errorf("body %q carries the first request's spelling", res.Body)
		}
	}
}

func TestCacheKey_IPv6HostSpellingsShareOneEntry(t *testing.T) {
	site, renders := hostEchoSite(t)
	for _, host := range []string{"[::1]", "[::1]:8080", "[0:0::1]:9"} {
		res := getHost(site, host, "/hot").WantStatus(http.StatusOK)
		if !strings.Contains(res.Body, "host=[::1] ") {
			t.Errorf("Host %q: body %q, want host=[::1]", host, res.Body)
		}
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("renders = %d, want 1", n)
	}
}

// Distinct hosts are still distinct entries: what a render builds from its host
// is not handed to another.
func TestCacheKey_DistinctHostsStayApart(t *testing.T) {
	site, renders := hostEchoSite(t)
	a := getHost(site, "a.example", "/hot")
	b := getHost(site, "b.example", "/hot")
	if renders.Load() != 2 || !strings.Contains(a.Body, "host=a.example") || !strings.Contains(b.Body, "host=b.example") {
		t.Errorf("renders %d; a %q; b %q", renders.Load(), a.Body, b.Body)
	}
}

// A page declaring its CacheParams is rendered from a request holding only
// them, so an undeclared parameter is no part of its key: 200 tracking values
// are one render. A declared one still splits.
func TestCacheKey_UndeclaredQueryIsNoPartOfTheKey(t *testing.T) {
	site, renders := hostEchoSite(t, "page")
	for i := range 200 {
		res := getHost(site, "example.com", fmt.Sprintf("/hot?utm=%d", i)).WantStatus(http.StatusOK)
		if strings.Contains(res.Body, "utm") {
			t.Fatalf("the render saw an undeclared parameter: %q", res.Body)
		}
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("renders = %d for 200 utm values, want 1", n)
	}
	getHost(site, "example.com", "/hot?page=2&utm=x")
	getHost(site, "example.com", "/hot?utm=y&page=2")
	getHost(site, "example.com", "/hot?page=3")
	if n := renders.Load(); n != 3 {
		t.Errorf("renders = %d after page=2 (twice, any order) and page=3, want 3", n)
	}
}
