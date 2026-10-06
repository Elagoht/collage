package collage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

type dataView struct{ Title string }

func TestData_RecordsTheType(t *testing.T) {
	viewType := reflect.TypeFor[dataView]()
	load := Load(func(context.Context, *RenderContext) (dataView, error) { return dataView{Title: "t"}, nil })
	handler := DataHandler(func(context.Context, *RenderContext) (*dataView, []string, error) {
		return &dataView{}, []string{"tag"}, nil
	})
	loose := Load(func(context.Context, *RenderContext) (any, error) { return 1, nil }) // any: the explicit opt-out the check skips

	tests := []struct {
		name string
		d    Data
		kind types.DataKind
		typ  reflect.Type
	}{
		{"Load", load, types.DataFetched, viewType},
		{"DataHandler", handler, types.DataFetched, reflect.TypeFor[*dataView]()},
		{"Value", Value(dataView{}), types.DataFixed, viewType},
		{"Effect", Effect(func(context.Context, *RenderContext) error { return nil }), types.DataEffect, nil},
		{"Load[any] is unknown", loose, types.DataFetched, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := NewFragment("f", "f.html").WithData(test.d).Build()
			got := f.DataSource()
			if got.Kind != test.kind || got.Type != test.typ {
				t.Errorf("source = kind %v type %v, want kind %v type %v", got.Kind, got.Type, test.kind, test.typ)
			}
		})
	}
}

func TestData_HandlersKeepTheirBehaviour(t *testing.T) {
	failing := DataHandler(func(context.Context, *RenderContext) (*dataView, []string, error) {
		return &dataView{}, []string{"t"}, errors.New("boom")
	}).source()
	data, tags, err := failing.Handler(context.Background(), nil)
	if data != nil || len(tags) != 1 || err == nil {
		t.Errorf("failed handler = %v, %v, %v; want nil data, its tags, the error", data, tags, err)
	}
	if Load[dataView](nil) != nil || DataHandler[dataView](nil) != nil || Effect(nil) != nil {
		t.Error("a nil fn must give a nil Data")
	}
	if Value[error](nil) != nil {
		t.Error("a nil interface value must give a nil Data")
	}
	if Value[*dataView](nil) == nil {
		t.Error("a typed nil pointer is data, its type known")
	}
}

func TestData_ValueOfAnInterfaceRecordsTheDynamicType(t *testing.T) {
	src := Value[any](dataView{}).source() // any: the interface a map[string]any hands over
	if src.Kind != types.DataFixed || src.Type != reflect.TypeFor[dataView]() {
		t.Errorf("source = kind %v type %v, want fixed data of dataView", src.Kind, src.Type)
	}
	if Value[error](nil) != nil {
		t.Error("a nil interface value must stay a nil Data")
	}
	loose := Load(func(context.Context, *RenderContext) (any, error) { return dataView{}, nil }) // any: a handler's interface T stays unknown
	if typ := loose.source().Type; typ != nil {
		t.Errorf("Load[any] type = %v, want unknown", typ)
	}
}

func TestData_SetTwiceIsAConflict(t *testing.T) {
	b := NewFragment("f", "f.html").WithData(Value(1)).WithData(Value(2))
	if err := b.BuildErr(); !errors.Is(err, ErrConflictingData) {
		t.Errorf("BuildErr = %v, want ErrConflictingData", err)
	}
}

func TestData_WithoutTypeCheck(t *testing.T) {
	if !NewFragment("f", "f.html").WithoutTypeCheck().Build().SkipTypeCheck {
		t.Error("WithoutTypeCheck did not set SkipTypeCheck")
	}
}
