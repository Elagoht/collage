package types

import (
	"context"
	"reflect"
	"testing"
)

func TestDataSource_Constructors(t *testing.T) {
	h := func(context.Context, *RenderContext) (any, []string, error) { return nil, nil, nil } // any: matches DataHandlerFunc
	strType := reflect.TypeFor[string]()

	if got := FixedData("x", strType); got.Kind != DataFixed || got.Value != "x" || got.Type != strType {
		t.Errorf("FixedData = %+v", got)
	}
	if got := FetchedData(h, strType); got.Kind != DataFetched || got.Handler == nil || got.Type != strType {
		t.Errorf("FetchedData = %+v", got)
	}
	if got := EffectData(h); got.Kind != DataEffect || got.Handler == nil || got.Type != nil {
		t.Errorf("EffectData = %+v", got)
	}
	if got := FetchedData(nil, strType); got.Kind != DataNone {
		t.Errorf("FetchedData(nil) = %+v, want no data", got)
	}
	if got := EffectData(nil); got.Kind != DataNone {
		t.Errorf("EffectData(nil) = %+v, want no data", got)
	}

	var f *Fragment
	if got := FragmentDataSource(f); got.Kind != DataNone {
		t.Errorf("nil Fragment DataSource = %+v", got)
	}
	f = &Fragment{}
	SetFragmentDataSource(f, FixedData("x", strType))
	if FragmentDataSource(f).Value != "x" {
		t.Errorf("SetDataSource did not stick: %+v", FragmentDataSource(f))
	}
}
