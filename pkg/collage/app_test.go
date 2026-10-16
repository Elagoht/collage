package collage_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// The methods App exposes are exactly the spec's list: nothing internal/build
// alone needs is reachable from a user's *App.
func TestApp_MethodSet(t *testing.T) {
	want := []string{
		"ActionURL", "Actions", "BaseURL", "BuildID", "Check", "Commands", "DevMode",
		"Documents", "FragmentURL", "Handle", "Handler", "InvalidateTags", "InvalidateTagsN",
		"ListenAndServe", "Locales", "Logger", "Mount", "OriginFor", "Page", "PageURLs", "Pages", "PurgeTags",
		"Register", "RegisterAction", "RegisterCommand", "RegisterDocument", "RegisterErrorPage",
		"RegisterNotFoundPage", "RegisterPage", "RegisterPlugin", "RenderDocumentPath",
		"RenderFragment", "RenderPath", "ServeStatus", "Shutdown", "Start", "URL", "Use",
	}
	typ := reflect.TypeOf((*collage.App)(nil))
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("App methods =\n%v\nwant\n%v", got, want)
	}
}

func TestBuild_NilAppAndEmptyOutDir(t *testing.T) {
	if _, err := collage.Build(context.Background(), nil, collage.BuildOptions{OutDir: t.TempDir()}); !errors.Is(err, collage.ErrNilApp) {
		t.Errorf("nil app: %v", err)
	}
	app, err := collage.New(&collage.Config{Template: collage.TemplateConfig{Root: mountTemplateRoot(t)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collage.Build(context.Background(), app, collage.BuildOptions{}); !errors.Is(err, collage.ErrEmptyOutDir) {
		t.Errorf("empty OutDir: %v", err)
	}
}
