package uibrand

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"reflect"
	"regexp"
	"testing"
)

func TestPalette(t *testing.T) {
	var source map[string]map[string]string
	if err := json.Unmarshal(paletteJSON, &source); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		raw, _ := json.Marshal(Palette(dark))
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, source[name]) {
			t.Fatal("palette must expose every shipped token")
		}
		for key, value := range got {
			if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(value) {
				t.Fatalf("%s: invalid color %s", key, value)
			}
		}
	}
}

func checkSVG(t *testing.T, data []byte) {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestArtwork(t *testing.T) {
	for name, path := range icons {
		svg, ok := IconSVG(name)
		if !ok || !bytes.Contains(svg, []byte(path)) {
			t.Fatalf("missing icon %s", name)
		}
		if !bytes.Contains(svg, []byte(`stroke-width="1.7"`)) {
			t.Fatal("icon stroke drift")
		}
		checkSVG(t, svg)
	}
	if _, ok := IconSVG("unknown"); ok {
		t.Fatal("unknown icon accepted")
	}
	checkSVG(t, MarkSVG())
	checkSVG(t, MarkOpaqueSVG())
	if !bytes.Contains(MarkSVG(), []byte(`opacity="0.6"`)) || bytes.Contains(MarkOpaqueSVG(), []byte("opacity")) {
		t.Fatal("mark opacity variants differ")
	}
	copy := MarkSVG()
	copy[0] = 'x'
	if MarkSVG()[0] != '<' {
		t.Fatal("caller mutated shared mark")
	}
}

func TestMarkInheritsCallerColor(t *testing.T) {
	var favicon struct {
		XMLName xml.Name `xml:"svg"`
		Color   string   `xml:"color,attr"`
		Fill    string   `xml:"fill,attr"`
	}
	if err := xml.Unmarshal(markSVG, &favicon); err != nil {
		t.Fatal(err)
	}
	if favicon.Color != Palette(false).Primary || favicon.Fill != "currentColor" {
		t.Fatal("standalone favicon must use the light primary fallback with currentColor fill")
	}
	for _, svg := range [][]byte{MarkSVG(), MarkOpaqueSVG()} {
		decoder := xml.NewDecoder(bytes.NewReader(svg))
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if element, ok := token.(xml.StartElement); ok {
				for _, attr := range element.Attr {
					if attr.Name.Local == "color" || attr.Name.Local == "style" {
						t.Fatal("native marks must not override inherited currentColor")
					}
					if attr.Name.Local == "fill" && attr.Value != "currentColor" {
						t.Fatal("native mark fill must inherit currentColor")
					}
				}
			}
		}
		if !bytes.Contains(svg, []byte(`fill="currentColor"`)) {
			t.Fatal("missing inherited fill")
		}
	}
}
