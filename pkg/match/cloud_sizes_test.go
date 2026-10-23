package match

import "testing"

func TestCloudSizesUseLabelsAndPreserveSelectorTerms(t *testing.T) {
	for _, tc := range []struct {
		selector []string
		want     int64
	}{
		{nil, 2},
		{[]string{"sparkwing-4cpu"}, 4},
		{[]string{"sparkwing-8cpu,sparkwing-4cpu", "gpu"}, 4},
		{[]string{"sparkwing-4cpu", "sparkwing-8cpu"}, 0},
		{[]string{"sparkwing-128cpu"}, 0},
		{[]string{"sparkwing-4cpu,gpu"}, 4},
	} {
		got, err := CloudSizeForProfile(tc.selector, Profile{})
		if tc.want == 0 {
			if err == nil {
				t.Fatalf("%q unexpectedly selected %+v", tc.selector, got)
			}
			continue
		}
		if err != nil || got.CreditClass != tc.want {
			t.Fatalf("%q: %+v %v", tc.selector, got, err)
		}
		if got.Resources.Cores != float64(got.CreditClass) || got.Resources.MemoryBytes != CloudSizeMemoryBytes(got.CreditClass) {
			t.Fatalf("allocation differs from catalog: %+v", got)
		}
		p := Profile{Class: ClassCloud, Labels: []string{got.Label, "gpu"}}
		if !Evaluate(p, Demand{Selector: tc.selector}).OK() {
			t.Fatalf("selected size does not satisfy labels: %+v", got)
		}
	}
}

func TestLabelSizeDoesNotAdmitAnotherSize(t *testing.T) {
	p := Profile{Class: ClassCloud, Labels: []string{"sparkwing-8cpu"}}
	if Evaluate(p, Demand{Selector: []string{"sparkwing-4cpu"}}).OK() {
		t.Fatal("larger runner substituted for a required label")
	}
}

func TestCloudProfileUsesOnlyCatalogSizeLabels(t *testing.T) {
	p := Profile{Class: ClassCloud, Labels: []string{"sparkwing-16cpu", "sparkwing-future"}}
	if !Evaluate(p, Demand{Selector: []string{"sparkwing-16cpu"}}).OK() ||
		Evaluate(p, Demand{Selector: []string{"sparkwing-future"}}).OK() {
		t.Fatal("Cloud size labels differ from the catalog")
	}
}

func TestCloudSizeAlternativesUseClaimantLabels(t *testing.T) {
	size, err := CloudSizeForProfile([]string{"sparkwing-128cpu,os=linux"}, Profile{OS: "linux"})
	if err != nil || size.CreditClass != 2 {
		t.Fatalf("platform alternative: %+v %v", size, err)
	}
	size, err = CloudSizeForProfile([]string{"sparkwing-4cpu,gpu"}, Profile{Labels: []string{"gpu"}})
	if err != nil || size.CreditClass != 2 {
		t.Fatalf("label alternative: %+v %v", size, err)
	}
}
