package api

import (
	"testing"

	"github.com/Resinat/Resin/internal/service"
)

func TestSummarizeSubscriptions_OnlyEnabledSubscriptionsContributeUsageAndNodes(t *testing.T) {
	summary := summarizeSubscriptions([]service.SubscriptionResponse{
		{
			Enabled:          true,
			HealthyNodeCount: 2,
			NodeCount:        3,
			Usage: &service.SubscriptionUsageResponse{
				UploadBytes:   10,
				DownloadBytes: 20,
				TotalBytes:    100,
			},
		},
		{
			Enabled:          false,
			HealthyNodeCount: 5,
			NodeCount:        8,
			Usage: &service.SubscriptionUsageResponse{
				UploadBytes:   1000,
				DownloadBytes: 2000,
				TotalBytes:    9000,
			},
		},
	})

	if summary.EnabledCount != 1 || summary.DisabledCount != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", summary.EnabledCount, summary.DisabledCount)
	}
	if summary.HealthyNodeCount != 2 || summary.NodeCount != 3 {
		t.Fatalf("nodes = %d/%d, want 2/3", summary.HealthyNodeCount, summary.NodeCount)
	}
	if summary.UsageUsedBytes != 30 || summary.UsageTotalBytes != 100 || summary.UsageRemainingBytes != 70 {
		t.Fatalf("usage = used %d total %d remaining %d, want 30/100/70", summary.UsageUsedBytes, summary.UsageTotalBytes, summary.UsageRemainingBytes)
	}
}
