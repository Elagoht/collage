package collage_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func exportedMethods(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	return names
}

// An internal type reached through an alias publishes every exported method it
// has. These are the ones meant for users; a new one fails here until it is
// added on purpose.
func TestAliasedTypes_MethodAllowlist(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		want []string
	}{
		{reflect.TypeOf((*collage.Page)(nil)), []string{"Locales", "PathFor", "Validate"}},
		{reflect.TypeOf((*collage.Fragment)(nil)), []string{"Validate"}},
		{reflect.TypeOf((*collage.Result)(nil)), []string{"Degraded"}},
		{reflect.TypeOf((*collage.Document)(nil)), []string{"Locales", "PathFor", "RootPath", "Validate"}},
		{reflect.TypeOf((*collage.Action)(nil)), nil},
		{reflect.TypeOf((*collage.RenderContext)(nil)), []string{"ActionURL", "Asset", "Context", "Hoist", "HoistAlternate", "HoistLink", "HoistMeta", "HoistProperty", "HoistStylesheet", "HoistTitle", "Hoisted", "Param", "URL", "WithContext", "WithFragment"}},
	} {
		if got := exportedMethods(c.typ); !slices.Equal(got, c.want) {
			t.Errorf("%v methods = %v, want %v", c.typ, got, c.want)
		}
	}
}

// A store that only gets, sets and drops entries is a Cache; invalidating tags
// belongs to a TaggedCache, the only kind the framework invalidates.
type getSetCache struct{}

func (getSetCache) Get(context.Context, string) ([]byte, string, bool) { return nil, "", false }
func (getSetCache) Set(context.Context, string, []byte, time.Duration) (string, error) {
	return "", nil
}
func (getSetCache) InvalidateKey(context.Context, string) error { return nil }
func (getSetCache) Clear(context.Context) error                 { return nil }

func TestCache_GetSetIsEnough(t *testing.T) {
	var _ collage.Cache = getSetCache{}
}
