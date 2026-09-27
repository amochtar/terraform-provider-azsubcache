package provider

import (
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/managementgroups/armmanagementgroups"
)

func sub(name, state string) *armmanagementgroups.SubscriptionUnderManagementGroup {
	s := &armmanagementgroups.SubscriptionUnderManagementGroup{Name: &name}
	if state != "" {
		s.Properties = &armmanagementgroups.SubscriptionUnderManagementGroupProperties{State: &state}
	}
	return s
}

func TestPoolCandidates(t *testing.T) {
	got := poolCandidates([]*armmanagementgroups.SubscriptionUnderManagementGroup{
		sub("a", "Enabled"),
		sub("b", "Disabled"), // cancelled leftover, must be skipped
		sub("c", ""),         // no properties, assume usable
		nil,
		sub("", "Enabled"),
	})
	want := []string{"a", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestClaimFirst(t *testing.T) {
	// Second candidate wins when another apply already holds the first.
	var tried []string
	got, err := claimFirst([]string{"a", "b", "c"}, func(id string) (bool, error) {
		tried = append(tried, id)
		return id == "b", nil
	})
	if err != nil || got != "b" {
		t.Fatalf("got %q, %v; want b, nil", got, err)
	}
	if len(tried) != 2 {
		t.Fatalf("kept going after winning: tried %v", tried)
	}

	// Everything already claimed: empty, not an error, so Create falls back.
	got, err = claimFirst([]string{"a", "b"}, func(string) (bool, error) { return false, nil })
	if err != nil || got != "" {
		t.Fatalf("got %q, %v; want \"\", nil", got, err)
	}

	// A real failure must not silently look like an empty pool.
	if _, err := claimFirst([]string{"a"}, func(string) (bool, error) {
		return false, errors.New("boom")
	}); err == nil {
		t.Fatal("want error")
	}
}
