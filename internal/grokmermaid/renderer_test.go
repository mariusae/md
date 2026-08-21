package grokmermaid

import (
	"strings"
	"testing"
)

func TestRenderFlowchart(t *testing.T) {
	art, err := Render("graph TD\n  A[Start] --> B[End]", 80)
	if err != nil {
		t.Fatal(err)
	}
	plain := art.Plain()
	if !strings.Contains(plain, "Start") || !strings.Contains(plain, "End") {
		t.Fatalf("node labels missing:\n%s", plain)
	}
	if !strings.Contains(plain, "▼") {
		t.Fatalf("arrowhead missing:\n%s", plain)
	}
	if !hasClass(art, ClassBorder) || !hasClass(art, ClassNodeText) || !hasClass(art, ClassEdge) {
		t.Fatalf("semantic styles missing: %#v", art)
	}
}

func TestRenderSequenceAndFallback(t *testing.T) {
	sequence, err := Render("sequenceDiagram\n  Alice->>Bob: Hello", 100)
	if err != nil {
		t.Fatal(err)
	}
	if plain := sequence.Plain(); !strings.Contains(plain, "Alice") || !strings.Contains(plain, "Hello") {
		t.Fatalf("sequence diagram missing content:\n%s", plain)
	}

	fallback, err := Render("pie title Pets\n  \"Dogs\" : 386", 80)
	if err != nil {
		t.Fatal(err)
	}
	if plain := fallback.Plain(); !strings.Contains(plain, "mermaid: pie") || !strings.Contains(plain, `"Dogs" : 386`) {
		t.Fatalf("unsupported diagram did not use source fallback:\n%s", plain)
	}
}

func TestRenderSupportedDiagramFamilies(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "state",
			source: "stateDiagram-v2\n  [*] --> Idle\n  Idle --> Ready : load",
			want:   []string{"Idle", "Ready", "load"},
		},
		{
			name:   "class",
			source: "classDiagram\n  class Animal {\n    +String name\n  }\n  Animal <|-- Dog",
			want:   []string{"Animal", "String name", "Dog"},
		},
		{
			name:   "entity relationship",
			source: "erDiagram\n  CUSTOMER ||--o{ ORDER : places",
			want:   []string{"CUSTOMER", "ORDER", "places"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			art, err := Render(tt.source, 100)
			if err != nil {
				t.Fatal(err)
			}
			plain := art.Plain()
			for _, want := range tt.want {
				if !strings.Contains(plain, want) {
					t.Fatalf("render missing %q:\n%s", want, plain)
				}
			}
		})
	}
}

func TestRenderBlank(t *testing.T) {
	art, err := Render("  \n ", 80)
	if err != nil {
		t.Fatal(err)
	}
	if art != nil {
		t.Fatalf("blank input rendered as %#v", art)
	}
}

func TestRenderReturnsIndependentCachedArt(t *testing.T) {
	source := "graph LR\n  A --> B"
	first, err := Render(source, 80)
	if err != nil {
		t.Fatal(err)
	}
	first.Lines[0].Spans[0].Text = "changed"
	second, err := Render(source, 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(second.Plain(), "changed") {
		t.Fatal("cached render was mutated by its caller")
	}
}

func hasClass(art *Art, class Class) bool {
	for _, line := range art.Lines {
		for _, span := range line.Spans {
			if span.Class == class {
				return true
			}
		}
	}
	return false
}
