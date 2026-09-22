package types

import "testing"

func TestRenderStrategy_String(t *testing.T) {
	tests := []struct {
		name string
		s    RenderStrategy
		want string
	}{
		{"dynamic", StrategyDynamic, "dynamic"},
		{"static", StrategyStatic, "static"},
		{"incremental", StrategyIncremental, "incremental"},
		{"out of range positive", RenderStrategy(99), "unknown(99)"},
		{"out of range negative", RenderStrategy(-1), "unknown(-1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderStrategy_Cacheable(t *testing.T) {
	tests := []struct {
		name string
		s    RenderStrategy
		want bool
	}{
		{"dynamic is not cacheable", StrategyDynamic, false},
		{"static is cacheable", StrategyStatic, true},
		{"incremental is cacheable", StrategyIncremental, true},
		{"out of range is not cacheable", RenderStrategy(42), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.Cacheable(); got != tt.want {
				t.Errorf("Cacheable() = %v, want %v", got, tt.want)
			}
		})
	}
}
