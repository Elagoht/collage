package types

import "testing"

// A layout chain entry's builder error must reach PageBuildErr even though
// LayoutFragment is still nil before registration folds the chain.
func TestPageBuildErrWalksLayoutChain(t *testing.T) {
	bad := &Fragment{Name: "bad", TemplatePath: "x.html"}
	bad.setBuildErr(ErrNilFragment)
	p := &Page{
		Name:            "p",
		ContentFragment: &Fragment{Name: "c", TemplatePath: "c.html"},
		LayoutChain:     []*Fragment{&Fragment{Name: "ok", TemplatePath: "o.html"}, bad},
	}
	if err := PageBuildErr(p); err == nil {
		t.Fatal("PageBuildErr missed the chain entry's build error")
	}
}
