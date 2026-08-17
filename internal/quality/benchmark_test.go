package quality

import (
	"fmt"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func BenchmarkGroupAndLabelFindings_1000Claims(b *testing.B) {
	authMgr := NewAuthorityManager()
	engine := NewCorroborationEngine(authMgr)

	domains := []string{
		"https://www.reuters.com/news/article-",
		"https://bloomberg.com/news/story-",
		"https://techcrunch.com/2026/tech-",
		"https://wsj.com/articles/finance-",
		"https://theverge.com/reports/gadget-",
		"https://github.com/releases/version-",
		"https://nytimes.com/business/market-",
		"https://cnbc.com/markets/stock-",
	}

	claimTemplates := []string{
		"Company reported Q%d revenue of $%dB",
		"In Q%d, company revenue grew to $%d billion",
		"The latest version of software package is v%d.%d",
		"CEO of enterprise group announced $%dM investment in AI",
		"Product pricing updated to $%d per user per month",
	}

	findings := make([]store.Finding, 1000)
	for i := 0; i < 1000; i++ {
		domainBase := domains[i%len(domains)]
		template := claimTemplates[i%len(claimTemplates)]
		quarter := (i % 4) + 1
		amount := (i % 50) + 10
		minor := i % 10

		claim := fmt.Sprintf(template, quarter, amount, minor)
		findings[i] = store.Finding{
			Claim:     claim,
			SourceURL: fmt.Sprintf("%s%d", domainBase, i),
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = engine.GroupAndLabelFindings(findings)
	}
}

func BenchmarkCacheToken_AllEntityTypes(b *testing.B) {
	entities := []struct {
		entity DetectedEntity
		claim  string
	}{
		{
			entity: DetectedEntity{Type: EntityVersion, Subject: "Kubernetes", RawMatch: "v1.30.0"},
			claim:  "Kubernetes version v1.30.0 was released",
		},
		{
			entity: DetectedEntity{Type: EntityYear, Subject: "Status", RawMatch: "2026"},
			claim:  "In 2026 software architecture evolved",
		},
		{
			entity: DetectedEntity{Type: EntityExecutive, Subject: "Apple", RawMatch: "CEO of Apple"},
			claim:  "Tim Cook is the CEO of Apple",
		},
		{
			entity: DetectedEntity{Type: EntityPrice, Subject: "Bitcoin", RawMatch: "$95,000"},
			claim:  "Bitcoin price reached $95,000 today",
		},
		{
			entity: DetectedEntity{Type: EntityUnknown, Subject: "", RawMatch: ""},
			claim:  "General declarative statement about natural sciences and astronomy",
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, e := range entities {
			_ = CacheToken(e.entity, e.claim)
		}
	}
}
