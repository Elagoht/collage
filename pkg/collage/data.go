package collage

import (
	"context"
	"reflect"

	"github.com/Elagoht/collage/internal/types"
)

// Data is what a fragment renders with: a handler run for every render, a value
// fixed when the program starts, or an effect. Only Load, DataHandler, Value and
// Effect make one, so the Go type a fragment's template sees is always known —
// which is what lets registration check the template against it.
//
//	collage.NewFragment("post", "post.html").WithData(collage.Load(loadPost))
type Data interface {
	source() types.DataSource
}

type data struct{ s types.DataSource }

func (d data) source() types.DataSource { return d.s }

// templateType is T as the template's dot: unknown, and so not checked, when T
// is an interface — collage.Load[any] written on purpose.
func templateType[T any]() reflect.Type {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Interface {
		return nil
	}
	return t
}

// Load fetches a fragment's data on every render, reporting no dependency tags
// — for a page that is not cached, or whose data does not change:
//
//	collage.NewFragment("clock", "fragments/clock.html").
//		WithData(collage.Load(func(ctx context.Context, rc *collage.RenderContext) (clockView, error) {
//			return clockView{Now: time.Now()}, nil
//		})).
//		Build()
//
// A handler whose page is cached and whose data changes — a post, a count —
// should report that data's tags, which is DataHandler's shape. On an error the
// data is dropped. A nil fn is a nil Data.
func Load[T any](fn func(context.Context, *RenderContext) (T, error)) Data {
	if fn == nil {
		return nil
	}
	return data{types.FetchedData(func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: html/template's parameter type
		v, err := fn(ctx, rc)
		if err != nil {
			return nil, nil, err
		}
		return v, nil, nil
	}, templateType[T]())}
}

// DataHandler fetches a fragment's data on every render and reports the
// dependency tags it was derived from, so a cached page built from it is
// invalidated with it:
//
//	collage.NewFragment("post", "post.html").WithData(collage.DataHandler(loadPost))
//
// where loadPost returns (Post, []string, error). On an error the data is
// dropped rather than boxed — a nil *view returned with an error would
// otherwise become a typed nil that reads as present — and the tags are kept. A
// nil fn is a nil Data.
func DataHandler[T any](fn func(context.Context, *RenderContext) (T, []string, error)) Data {
	if fn == nil {
		return nil
	}
	return data{types.FetchedData(func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: html/template's parameter type
		v, tags, err := fn(ctx, rc)
		if err != nil {
			return nil, tags, err
		}
		return v, tags, nil
	}, templateType[T]())}
}

// Value hands the fragment's template v on every render — data fixed when the
// program starts, a list of links or a heading. Unlike a handler, it leaves a
// page that declares no strategy static. A nil interface value is a nil Data,
// as WithData(nil) is no data.
func Value[T any](v T) Data {
	if any(v) == nil { // any: only a nil interface value boxes to nil
		return nil
	}
	return data{types.FixedData(v, templateType[T]())}
}

// Effect runs fn on every render for what it declares — rc.HoistTitle, a
// plugin's Emit — and gives the template no data. It reports no dependency
// tags; see DataHandler for a handler whose declarations come from data that
// changes. A nil fn is a nil Data.
func Effect(fn func(context.Context, *RenderContext) error) Data {
	if fn == nil {
		return nil
	}
	return data{types.EffectData(func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: html/template's parameter type
		return nil, nil, fn(ctx, rc)
	})}
}
