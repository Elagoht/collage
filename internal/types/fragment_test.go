package types

import (
	"errors"
	"testing"
	"time"
)

func TestFragment_Bind(t *testing.T) {
	newSlotted := func(allowMultiple bool) *Fragment {
		return &Fragment{
			Name:         "parent",
			TemplatePath: "parent.html",
			Slots: map[string]*SlotDefinition{
				"content": {Name: "content", AllowMultiple: allowMultiple},
			},
		}
	}
	child := &Fragment{Name: "child", TemplatePath: "child.html"}

	t.Run("success", func(t *testing.T) {
		f := newSlotted(false)
		if err := f.Bind("content", child); err != nil {
			t.Fatalf("Bind() error = %v, want nil", err)
		}
		fill := f.Slots["content"].Fill
		if len(fill) != 1 || fill[0] != child {
			t.Fatalf("Fill = %v, want [child]", fill)
		}
	})

	t.Run("unknown slot", func(t *testing.T) {
		f := newSlotted(false)
		err := f.Bind("missing", child)
		if !errors.Is(err, ErrUnknownSlot) {
			t.Fatalf("Bind() error = %v, want ErrUnknownSlot", err)
		}
	})

	t.Run("occupied slot", func(t *testing.T) {
		f := newSlotted(false)
		if err := f.Bind("content", child); err != nil {
			t.Fatalf("first bind: %v", err)
		}
		err := f.Bind("content", child)
		if !errors.Is(err, ErrSlotOccupied) {
			t.Fatalf("Bind() error = %v, want ErrSlotOccupied", err)
		}
	})

	t.Run("nil child", func(t *testing.T) {
		f := newSlotted(false)
		err := f.Bind("content", nil)
		if !errors.Is(err, ErrNilFragment) {
			t.Fatalf("Bind() error = %v, want ErrNilFragment", err)
		}
	})

	t.Run("multiple allowed", func(t *testing.T) {
		f := newSlotted(true)
		other := &Fragment{Name: "other", TemplatePath: "other.html"}
		if err := f.Bind("content", child); err != nil {
			t.Fatalf("first bind: %v", err)
		}
		if err := f.Bind("content", other); err != nil {
			t.Fatalf("second bind: %v", err)
		}
		fill := f.Slots["content"].Fill
		if len(fill) != 2 || fill[0] != child || fill[1] != other {
			t.Fatalf("Fill = %v, want [child other]", fill)
		}
	})
}

func TestFragment_Validate(t *testing.T) {
	t.Run("valid nested tree", func(t *testing.T) {
		parent := &Fragment{
			Name:         "parent",
			TemplatePath: "parent.html",
			Slots: map[string]*SlotDefinition{
				"content": {Name: "content", Required: true},
			},
		}
		leaf := &Fragment{Name: "leaf", TemplatePath: "leaf.html"}
		if err := parent.Bind("content", leaf); err != nil {
			t.Fatalf("bind: %v", err)
		}
		if err := parent.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
	})

	t.Run("nil receiver", func(t *testing.T) {
		var f *Fragment
		if err := f.Validate(); !errors.Is(err, ErrNilFragment) {
			t.Fatalf("Validate() error = %v, want ErrNilFragment", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		f := &Fragment{TemplatePath: "x.html"}
		if err := f.Validate(); !errors.Is(err, ErrEmptyName) {
			t.Fatalf("Validate() error = %v, want ErrEmptyName", err)
		}
	})

	t.Run("empty template path", func(t *testing.T) {
		f := &Fragment{Name: "x"}
		if err := f.Validate(); !errors.Is(err, ErrEmptyTemplatePath) {
			t.Fatalf("Validate() error = %v, want ErrEmptyTemplatePath", err)
		}
	})

	t.Run("required slot unfilled", func(t *testing.T) {
		f := &Fragment{
			Name:         "parent",
			TemplatePath: "parent.html",
			Slots: map[string]*SlotDefinition{
				"content": {Name: "content", Required: true},
			},
		}
		if err := f.Validate(); !errors.Is(err, ErrRequiredSlotUnfilled) {
			t.Fatalf("Validate() error = %v, want ErrRequiredSlotUnfilled", err)
		}
	})

	t.Run("negative timeout", func(t *testing.T) {
		f := &Fragment{Name: "x", TemplatePath: "x.html", Timeout: -time.Second}
		if err := f.Validate(); !errors.Is(err, ErrInvalidTimeout) {
			t.Fatalf("Validate() error = %v, want ErrInvalidTimeout", err)
		}
	})

	t.Run("empty slot name", func(t *testing.T) {
		f := &Fragment{
			Name:         "parent",
			TemplatePath: "parent.html",
			Slots: map[string]*SlotDefinition{
				"content": {Name: ""},
			},
		}
		if err := f.Validate(); !errors.Is(err, ErrInvalidSlotDefinition) {
			t.Fatalf("Validate() error = %v, want ErrInvalidSlotDefinition", err)
		}
	})

	t.Run("slot key does not match slot name", func(t *testing.T) {
		f := &Fragment{
			Name:         "parent",
			TemplatePath: "parent.html",
			Slots: map[string]*SlotDefinition{
				"content": {Name: "other"},
			},
		}
		if err := f.Validate(); !errors.Is(err, ErrInvalidSlotDefinition) {
			t.Fatalf("Validate() error = %v, want ErrInvalidSlotDefinition", err)
		}
	})

	t.Run("invalid child propagates", func(t *testing.T) {
		parent := &Fragment{
			Name:         "parent",
			TemplatePath: "parent.html",
			Slots: map[string]*SlotDefinition{
				"content": {Name: "content", AllowMultiple: true},
			},
		}
		badChild := &Fragment{TemplatePath: "bad.html"} // empty name
		if err := parent.Bind("content", badChild); err != nil {
			t.Fatalf("bind: %v", err)
		}
		if err := parent.Validate(); !errors.Is(err, ErrEmptyName) {
			t.Fatalf("Validate() error = %v, want ErrEmptyName", err)
		}
	})

	t.Run("invalid fallback propagates", func(t *testing.T) {
		parent := &Fragment{
			Name:         "parent",
			TemplatePath: "parent.html",
			Fallback:     &Fragment{TemplatePath: "bad.html"}, // empty name
		}
		if err := parent.Validate(); !errors.Is(err, ErrEmptyName) {
			t.Fatalf("Validate() error = %v, want ErrEmptyName", err)
		}
	})
}

// TestFragment_Validate_CycleDetected binds A -> B -> A and asserts Validate returns
// ErrFragmentCycle instead of recursing forever. The test completing at all is part
// of what it proves: a broken cycle guard would hang until the test binary's timeout.
func TestFragment_Validate_CycleDetected(t *testing.T) {
	a := &Fragment{Name: "a", TemplatePath: "a.html", Slots: map[string]*SlotDefinition{
		"child": {Name: "child", AllowMultiple: true},
	}}
	b := &Fragment{Name: "b", TemplatePath: "b.html", Slots: map[string]*SlotDefinition{
		"child": {Name: "child", AllowMultiple: true},
	}}
	if err := a.Bind("child", b); err != nil {
		t.Fatalf("bind a->b: %v", err)
	}
	if err := b.Bind("child", a); err != nil {
		t.Fatalf("bind b->a: %v", err)
	}

	err := a.Validate()
	if !errors.Is(err, ErrFragmentCycle) {
		t.Fatalf("Validate() error = %v, want ErrFragmentCycle", err)
	}
}

// TestFragment_Validate_DiamondIsNotACycle binds the same child fragment into two
// different slots of the same parent. This is a legal diamond, not a cycle: the
// recursion-stack cycle guard must not flag revisiting a fragment once its own
// subtree has already finished validating.
func TestFragment_Validate_DiamondIsNotACycle(t *testing.T) {
	shared := &Fragment{Name: "shared", TemplatePath: "shared.html"}
	parent := &Fragment{
		Name:         "parent",
		TemplatePath: "parent.html",
		Slots: map[string]*SlotDefinition{
			"left":  {Name: "left", AllowMultiple: true},
			"right": {Name: "right", AllowMultiple: true},
		},
	}
	if err := parent.Bind("left", shared); err != nil {
		t.Fatalf("bind left: %v", err)
	}
	if err := parent.Bind("right", shared); err != nil {
		t.Fatalf("bind right: %v", err)
	}

	if err := parent.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a legal diamond", err)
	}
}

func TestFragment_EffectiveTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		def     time.Duration
		want    time.Duration
	}{
		{"uses own positive timeout", 5 * time.Second, 10 * time.Second, 5 * time.Second},
		{"falls back to default when zero", 0, 10 * time.Second, 10 * time.Second},
		{"falls back to default when negative", -time.Second, 10 * time.Second, 10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Fragment{Timeout: tt.timeout}
			if got := f.EffectiveTimeout(tt.def); got != tt.want {
				t.Errorf("EffectiveTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFragment_SlotNames(t *testing.T) {
	f := &Fragment{
		Slots: map[string]*SlotDefinition{
			"b": {Name: "b"},
			"a": {Name: "a"},
			"c": {Name: "c"},
		},
	}
	got := f.SlotNames()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("SlotNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SlotNames() = %v, want %v", got, want)
		}
	}
}
