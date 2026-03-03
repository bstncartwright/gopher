package queryexpansion

import "testing"

func TestExpandFiltersStopWordsAcrossLanguages(t *testing.T) {
	terms := Expand("what did we do ayer y que hoy", 20)
	if len(terms) == 0 {
		t.Fatalf("expected expanded terms")
	}
	for _, disallowed := range []string{"what", "y", "que"} {
		for _, term := range terms {
			if term == disallowed {
				t.Fatalf("term %q should be filtered as a stopword", disallowed)
			}
		}
	}
}

func TestBuildFTSQueryUsesQuotedOrTerms(t *testing.T) {
	query := BuildFTSQuery("deploy migration checks")
	if query == "" {
		t.Fatalf("expected non-empty query")
	}
	if query != `"checks" OR "deploy" OR "migration"` {
		t.Fatalf("unexpected fts query: %q", query)
	}
}
