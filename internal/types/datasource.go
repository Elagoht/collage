package types

import "reflect"

// DataKind says where a fragment's data comes from.
type DataKind uint8

const (
	// DataNone: the fragment renders with no data.
	DataNone DataKind = iota
	// DataFixed: a value fixed when the program starts.
	DataFixed
	// DataFetched: a handler run for every render.
	DataFetched
	// DataEffect: a handler run for what it declares; the template gets no data.
	DataEffect
)

// DataSource is what a fragment renders with, and the Go type its template sees
// as dot. Only pkg/collage's constructors make one for an application, so the
// type always matches the data: it is the handler's or the value's own static
// type, nil when that type is an interface and so unknown until it renders.
type DataSource struct {
	Kind DataKind
	// Handler fetches the data for DataFetched, and runs for DataEffect.
	Handler DataHandlerFunc
	// Value is the data for DataFixed.
	Value any // any: fixed data flows straight into html/template, whose parameter is any
	// Type is the template's dot for DataFixed and DataFetched; nil when unknown.
	Type reflect.Type
}

// FixedData is a source rendering v, whose static type is t.
func FixedData(v any, t reflect.Type) DataSource { // any: as DataSource.Value
	return DataSource{Kind: DataFixed, Value: v, Type: t}
}

// FetchedData is a source running h for every render, its data of type t. A nil
// h is no data.
func FetchedData(h DataHandlerFunc, t reflect.Type) DataSource {
	if h == nil {
		return DataSource{}
	}
	return DataSource{Kind: DataFetched, Handler: h, Type: t}
}

// EffectData is a source running h for what it declares, giving the template no
// data. A nil h is no data.
func EffectData(h DataHandlerFunc) DataSource {
	if h == nil {
		return DataSource{}
	}
	return DataSource{Kind: DataEffect, Handler: h}
}

// DataSource returns what f renders with. It is nil-safe.
func (f *Fragment) DataSource() DataSource {
	if f == nil {
		return DataSource{}
	}
	return f.data
}

// SetDataSource sets what f renders with.
func (f *Fragment) SetDataSource(d DataSource) { f.data = d }
