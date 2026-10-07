package capacity

import "testing"

func TestPNGResourcesAreFiniteAndBounded(t *testing.T) {
	for key, maximum := range pngResourceMaximums {
		p := Defaults()
		p.Resources[key] = Limited(maximum)
		if err := p.Validate(); err != nil {
			t.Fatalf("%s upper bound: %v", key, err)
		}
		p.Resources[key] = Limited(maximum + 1)
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted excessive %s", key)
		}
		p.Resources[key] = Choice{Mode: "unlimited"}
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted unlimited %s", key)
		}
		p.Resources[key] = Limited(1)
		if err := p.Validate(); err != nil {
			t.Fatalf("%s small finite policy: %v", key, err)
		}
	}
}
