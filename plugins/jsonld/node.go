package jsonld

import (
	"encoding/json"
	"time"
)

// Node is one piece of structured data, ready to be emitted.
//
// It is an interface over concrete types rather than a map, and that is the whole
// design decision in this file. JSON-LD is open-ended, so the obvious shape is
// map[string]any — and then nothing checks a misspelled property, the compiler has
// nothing to say about a missing one, and every application invents its own spelling
// of the same vocabulary. The types below cover what a content site actually emits;
// Raw is the escape hatch for everything else, and it makes the escape explicit
// rather than making it the default.
type Node interface {
	json.Marshaler
	// Type is the schema.org type this node declares, used in error messages and
	// to keep the interface from being satisfied by accident.
	Type() string
}

// schemaContext is the JSON-LD context every node declares. It is not named
// "context" because this package also imports Go's.
const schemaContext = "https://schema.org"

// Raw is structured data the application marshalled itself, for a schema.org type
// this package does not model.
//
// It is checked for being valid JSON when it is marshalled, because invalid JSON
// inside a <script> tag is worse than absent JSON: a consumer that fails to parse it
// may discard every other node on the page along with it.
type Raw struct {
	// SchemaType is what the data declares as its "@type", for diagnostics.
	SchemaType string
	// JSON is the node, already marshalled, without the "@context" property —
	// which MarshalJSON adds, so every node on a page declares it the same way.
	JSON json.RawMessage
}

func (r Raw) Type() string { return r.SchemaType }

func (r Raw) MarshalJSON() ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(r.JSON, &probe); err != nil {
		return nil, err
	}
	probe["@context"] = json.RawMessage(`"` + schemaContext + `"`)
	return json.Marshal(probe)
}

// Article is a piece of writing: schema.org/Article.
type Article struct {
	Headline      string
	Description   string
	URL           string
	Section       string
	Keywords      []string
	DatePublished time.Time
	DateModified  time.Time
	AuthorName    string
	AuthorURL     string
	PublisherName string
	PublisherLogo string
	ImageURL      string
	WordCount     int
}

func (Article) Type() string { return "Article" }

func (a Article) MarshalJSON() ([]byte, error) {
	out := articleJSON{
		Context:     schemaContext,
		SchemaType:  "Article",
		Headline:    a.Headline,
		Description: a.Description,
		URL:         a.URL,
		Section:     a.Section,
		Keywords:    a.Keywords,
		WordCount:   a.WordCount,
	}
	if !a.DatePublished.IsZero() {
		out.DatePublished = a.DatePublished.Format(time.RFC3339)
	}
	if !a.DateModified.IsZero() {
		out.DateModified = a.DateModified.Format(time.RFC3339)
	}
	if a.AuthorName != "" {
		out.Author = &person{SchemaType: "Person", Name: a.AuthorName, URL: a.AuthorURL}
	}
	if a.PublisherName != "" {
		out.Publisher = &organization{SchemaType: "Organization", Name: a.PublisherName}
		if a.PublisherLogo != "" {
			out.Publisher.Logo = &imageObject{SchemaType: "ImageObject", URL: a.PublisherLogo}
		}
	}
	if a.ImageURL != "" {
		out.Image = &imageObject{SchemaType: "ImageObject", URL: a.ImageURL}
	}
	return json.Marshal(out)
}

// WebSite describes the site itself: schema.org/WebSite.
type WebSite struct {
	Name string
	URL  string
	// SearchURL is a search endpoint with "{query}" where the term goes. Setting
	// it emits a SearchAction, which is what a search box in a result listing is
	// built from.
	SearchURL string
}

func (WebSite) Type() string { return "WebSite" }

func (w WebSite) MarshalJSON() ([]byte, error) {
	out := webSiteJSON{Context: schemaContext, SchemaType: "WebSite", Name: w.Name, URL: w.URL}
	if w.SearchURL != "" {
		out.PotentialAction = &searchAction{
			SchemaType: "SearchAction",
			Target:     w.SearchURL,
			QueryInput: "required name=query",
		}
	}
	return json.Marshal(out)
}

// Breadcrumb is one step in a trail.
type Breadcrumb struct {
	Name string
	URL  string
}

// BreadcrumbList is the trail to the current page: schema.org/BreadcrumbList.
type BreadcrumbList struct {
	Items []Breadcrumb
}

func (BreadcrumbList) Type() string { return "BreadcrumbList" }

func (b BreadcrumbList) MarshalJSON() ([]byte, error) {
	items := make([]listItem, 0, len(b.Items))
	for i, crumb := range b.Items {
		items = append(items, listItem{
			SchemaType: "ListItem",
			Position:   i + 1, // schema.org positions are 1-based
			Name:       crumb.Name,
			Item:       crumb.URL,
		})
	}
	return json.Marshal(breadcrumbJSON{
		Context:         schemaContext,
		SchemaType:      "BreadcrumbList",
		ItemListElement: items,
	})
}

// The wire shapes. They are separate from the exported types so the exported ones
// can use Go's vocabulary — time.Time, a slice of Breadcrumb — while what goes over
// the wire uses schema.org's.

type articleJSON struct {
	Context       string        `json:"@context"`
	SchemaType    string        `json:"@type"`
	Headline      string        `json:"headline,omitempty"`
	Description   string        `json:"description,omitempty"`
	URL           string        `json:"url,omitempty"`
	Section       string        `json:"articleSection,omitempty"`
	Keywords      []string      `json:"keywords,omitempty"`
	DatePublished string        `json:"datePublished,omitempty"`
	DateModified  string        `json:"dateModified,omitempty"`
	WordCount     int           `json:"wordCount,omitempty"`
	Author        *person       `json:"author,omitempty"`
	Publisher     *organization `json:"publisher,omitempty"`
	Image         *imageObject  `json:"image,omitempty"`
}

type person struct {
	SchemaType string `json:"@type"`
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
}

type organization struct {
	SchemaType string       `json:"@type"`
	Name       string       `json:"name"`
	Logo       *imageObject `json:"logo,omitempty"`
}

type imageObject struct {
	SchemaType string `json:"@type"`
	URL        string `json:"url"`
}

type webSiteJSON struct {
	Context         string        `json:"@context"`
	SchemaType      string        `json:"@type"`
	Name            string        `json:"name"`
	URL             string        `json:"url,omitempty"`
	PotentialAction *searchAction `json:"potentialAction,omitempty"`
}

type searchAction struct {
	SchemaType string `json:"@type"`
	Target     string `json:"target"`
	QueryInput string `json:"query-input"`
}

type breadcrumbJSON struct {
	Context         string     `json:"@context"`
	SchemaType      string     `json:"@type"`
	ItemListElement []listItem `json:"itemListElement"`
}

type listItem struct {
	SchemaType string `json:"@type"`
	Position   int    `json:"position"`
	Name       string `json:"name"`
	Item       string `json:"item,omitempty"`
}
