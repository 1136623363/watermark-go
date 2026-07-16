package parser

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

type noIOParser struct{}

func (noIOParser) Parse(context.Context, Request) (Result, error) { return Result{}, nil }

type countingDependencies struct{ calls atomic.Int32 }

func TestParserConstructionPerformsNoIO(t *testing.T) {
	t.Parallel()
	dependencies := &countingDependencies{}
	descriptor := Descriptor{
		Key: "example",
		New: func(Dependencies) (Parser, error) {
			return noIOParser{}, nil
		},
	}
	constructed, err := descriptor.New(Dependencies{Probe: func() { dependencies.calls.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	if constructed == nil || dependencies.calls.Load() != 0 {
		t.Fatalf("constructor performed I/O probe: %d", dependencies.calls.Load())
	}
}

func TestRegistryRejectsAmbiguousDescriptorMetadata(t *testing.T) {
	t.Parallel()
	_, err := NewRegistry([]Descriptor{
		validRegistryTestDescriptor("first", "video-one.example", "shared"),
		validRegistryTestDescriptor("second", "video-two.example", "shared"),
	})
	if err == nil {
		t.Fatal("ambiguous descriptors were accepted")
	}
}

func TestRegistryRejectsDuplicateExactHostWithinDescriptor(t *testing.T) {
	t.Parallel()
	_, err := NewRegistry([]Descriptor{{
		Key: "duplicate", DisplayName: "duplicate", Capabilities: CapabilityVideo,
		HostRules: []HostRule{
			{Host: "Media.Example.", IncludeSubdomains: true},
			{Host: "media.example", IncludeSubdomains: true},
		},
		MaxRequests: 1, New: func(Dependencies) (Parser, error) { return noIOParser{}, nil },
	}})
	if err == nil {
		t.Fatal("duplicate exact host rule within one descriptor was accepted")
	}
}

func TestRegistryConstructionDoesNotMutateDescriptorInput(t *testing.T) {
	t.Parallel()
	descriptors := []Descriptor{{
		Key: "example", DisplayName: "example", HostRules: []HostRule{{Host: "Media.Example."}},
		Capabilities: CapabilityVideo, MaxRequests: 1,
		QueryKeys: []string{"VID", "id"}, New: func(Dependencies) (Parser, error) { return noIOParser{}, nil },
	}}
	_, err := NewRegistry(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	if descriptors[0].HostRules[0].Host != "Media.Example." || descriptors[0].QueryKeys[0] != "VID" {
		t.Fatal("registry mutated caller-owned descriptor metadata")
	}
}

func TestRegistryRejectsUnknownHostWithTypedError(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry([]Descriptor{{
		Key: "known", DisplayName: "known", Capabilities: CapabilityVideo, MaxRequests: 1,
		HostRules: []HostRule{{Host: "known.example", IncludeSubdomains: true}},
		New:       func(Dependencies) (Parser, error) { return noIOParser{}, nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.ResolveURL("https://known.example.attacker.invalid/watch")
	var unknown *UnknownHostError
	if !errors.As(err, &unknown) || unknown.Host != "known.example.attacker.invalid" {
		t.Fatalf("unknown-host error = %#v", err)
	}
	if _, err := registry.ResolveURL("ftp://known.example/watch"); err == nil {
		t.Fatal("non-HTTP parser URL was accepted")
	}
}

func TestRegistryRejectsInvalidRequiredMetadataIndependently(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Descriptor)
	}{
		{name: "display-name", mutate: func(value *Descriptor) { value.DisplayName = "" }},
		{name: "capabilities", mutate: func(value *Descriptor) { value.Capabilities = 0 }},
		{name: "unknown-capability", mutate: func(value *Descriptor) { value.Capabilities = 1 << 31 }},
		{name: "priority", mutate: func(value *Descriptor) { value.Priority = -1 }},
		{name: "request-budget", mutate: func(value *Descriptor) { value.MaxRequests = 0 }},
		{name: "redirect-budget", mutate: func(value *Descriptor) { value.MaxRedirects = -1 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			descriptor := validRegistryTestDescriptor("fixture", "fixture.example")
			test.mutate(&descriptor)
			if _, err := NewRegistry([]Descriptor{descriptor}); err == nil {
				t.Fatal("registry accepted invalid required descriptor metadata")
			}
		})
	}
}

func validRegistryTestDescriptor(key PlatformKey, host string, aliases ...PlatformKey) Descriptor {
	return Descriptor{
		Key: key, DisplayName: string(key), Aliases: aliases,
		HostRules: []HostRule{{Host: host}}, Capabilities: CapabilityVideo,
		MaxRequests: 1, MaxRedirects: 0,
		New: func(Dependencies) (Parser, error) { return noIOParser{}, nil },
	}
}

func TestSnapshotHonorsDescriptorRequestBudgetAndRejectsDuplicateURL(t *testing.T) {
	t.Parallel()
	budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 3, MaxRedirects: 2, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := netguard.NewFetchURL("https://media.example/one?id=opaque")
	second, _ := netguard.NewFetchURL("https://media.example/two")
	if err := budget.AllowFetch(first); err != nil {
		t.Fatal(err)
	}
	if err := budget.AllowFetch(second); err != nil {
		t.Fatal(err)
	}
	if err := budget.AllowFetch(first); !errors.Is(err, ErrDuplicateFetch) {
		t.Fatalf("duplicate fetch error = %v", err)
	}
	if err := budget.AllowRedirect(); err != nil {
		t.Fatal(err)
	}
	if err := budget.AllowRedirect(); err != nil {
		t.Fatal(err)
	}
	if err := budget.AllowRedirect(); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("redirect budget error = %v", err)
	}
}

func TestRequestBudgetBindsContextsToOneSharedDeadline(t *testing.T) {
	t.Parallel()
	budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 2, MaxRedirects: 1, Duration: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	first, cancelFirst, err := budget.BindContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cancelFirst()
	firstDeadline, ok := first.Deadline()
	if !ok {
		t.Fatal("first budget context has no deadline")
	}
	time.Sleep(25 * time.Millisecond)
	second, cancelSecond, err := budget.BindContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cancelSecond()
	secondDeadline, ok := second.Deadline()
	if !ok {
		t.Fatal("second budget context has no deadline")
	}
	delta := secondDeadline.Sub(firstDeadline)
	if delta < -10*time.Millisecond || delta > 10*time.Millisecond {
		t.Fatalf("budget contexts did not share a deadline: first=%s second=%s delta=%s", firstDeadline, secondDeadline, delta)
	}
}

func TestCandidateFallbackHonorsTotalBudget(t *testing.T) {
	t.Parallel()
	budget, err := NewRequestBudget(BudgetOptions{MaxRequests: 2, MaxRedirects: 1, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	candidates := []MediaCandidate{
		{URL: "https://media.example/high", Quality: 1080},
		{URL: "https://media.example/medium", Quality: 720},
		{URL: "https://media.example/low", Quality: 480},
	}
	attempts := 0
	_, err = AttemptMediaCandidates(t.Context(), candidates, budget, func(context.Context, MediaCandidate, netguard.FetchURL) error {
		attempts++
		return errors.New("synthetic candidate failure")
	})
	if !errors.Is(err, ErrBudgetExceeded) || attempts != 2 {
		t.Fatalf("candidate fallback escaped total budget: attempts=%d error=%v", attempts, err)
	}
}

func TestDescriptorQueryPolicyNormalizesAndStripsTrackingDeterministically(t *testing.T) {
	t.Parallel()
	descriptor := Descriptor{
		HostRules: []HostRule{{Host: "media.example", IncludeSubdomains: true}},
		QueryKeys: []string{"vid", "id", "xsec_token", "modal_id", "v", "s", "pid"},
	}
	raw := "https://media.example/watch?UTM_source=tracking&VID=b&VID=a&vid=a&%76id=b&id=&xsec_token=a%2Fb&modal_id=42&v=1&s=20&pid=7#fragment"
	got, err := normalizeAllowedQuery(descriptor, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://media.example/watch?modal_id=42&pid=7&s=20&v=1&vid=a&vid=b&xsec_token=a%2Fb"
	if got != want {
		t.Fatal("normalized query policy drifted")
	}
	if _, err := normalizeAllowedQuery(descriptor, "https://media.example/watch?vid=%zz"); err == nil {
		t.Fatal("invalid percent encoding was accepted")
	}
}
