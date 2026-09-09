package music

import "testing"

func TestNewImportWorkerMetadataEnricherPrefersDiscogs(t *testing.T) {
	t.Setenv("MUSICBRAINZ_USER_AGENT", "Atoman/test")
	t.Setenv("DISCOGS_CONSUMER_KEY", "consumer-key")
	t.Setenv("DISCOGS_CONSUMER_SECRET", "consumer-secret")
	t.Setenv("DISCOGS_BASE_URL", "https://discogs.example")

	enricher := newImportWorkerMetadataEnricher()
	if enricher == nil {
		t.Fatal("expected metadata enricher")
	}
	if !enricher.preferDiscogs {
		t.Fatal("import worker metadata enricher must prefer Discogs")
	}
	if enricher.discogsBase != "https://discogs.example" {
		t.Fatalf("Discogs base URL = %q", enricher.discogsBase)
	}
}
