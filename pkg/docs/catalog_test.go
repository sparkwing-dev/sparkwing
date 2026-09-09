package docs

import (
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestListCallerMutationIsIsolated(t *testing.T) {
	want := List()
	if len(want) < 2 {
		t.Fatal("embedded catalog must contain multiple documents")
	}
	if !slices.IsSortedFunc(want, func(a, b Entry) int { return strings.Compare(a.Slug, b.Slug) }) {
		t.Fatal("catalog is not sorted by slug")
	}
	got := List()
	got[0] = Entry{Slug: "caller-owned"}
	slices.Reverse(got)
	if !slices.Equal(List(), want) {
		t.Fatal("mutating a returned catalog changed subsequent results")
	}
}

func TestCatalogConcurrentReaders(t *testing.T) {
	want := List()
	hits := SearchSections("cache")
	body, err := Read("pipelines")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 3 {
				own := List()
				if !slices.Equal(own, want) {
					t.Error("concurrent list changed")
				}
				own[0] = Entry{Slug: "caller-owned"}
				got, err := Read("pipelines")
				if err != nil || got != body {
					t.Errorf("concurrent read changed: %v", err)
				}
				if !slices.Equal(SearchSections("cache"), hits) {
					t.Error("concurrent search order or body changed")
				}
			}
		})
	}
	workers.Wait()
}

func TestSearchSectionsAvoidsRepeatedCatalogAllocation(t *testing.T) {
	allocations := testing.AllocsPerRun(1, func() { SearchSections("cache") })
	if allocations > 50000 {
		t.Fatalf("search allocates %.0f objects; repeated catalog construction exceeds the 50000-object budget", allocations)
	}
}

func BenchmarkSearchSections(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		SearchSections("cache")
	}
}

func BenchmarkList(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		List()
	}
}
