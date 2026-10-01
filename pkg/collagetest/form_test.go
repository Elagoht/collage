package collagetest

import (
	"reflect"
	"testing"
)

func TestScan(t *testing.T) {
	tests := []struct {
		name  string
		page  string
		forms []form
		loose []field
	}{
		{
			name:  "attribute forms",
			page:  `<FORM Action='/kayıt' METHOD=post enctype="Multipart/Form-Data"><input type=HIDDEN name=a value=1><input type="hidden" name='b' value="ş&amp;ğ"><input type="hidden" name="c"></form>`,
			forms: []form{{action: "/kayıt", method: "post", enctype: "multipart/form-data", hidden: []field{{"a", "1"}, {"b", "ş&ğ"}, {"c", ""}}}},
			loose: []field{{"a", "1"}, {"b", "ş&ğ"}, {"c", ""}},
		},
		{
			name:  "only hidden and enabled inputs are carried",
			page:  `<form><input name="visible" value="x"><input type="hidden" name="off" value="y" disabled><input type="hidden" value="no name"><input type="hidden" name="İ" value="ok"/></form>`,
			forms: []form{{hidden: []field{{"İ", "ok"}}}},
			loose: []field{{"İ", "ok"}},
		},
		{
			name:  "comments and raw text hide what looks like a form",
			page:  `<!-- <form action="/no"> --><script>const s = '<form action="/no"><input type="hidden" name="x">';</script><style>form{}</style><textarea><form></textarea><form action="/yes"></form>`,
			forms: []form{{action: "/yes"}},
		},
		{
			name:  "the first of a repeated attribute wins, and > inside quotes does not end the tag",
			page:  `<form action="/a>b" action="/c"><input type="hidden" name="x" name="y" value="1"></form>`,
			forms: []form{{action: "/a>b", hidden: []field{{"x", "1"}}}},
			loose: []field{{"x", "1"}},
		},
		{
			name:  "a hidden input outside every form is loose only; an unclosed form runs to the end",
			page:  `<input type="hidden" name="_csrf" value="t"><form action="/1"><form action="/2"><input type="hidden" name="z" value="2">`,
			forms: []form{{action: "/1"}, {action: "/2", hidden: []field{{"z", "2"}}}},
			loose: []field{{"_csrf", "t"}, {"z", "2"}},
		},
		{
			name:  "text that is not a tag",
			page:  `<!DOCTYPE html><p>1 < 2 and a<-b</p><form action="/x"></form>`,
			forms: []form{{action: "/x"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			forms, loose := scan(tt.page)
			if !reflect.DeepEqual(forms, tt.forms) {
				t.Errorf("forms = %+v, want %+v", forms, tt.forms)
			}
			if !reflect.DeepEqual(loose, tt.loose) {
				t.Errorf("hidden = %+v, want %+v", loose, tt.loose)
			}
		})
	}
}
