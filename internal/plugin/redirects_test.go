package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

type redirectsPlugin struct {
	name  string
	rules []BuiltRedirect
}

func (p redirectsPlugin) Name() string                     { return p.name }
func (p redirectsPlugin) Version() string                  { return "0" }
func (p redirectsPlugin) Init(context.Context, Host) error { return nil }
func (p redirectsPlugin) Shutdown(context.Context) error   { return nil }
func (p redirectsPlugin) Redirects() []BuiltRedirect       { return p.rules }

func TestRegistry_PluginRedirects(t *testing.T) {
	r := NewRegistry(nil)
	mustRegister(t, r, redirectsPlugin{name: "a", rules: []BuiltRedirect{{From: "/x", To: "/y", Status: 301}}})
	mustRegister(t, r, redirectsPlugin{name: "b", rules: []BuiltRedirect{{From: "/gone", Status: 410}}})
	got, err := r.PluginRedirects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Source != "a" || got[1].Source != "b" || got[1].Status != 410 {
		t.Errorf("PluginRedirects = %+v", got)
	}

	bad := NewRegistry(nil)
	mustRegister(t, bad, redirectsPlugin{name: "evil", rules: []BuiltRedirect{{From: "/x\r\n", To: "/y", Status: 301}}})
	if _, err := bad.PluginRedirects(); !errors.Is(err, types.ErrInvalidRedirect) {
		t.Errorf("err = %v, want ErrInvalidRedirect naming the plugin", err)
	}
}
